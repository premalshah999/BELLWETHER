// Package yfin talks to the yfinance sidecar, which wraps the yfinance
// library: Yahoo session handling, split and dividend adjustment, and years
// of history with no request budget. It leads the provider chain; the
// budgeted providers stand behind it.
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

// DefaultYears of daily history is fetched even for small requests: enough
// for a 200-day average from the first visible candle, fetched once and
// cached rather than re-fetched the moment a chart zooms out.
const DefaultYears = 5.0

// Client calls the sidecar.
type Client struct {
	baseURL string
	http    *http.Client
	adjust  bool // total-return prices rather than as-traded
	now     func() time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithDividendAdjustment returns dividend-adjusted prices as well as split
// adjusted ones. Off by default: a chart should show prices that traded.
func WithDividendAdjustment(on bool) Option { return func(c *Client) { c.adjust = on } }

// New builds a client for the sidecar at baseURL; an empty URL leaves the
// provider unconfigured.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		// Five cold years of daily bars take a few seconds upstream.
		http: &http.Client{Timeout: 90 * time.Second},
		now:  time.Now,
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

// vendorSymbol renders a symbol the way yfinance takes it: bare, or ^GSPC
// for an index.
func vendorSymbol(sym marketdata.Symbol) string {
	if sym.IsIndex() {
		return "^" + sym.Ticker
	}
	return sym.Ticker
}

type response struct {
	Candles []struct {
		T string  `json:"t"`
		O float64 `json:"o"`
		H float64 `json:"h"`
		L float64 `json:"l"`
		C float64 `json:"c"`
		V float64 `json:"v"`
	} `json:"candles"`
	Error string `json:"error"`
}

// Candles returns up to limit recent bars, oldest first.
func (c *Client) Candles(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Bars, error) {
	if !c.Configured() {
		return marketdata.Bars{}, fmt.Errorf("%w: no yfinance sidecar configured", marketdata.ErrNotSupported)
	}
	if limit <= 0 {
		limit = 200
	}
	params := url.Values{
		"symbol":   {vendorSymbol(sym)},
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
	switch {
	case parsed.Error != "":
		return marketdata.Bars{}, fmt.Errorf("yfinance: %s", parsed.Error)
	case resp.StatusCode >= 400:
		return marketdata.Bars{}, fmt.Errorf("yfinance: sidecar returned http %d", resp.StatusCode)
	}

	candles := make([]marketdata.Candle, 0, len(parsed.Candles))
	for _, raw := range parsed.Candles {
		t, err := time.Parse(time.RFC3339, raw.T)
		// A non-positive price is not data.
		if err != nil || raw.O <= 0 || raw.H <= 0 || raw.L <= 0 || raw.C <= 0 {
			continue
		}
		candles = append(candles, marketdata.Candle{
			Time: t.UTC(), Open: raw.O, High: raw.H, Low: raw.L, Close: raw.C, Volume: raw.V,
		})
	}
	if len(candles) == 0 {
		return marketdata.Bars{}, fmt.Errorf("%w: yfinance has no usable bars for %s", marketdata.ErrNoData, sym)
	}
	candles = marketdata.SortCandles(candles)
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return marketdata.Bars{Candles: candles}, nil
}

// yearsFor converts a bar count into the span of years to request: at least
// DefaultYears for daily and weekly bars, and only the sessions an intraday
// request needs (asking for a constant made a 30-bar chart download 59 days
// of five-minute bars).
func yearsFor(iv marketdata.Interval, limit int) float64 {
	switch iv {
	case marketdata.Interval1d:
		return math.Max(DefaultYears, float64(limit)/252+0.25)
	case marketdata.Interval1wk:
		return math.Max(DefaultYears, float64(limit)/52+0.5)
	}
	per := barsPerSession(iv)
	if per <= 0 {
		return 1
	}
	return (math.Ceil(float64(limit)/per) + 1) / 252
}

// barsPerSession is how many bars of an interval a 6.5-hour US session makes.
func barsPerSession(iv marketdata.Interval) float64 {
	switch iv {
	case marketdata.Interval1m:
		return 390
	case marketdata.Interval5m:
		return 78
	case marketdata.Interval15m:
		return 26
	case marketdata.Interval1h:
		return 7
	}
	return 0
}

// quoteAsOf is when a price should be understood to be valid: now during a
// session, else that session's close. A daily bar's timestamp labels the
// session and would make a closing price look hours old.
func quoteAsOf(sym marketdata.Symbol, barTime, now time.Time) time.Time {
	if sym.Exchange.SessionActive(now) {
		return now
	}
	if close, ok := sym.Exchange.SessionClose(barTime); ok {
		return close
	}
	return barTime
}

// Quote returns the latest price. During a session it reads minute bars: the
// provider's forming daily bar updates only every several minutes, so a quote
// built from it froze through the day while reporting itself connected. The
// change is always measured against the previous daily close.
func (c *Client) Quote(ctx context.Context, sym marketdata.Symbol) (marketdata.Quote, error) {
	bars, err := c.Candles(ctx, sym, marketdata.Interval1d, 5)
	if err != nil {
		return marketdata.Quote{}, err
	}
	candles := bars.Candles
	last := candles[len(candles)-1]
	price, high, low, volume := last.Close, last.High, last.Low, last.Volume
	if sym.Exchange.SessionActive(c.now()) {
		if live, ok := c.intraday(ctx, sym); ok {
			price, high, low, volume = live.price, live.high, live.low, live.volume
		}
	}
	q := marketdata.Quote{
		Symbol: sym, Price: price, DayHigh: high, DayLow: low, Volume: volume,
		Currency: sym.Currency(), AsOf: quoteAsOf(sym, last.Time, c.now()),
	}
	if len(candles) >= 2 {
		q.PrevClose = candles[len(candles)-2].Close
		q.Change = q.Price - q.PrevClose
		if q.PrevClose != 0 {
			q.ChangePercent = q.Change / q.PrevClose * 100
		}
	}
	return q, nil
}

type todaysBars struct{ price, high, low, volume float64 }

// intraday summarises today's session from one-minute bars. The forming
// minute often arrives with no trade in it, so non-positive closes are
// skipped rather than reported.
func (c *Client) intraday(ctx context.Context, sym marketdata.Symbol) (todaysBars, bool) {
	bars, err := c.Candles(ctx, sym, marketdata.Interval1m, 400) // a session is 390 minutes
	if err != nil || len(bars.Candles) == 0 {
		return todaysBars{}, false
	}
	day := func(t time.Time) string { return t.In(marketdata.Market).Format("2006-01-02") }
	today := day(bars.Candles[len(bars.Candles)-1].Time)
	out := todaysBars{low: math.Inf(1)}
	for _, b := range bars.Candles {
		if day(b.Time) != today || b.Close <= 0 {
			continue
		}
		out.price = b.Close
		out.volume += b.Volume
		out.high = math.Max(out.high, b.High)
		if b.Low > 0 {
			out.low = math.Min(out.low, b.Low)
		}
	}
	if out.price <= 0 {
		return todaysBars{}, false
	}
	if math.IsInf(out.low, 1) {
		out.low = out.price
	}
	return out, true
}

var _ marketdata.Provider = (*Client)(nil)
