// Package alphavantage adapts the Alpha Vantage REST API to
// marketdata.Provider.
//
// The free tier allows roughly 25 requests per day. That budget is enforced
// here, before any network call, against a persistent counter — so restarting
// the process cannot reset it and burn the operator's quota. When the budget is
// spent the adapter returns marketdata.ErrBudgetExhausted, which the router
// treats as a routine "try someone else" rather than a fault.
package alphavantage

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

// DefaultBaseURL is Alpha Vantage's API host. Overridden in tests.
const DefaultBaseURL = "https://www.alphavantage.co"

// ProviderName is the stable identifier used for budget rows and health.
const ProviderName = "alphavantage"

// Budget is the persistence port the adapter needs to enforce its daily quota.
// It is declared here, at the point of use, so this package does not depend on
// the storage package.
type Budget interface {
	ConsumeBudget(ctx context.Context, provider, period string, limit int) (allowed bool, remaining int, err error)
	BudgetUsage(ctx context.Context, provider, period string) (used int, err error)
}

// Client is the Alpha Vantage adapter.
type Client struct {
	baseURL    string
	apiKey     string
	dailyLimit int
	budget     Budget
	http       *http.Client
	now        func() time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at a different host, used by tests.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// WithClock replaces the time source, used by tests to cross a day boundary.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// New builds an Alpha Vantage adapter. A zero or negative dailyLimit disables
// the provider entirely rather than allowing unlimited calls, because an
// unbounded free-tier key gets blocked within minutes.
func New(apiKey string, dailyLimit int, budget Budget, opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		apiKey:     apiKey,
		dailyLimit: dailyLimit,
		budget:     budget,
		http:       &http.Client{Timeout: 20 * time.Second},
		now:        func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name identifies this provider in health reporting and cache provenance.
func (c *Client) Name() string { return ProviderName }

// Configured reports whether an API key was supplied. An unconfigured adapter
// is a normal state — the app runs on Yahoo alone — and is reported as
// "unconfigured" rather than "down".
func (c *Client) Configured() bool { return c.apiKey != "" }

// Period is the budget bucket for a moment in time. Alpha Vantage resets its
// free-tier counter daily, so the bucket is a UTC calendar date.
func (c *Client) Period() string { return c.now().UTC().Format("2006-01-02") }

// Usage reports how many of today's requests have been spent.
func (c *Client) Usage(ctx context.Context) (used, limit int, err error) {
	if c.budget == nil {
		return 0, c.dailyLimit, nil
	}
	used, err = c.budget.BudgetUsage(ctx, ProviderName, c.Period())
	return used, c.dailyLimit, err
}

// vendorSymbol renders a canonical symbol in Alpha Vantage's dialect, which is
// the same spelling we use canonically.
func vendorSymbol(sym marketdata.Symbol) string { return sym.String() }

// spend consumes one unit of the daily budget, or reports that it is gone.
func (c *Client) spend(ctx context.Context) error {
	if !c.Configured() {
		return fmt.Errorf("%w: alphavantage has no API key", marketdata.ErrNotSupported)
	}
	if c.dailyLimit <= 0 {
		return fmt.Errorf("%w: alphavantage daily limit is %d", marketdata.ErrBudgetExhausted, c.dailyLimit)
	}
	if c.budget == nil {
		return nil
	}
	allowed, _, err := c.budget.ConsumeBudget(ctx, ProviderName, c.Period(), c.dailyLimit)
	if err != nil {
		return fmt.Errorf("alphavantage: budget check: %w", err)
	}
	if !allowed {
		return fmt.Errorf("%w: alphavantage daily limit of %d reached", marketdata.ErrBudgetExhausted, c.dailyLimit)
	}
	return nil
}

// get performs one budgeted API call and returns the raw body.
func (c *Client) get(ctx context.Context, params url.Values) ([]byte, error) {
	if err := c.spend(ctx); err != nil {
		return nil, err
	}
	params.Set("apikey", c.apiKey)
	endpoint := c.baseURL + "/query?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("alphavantage: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tradesys-dashboard/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("alphavantage: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("alphavantage: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("alphavantage: http %d: %s", resp.StatusCode, snippet(body))
	}
	return body, nil
}

// Candles returns up to limit recent bars, oldest first.
func (c *Client) Candles(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Bars, error) {
	params, seriesKey, err := candleParams(sym, iv, limit)
	if err != nil {
		return marketdata.Bars{}, err
	}
	body, err := c.get(ctx, params)
	if err != nil {
		return marketdata.Bars{}, err
	}
	candles, err := ParseTimeSeries(body, seriesKey)
	if err != nil {
		return marketdata.Bars{}, err
	}
	if len(candles) == 0 {
		return marketdata.Bars{}, fmt.Errorf("%w: alphavantage returned no bars for %s", marketdata.ErrNoData, sym)
	}
	if limit > 0 && len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return marketdata.Bars{Candles: candles}, nil
}

// candleParams builds the query for an interval and names the JSON key the
// series will arrive under, which Alpha Vantage varies per function.
func candleParams(sym marketdata.Symbol, iv marketdata.Interval, limit int) (url.Values, string, error) {
	v := url.Values{"symbol": {vendorSymbol(sym)}}
	switch iv {
	case marketdata.Interval1d:
		v.Set("function", "TIME_SERIES_DAILY")
		// "compact" is 100 bars and is markedly cheaper to transfer; only ask
		// for the full 20-year history when the caller actually needs it.
		if limit > 100 {
			v.Set("outputsize", "full")
		} else {
			v.Set("outputsize", "compact")
		}
		return v, "Time Series (Daily)", nil
	case marketdata.Interval1wk:
		v.Set("function", "TIME_SERIES_WEEKLY")
		return v, "Weekly Time Series", nil
	case marketdata.Interval1m, marketdata.Interval5m, marketdata.Interval15m, marketdata.Interval1h:
		avInterval := map[marketdata.Interval]string{
			marketdata.Interval1m:  "1min",
			marketdata.Interval5m:  "5min",
			marketdata.Interval15m: "15min",
			marketdata.Interval1h:  "60min",
		}[iv]
		v.Set("function", "TIME_SERIES_INTRADAY")
		v.Set("interval", avInterval)
		if limit > 100 {
			v.Set("outputsize", "full")
		} else {
			v.Set("outputsize", "compact")
		}
		return v, "Time Series (" + avInterval + ")", nil
	default:
		return nil, "", fmt.Errorf("%w: alphavantage interval %q", marketdata.ErrNotSupported, iv)
	}
}

// Quote returns the latest price via GLOBAL_QUOTE.
func (c *Client) Quote(ctx context.Context, sym marketdata.Symbol) (marketdata.Quote, error) {
	body, err := c.get(ctx, url.Values{
		"function": {"GLOBAL_QUOTE"},
		"symbol":   {vendorSymbol(sym)},
	})
	if err != nil {
		return marketdata.Quote{}, err
	}
	return ParseGlobalQuote(body, sym)
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
