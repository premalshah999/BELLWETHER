// Package yahoo adapts Yahoo Finance's unofficial chart endpoint to the
// marketdata.Provider interface.
//
// This endpoint is undocumented and unsupported. It changes without notice, it
// rate-limits aggressively from datacenter IP ranges, and it will eventually
// break. Every assumption about its shape is therefore confined to parse.go,
// which works on bytes and is exercised by tests against recorded payloads. A
// shape change should surface as a clean parse error that the router degrades
// on, never as a panic or a silent zero.
package yahoo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// DefaultBaseURL is Yahoo's chart host. Overridden in tests.
const DefaultBaseURL = "https://query1.finance.yahoo.com"

// browserUA is sent because Yahoo rejects requests with a default Go user
// agent. It is not an attempt to evade rate limits, which we respect via the
// router's caching and TTLs.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Client is the Yahoo adapter.
type Client struct {
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at a different host, used by tests.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New builds a Yahoo adapter. It needs no credentials.
func New(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name identifies this provider in health reporting and cache provenance.
func (c *Client) Name() string { return "yahoo" }

// vendorSymbol renders a canonical symbol in Yahoo's dialect. Yahoo suffixes
// Indian listings by venue: .BO for BSE, .NS for NSE.
func vendorSymbol(sym marketdata.Symbol) (string, error) {
	switch sym.Exchange {
	case marketdata.ExchangeUS:
		return sym.Ticker, nil
	case marketdata.ExchangeBSE:
		return sym.Ticker + ".BO", nil
	case marketdata.ExchangeNSE:
		return sym.Ticker + ".NS", nil
	default:
		return "", fmt.Errorf("%w: yahoo has no mapping for exchange %q", marketdata.ErrNotSupported, sym.Exchange)
	}
}

// minUsableBars is the point below which a daily series is not worth charting.
// Yahoo answers 200 OK with a single bar for some Indian listings — RELIANCE.BO
// is one — which is not an error it reports, just a hole in its data.
const minUsableBars = 5

// venueFallbacks lists the sibling listing to try when the requested venue
// comes back unusable. An Indian blue chip trades on both BSE and NSE, so a
// hole in one feed does not have to mean an empty chart. Any substitution is
// reported in Bars.ResolvedSymbol and surfaced to the operator; it is never
// passed off as the venue they asked for.
var venueFallbacks = map[marketdata.Exchange]marketdata.Exchange{
	marketdata.ExchangeBSE: marketdata.ExchangeNSE,
	marketdata.ExchangeNSE: marketdata.ExchangeBSE,
}

// candidates returns the listings to try, in order.
func candidates(sym marketdata.Symbol) []marketdata.Symbol {
	out := []marketdata.Symbol{sym}
	if alt, ok := venueFallbacks[sym.Exchange]; ok {
		out = append(out, marketdata.Symbol{Ticker: sym.Ticker, Exchange: alt})
	}
	return out
}

// vendorInterval maps our interval onto Yahoo's spelling.
func vendorInterval(iv marketdata.Interval) (string, error) {
	switch iv {
	case marketdata.Interval1m:
		return "1m", nil
	case marketdata.Interval5m:
		return "5m", nil
	case marketdata.Interval15m:
		return "15m", nil
	case marketdata.Interval1h:
		return "60m", nil
	case marketdata.Interval1d:
		return "1d", nil
	case marketdata.Interval1wk:
		return "1wk", nil
	default:
		return "", fmt.Errorf("%w: yahoo interval %q", marketdata.ErrNotSupported, iv)
	}
}

// vendorRange picks the lookback window to request.
//
// Yahoo caps intraday history hard — roughly 7 days at 1m and 60 days at
// 5m/15m — and rejects a range that exceeds the cap for the requested
// granularity. So we never round up past the cap: we pick the smallest
// standard range that covers the bars we want, restricted to ranges the cap
// permits, and settle for the largest permitted range when the caller wants
// more history than Yahoo will ever serve at that granularity.
func vendorRange(iv marketdata.Interval, limit int) string {
	if limit <= 0 {
		limit = 200
	}
	span := iv.Duration() * time.Duration(limit)
	// Trading sessions are a fraction of calendar time, so pad the window:
	// heavily for intraday, and by the ~5/7 trading-day ratio for daily bars.
	if iv.Intraday() {
		span *= 3
	} else {
		span = span * 7 / 5
	}
	needDays := int(span.Hours()/24) + 1

	candidates := []struct {
		label string
		days  int
	}{
		{"1d", 1}, {"5d", 5}, {"1mo", 31}, {"3mo", 92}, {"6mo", 183},
		{"1y", 366}, {"2y", 731}, {"5y", 1827}, {"10y", 3653},
	}
	// Yahoo's documented retention ceiling per intraday granularity.
	maxDays := map[marketdata.Interval]int{
		marketdata.Interval1m:  7,
		marketdata.Interval5m:  60,
		marketdata.Interval15m: 60,
		marketdata.Interval1h:  730,
	}

	capDays, capped := maxDays[iv]
	best := ""
	for _, c := range candidates {
		if capped && c.days > capDays {
			break
		}
		best = c.label // largest range still permitted by the cap
		if c.days >= needDays {
			return c.label
		}
	}
	if best == "" {
		// The cap is tighter than the smallest standard range.
		return "1d"
	}
	if capped {
		return best
	}
	return "max"
}

func (c *Client) fetchChart(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) ([]byte, error) {
	vs, err := vendorSymbol(sym)
	if err != nil {
		return nil, err
	}
	vi, err := vendorInterval(iv)
	if err != nil {
		return nil, err
	}

	q := url.Values{
		"interval":       {vi},
		"range":          {vendorRange(iv, limit)},
		"includePrePost": {"false"},
		"events":         {"div,split"},
	}
	endpoint := fmt.Sprintf("%s/v8/finance/chart/%s?%s", c.baseURL, url.PathEscape(vs), q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("yahoo: build request: %w", err)
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yahoo: request %s: %w", vs, err)
	}
	defer resp.Body.Close()

	// Cap the read so a hostile or broken upstream cannot exhaust memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("yahoo: read body %s: %w", vs, err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: yahoo 404 for %s", marketdata.ErrNoData, vs)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("yahoo: rate limited (429) for %s; Yahoo throttles datacenter IPs", vs)
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("yahoo: http %d for %s: %s", resp.StatusCode, vs, snippet(body))
	}
	return body, nil
}

// Candles returns up to limit recent bars, oldest first.
//
// When the requested venue yields too little history to chart, the sibling
// Indian venue is tried and the substitution is reported back rather than
// hidden.
func (c *Client) Candles(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Bars, error) {
	want := limit
	if want <= 0 {
		want = 200
	}

	var (
		best      marketdata.Bars
		bestFrom  marketdata.Symbol
		firstErr  error
		attempted int
	)

	for i, cand := range candidates(sym) {
		bars, err := c.candlesFor(ctx, cand, iv, limit)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		attempted++
		if len(bars.Candles) > len(best.Candles) {
			best, bestFrom = bars, cand
		}
		// Good enough: stop before spending a request on the sibling venue.
		if len(best.Candles) >= minUsableBars || len(best.Candles) >= want {
			if i == 0 {
				return best, nil
			}
			break
		}
	}

	if attempted == 0 {
		if firstErr != nil {
			return marketdata.Bars{}, firstErr
		}
		return marketdata.Bars{}, fmt.Errorf("%w: yahoo returned no bars for %s", marketdata.ErrNoData, sym)
	}
	if len(best.Candles) == 0 {
		return marketdata.Bars{}, fmt.Errorf("%w: yahoo returned no bars for %s", marketdata.ErrNoData, sym)
	}
	if bestFrom != sym {
		best.ResolvedSymbol = bestFrom.String()
	}
	return best, nil
}

// candlesFor fetches one specific listing.
func (c *Client) candlesFor(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Bars, error) {
	body, err := c.fetchChart(ctx, sym, iv, limit)
	if err != nil {
		return marketdata.Bars{}, err
	}
	candles, _, err := ParseChart(body)
	if err != nil {
		return marketdata.Bars{}, err
	}
	if len(candles) == 0 {
		return marketdata.Bars{}, fmt.Errorf("%w: yahoo returned no bars for %s", marketdata.ErrNoData, sym)
	}
	if limit > 0 && len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return marketdata.Bars{Candles: candles}, nil
}

// Quote reads the latest price out of the chart endpoint's metadata block. We
// deliberately do not use Yahoo's v7 quote endpoint, which now demands a
// cookie/crumb handshake and breaks more often than the chart endpoint does.
func (c *Client) Quote(ctx context.Context, sym marketdata.Symbol) (marketdata.Quote, error) {
	var firstErr error
	for _, cand := range candidates(sym) {
		body, err := c.fetchChart(ctx, cand, marketdata.Interval1d, 5)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		candles, meta, err := ParseChart(body)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Report the quote under the symbol the operator asked for; the
		// venue that served it is recorded on the candle series.
		q, err := quoteFromMeta(sym, meta, candles)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return q, nil
	}
	if firstErr != nil {
		return marketdata.Quote{}, firstErr
	}
	return marketdata.Quote{}, fmt.Errorf("%w: yahoo has no quote for %s", marketdata.ErrNoData, sym)
}

func snippet(b []byte) string {
	const max = 180
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

var _ marketdata.Provider = (*Client)(nil)
