// Package yfin talks to the yfinance sidecar.
//
// The sidecar wraps the yfinance library, which is the de facto standard tool
// for Yahoo Finance data and handles the parts that are tedious to reimplement
// correctly: session and crumb management, split and dividend adjustment, and
// the transport Yahoo actually accepts.
//
// It is the depth provider. Where Alpha Vantage's free tier gives 100 bars and
// Twelve Data's excludes Indian listings, this gives five or more years across
// NSE, BSE and US markets with no daily request budget to ration. It is also
// the least contractual of the three, so it leads the chain and the budgeted
// providers stand behind it.
package yfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// ProviderName identifies this provider in health reporting and provenance.
const ProviderName = "yfinance"

// DefaultYears is how much history to request. Five years of daily bars is
// enough for a 200-day average to be meaningful from the first visible candle,
// and enough to see a full cycle.
const DefaultYears = 5.0

// Client calls the sidecar.
type Client struct {
	baseURL string
	http    *http.Client
	// adjust selects total-return prices over as-traded ones.
	adjust bool
	// now is injectable so session-dependent behaviour is testable without
	// waiting for a market to open.
	now func() time.Time
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) Option {
	return func(c *Client) { c.now = now }
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithDividendAdjustment returns prices adjusted for dividends as well as
// splits. Off by default: an adjusted series answers "what would I have made"
// rather than "what did it trade at", and a dashboard showing a price that
// never appeared on a screen is confusing.
func WithDividendAdjustment(on bool) Option { return func(c *Client) { c.adjust = on } }

// New builds a client for the sidecar at baseURL. An empty baseURL leaves the
// provider unconfigured, which the router treats as absent rather than broken.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		// Five years of daily bars for a cold symbol takes a few seconds
		// upstream; this is not a latency-sensitive path.
		http: &http.Client{Timeout: 90 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name identifies this provider.
func (c *Client) Name() string { return ProviderName }

// Configured reports whether a sidecar address was supplied.
func (c *Client) Configured() bool { return c.baseURL != "" }

// vendorSymbol renders a canonical symbol in Yahoo's dialect, which is what
// yfinance takes: .BO for BSE, .NS for NSE, bare for US.
func vendorSymbol(sym marketdata.Symbol) (string, error) {
	switch sym.Exchange {
	case marketdata.ExchangeUS:
		return sym.Ticker, nil
	case marketdata.ExchangeBSE:
		return sym.Ticker + ".BO", nil
	case marketdata.ExchangeNSE:
		return sym.Ticker + ".NS", nil
	case marketdata.ExchangeIndex:
		// Yahoo's own dialect for a benchmark index: a caret prefix, no
		// suffix. "^GSPC", not "GSPC.INDEX".
		return "^" + sym.Ticker, nil
	default:
		return "", fmt.Errorf("%w: yfinance has no mapping for exchange %q",
			marketdata.ErrNotSupported, sym.Exchange)
	}
}

// minUsableBars is the point below which a series is not worth charting.
// Yahoo's BSE feed is missing for some names that trade fine on NSE.
const minUsableBars = 5

// venueFallbacks names the sibling listing to try when the requested venue has
// no usable history.
var venueFallbacks = map[marketdata.Exchange]marketdata.Exchange{
	marketdata.ExchangeBSE: marketdata.ExchangeNSE,
	marketdata.ExchangeNSE: marketdata.ExchangeBSE,
}

func candidates(sym marketdata.Symbol) []marketdata.Symbol {
	out := []marketdata.Symbol{sym}
	if alt, ok := venueFallbacks[sym.Exchange]; ok {
		out = append(out, marketdata.Symbol{Ticker: sym.Ticker, Exchange: alt})
	}
	return out
}

// response is the sidecar's payload.
type response struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Candles  []struct {
		T string  `json:"t"`
		O float64 `json:"o"`
		H float64 `json:"h"`
		L float64 `json:"l"`
		C float64 `json:"c"`
		V float64 `json:"v"`
	} `json:"candles"`
	Adjusted bool `json:"adjusted"`
	Splits   []struct {
		Date  string  `json:"date"`
		Ratio float64 `json:"ratio"`
	} `json:"splits"`
	Currency string `json:"currency"`
	Error    string `json:"error"`
}

// Candles returns up to limit recent bars, oldest first, falling back to the
// sibling Indian venue when the requested one has no usable history.
func (c *Client) Candles(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Bars, error) {
	if !c.Configured() {
		return marketdata.Bars{}, fmt.Errorf("%w: no yfinance sidecar configured", marketdata.ErrNotSupported)
	}
	if limit <= 0 {
		limit = 200
	}

	var (
		best     marketdata.Bars
		bestFrom marketdata.Symbol
		firstErr error
	)

	for i, cand := range candidates(sym) {
		bars, err := c.fetch(ctx, cand, iv, limit)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(bars.Candles) > len(best.Candles) {
			best, bestFrom = bars, cand
		}
		if len(best.Candles) >= minUsableBars {
			if i == 0 {
				return best, nil
			}
			break
		}
	}

	if len(best.Candles) == 0 {
		if firstErr != nil {
			return marketdata.Bars{}, firstErr
		}
		return marketdata.Bars{}, fmt.Errorf("%w: yfinance has no bars for %s", marketdata.ErrNoData, sym)
	}
	if bestFrom != sym {
		best.ResolvedSymbol = bestFrom.String()
	}
	return best, nil
}

func (c *Client) fetch(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Bars, error) {
	vendor, err := vendorSymbol(sym)
	if err != nil {
		return marketdata.Bars{}, err
	}

	params := url.Values{
		"symbol":   {vendor},
		"interval": {string(iv)},
		"years":    {strconv.FormatFloat(yearsFor(iv, limit), 'f', -1, 64)},
		"adjust":   {strconv.FormatBool(c.adjust)},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/candles?"+params.Encode(), nil)
	if err != nil {
		return marketdata.Bars{}, fmt.Errorf("yfinance: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return marketdata.Bars{}, fmt.Errorf("yfinance: sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return marketdata.Bars{}, fmt.Errorf("yfinance: read response: %w", err)
	}

	var parsed response
	if err := json.Unmarshal(body, &parsed); err != nil {
		return marketdata.Bars{}, fmt.Errorf("yfinance: decode response (http %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != "" {
		return marketdata.Bars{}, fmt.Errorf("yfinance: %s", parsed.Error)
	}
	if resp.StatusCode >= 400 {
		return marketdata.Bars{}, fmt.Errorf("yfinance: sidecar returned http %d", resp.StatusCode)
	}
	if len(parsed.Candles) == 0 {
		return marketdata.Bars{}, fmt.Errorf("%w: yfinance returned no bars for %s", marketdata.ErrNoData, sym)
	}

	candles := make([]marketdata.Candle, 0, len(parsed.Candles))
	for _, raw := range parsed.Candles {
		t, err := time.Parse(time.RFC3339, raw.T)
		if err != nil {
			continue
		}
		if raw.O <= 0 || raw.H <= 0 || raw.L <= 0 || raw.C <= 0 {
			// A non-positive price is not data; drop the bar rather than
			// charting it.
			continue
		}
		candles = append(candles, marketdata.Candle{
			Time: t.UTC(), Open: raw.O, High: raw.H, Low: raw.L, Close: raw.C, Volume: raw.V,
		})
	}
	if len(candles) == 0 {
		return marketdata.Bars{}, fmt.Errorf("%w: yfinance returned %d unusable rows for %s",
			marketdata.ErrNoData, len(parsed.Candles), sym)
	}

	candles = marketdata.SortCandles(candles)
	if limit > 0 && len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return marketdata.Bars{Candles: candles}, nil
}

// yearsFor converts a bar count into a span of years to request.
//
// It never asks for less than DefaultYears on daily or weekly data, even for a
// small request. This provider has no request budget, the router caches what
// comes back, and fetching five years once is strictly better than fetching
// thirty bars now and five years again the moment someone opens a chart.
func yearsFor(iv marketdata.Interval, limit int) float64 {
	switch iv {
	case marketdata.Interval1d:
		// Roughly 252 trading days a year, with headroom for holidays.
		years := float64(limit)/252.0 + 0.25
		if years < DefaultYears {
			return DefaultYears
		}
		return years
	case marketdata.Interval1wk:
		years := float64(limit)/52.0 + 0.5
		if years < DefaultYears {
			return DefaultYears
		}
		return years
	default:
		// Intraday asks for the span the request actually needs.
		//
		// This returned a constant, and the sidecar read that as "give me the
		// cap" — so drawing thirty five-minute bars downloaded fifty-nine days
		// of them, about 4,400 bars, on every uncached request. The chart felt
		// slow for a reason that had nothing to do with the market.
		per := barsPerSession(iv)
		if per <= 0 {
			return 1
		}
		sessions := math.Ceil(float64(limit)/per) + 1
		return sessions / 252.0
	}
}

// barsPerSession is how many bars of an interval one NSE session produces.
// NSE trades 09:15 to 15:30, six and a quarter hours.
func barsPerSession(iv marketdata.Interval) float64 {
	switch iv {
	case marketdata.Interval1m:
		return 375
	case marketdata.Interval5m:
		return 75
	case marketdata.Interval15m:
		return 25
	case marketdata.Interval1h:
		return 7
	}
	return 0
}

// quoteAsOf reports the time a price should be understood to be valid for.
//
// A daily bar's timestamp is the session's label, not an observation: NSE
// stamps it at midnight IST. Reporting that as the age of the price makes a
// closing price look sixteen hours old and a live one look like it came from
// the middle of the night.
func quoteAsOf(sym marketdata.Symbol, barTime, now time.Time) time.Time {
	if sym.Exchange.SessionActive(now) {
		return now
	}
	if close, ok := sym.Exchange.SessionClose(barTime); ok {
		return close
	}
	return barTime
}

// Quote returns the latest price.
//
// While a venue is trading this reads the minute bars, not the daily one, and
// that distinction is the whole difference between a live price and a stale
// one. The provider's daily bar for the session in progress updates only
// every several minutes: polled once a second it returns byte-identical data,
// so a quote built from it never changes, nothing is ever published to the
// live stream, and the interface sits frozen through an entire trading day
// while reporting itself as connected. Measured on AAPL three minutes after
// the open, the daily bar held one price across four fetches a minute apart
// while the minute bars moved on every one of them.
//
// Outside a session the daily bar is exactly right: it is settled, it is the
// close, and there is no intraday data to prefer.
func (c *Client) Quote(ctx context.Context, sym marketdata.Symbol) (marketdata.Quote, error) {
	bars, err := c.Candles(ctx, sym, marketdata.Interval1d, 5)
	if err != nil {
		return marketdata.Quote{}, err
	}
	candles := bars.Candles
	last := candles[len(candles)-1]

	// Today's figures, from the minute bars, when there are any to have.
	price, high, low, volume := last.Close, last.High, last.Low, last.Volume
	if sym.Exchange.SessionActive(c.clock()) {
		if live, ok := c.intraday(ctx, sym); ok {
			price, high, low, volume = live.price, live.high, live.low, live.volume
		}
	}

	q := marketdata.Quote{
		Symbol:   sym,
		Price:    price,
		DayHigh:  high,
		DayLow:   low,
		Volume:   volume,
		Currency: sym.Currency(),
		// When the venue is trading, the last daily bar is the one being
		// written right now and its timestamp is the session's label, not an
		// observation time -- for NSE that label is midnight IST, so a live
		// price would announce itself as being from the small hours. What the
		// caller needs to know is how old the number is, so during a session
		// that is the moment it was read.
		AsOf: quoteAsOf(sym, last.Time, c.clock()),
	}
	// The change is always measured against the previous session's close,
	// which only the daily series carries. Using the first intraday bar
	// instead would report the move since the opening print rather than since
	// yesterday, and those are different numbers on any day with a gap.
	if len(candles) >= 2 {
		q.PrevClose = candles[len(candles)-2].Close
		q.Change = q.Price - q.PrevClose
		if q.PrevClose != 0 {
			q.ChangePercent = q.Change / q.PrevClose * 100
		}
	}
	return q, nil
}

// todaysBars is the session so far, aggregated from minute bars.
type todaysBars struct {
	price, high, low, volume float64
}

// intraday summarises the current session from one-minute bars.
//
// High, low and volume are recomputed from the bars rather than taken from
// the daily record, because the daily record is the thing that is stale. The
// forming minute frequently arrives with zero volume and a close equal to the
// previous one; it is skipped for the price so a quote never reports a bar
// that has not traded yet.
func (c *Client) intraday(ctx context.Context, sym marketdata.Symbol) (todaysBars, bool) {
	// A full session is 390 minutes for the US and 375 for India; 400 covers
	// either with room, and the router caches it for the minute.
	bars, err := c.Candles(ctx, sym, marketdata.Interval1m, 400)
	if err != nil || len(bars.Candles) == 0 {
		return todaysBars{}, false
	}

	day := bars.Candles[len(bars.Candles)-1].Time.UTC().Format("2006-01-02")
	out := todaysBars{low: math.Inf(1)}
	var seen bool
	for _, b := range bars.Candles {
		// Minute history can span more than one session; only today counts
		// toward today's high, low and volume.
		if b.Time.UTC().Format("2006-01-02") != day || b.Close <= 0 {
			continue
		}
		seen = true
		out.price = b.Close
		out.volume += b.Volume
		if b.High > out.high {
			out.high = b.High
		}
		if b.Low > 0 && b.Low < out.low {
			out.low = b.Low
		}
	}
	if !seen || out.price <= 0 {
		return todaysBars{}, false
	}
	if math.IsInf(out.low, 1) {
		out.low = out.price
	}
	return out, true
}

var _ marketdata.Provider = (*Client)(nil)
