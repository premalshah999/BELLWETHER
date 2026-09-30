// Package twelvedata adapts the Twelve Data REST API to marketdata.Provider.
//
// It is the first fallback behind the yfinance sidecar: it answers from a
// datacenter IP that Yahoo may refuse, and its free tier (about 800 requests a
// day) is enough to carry the app through a sidecar outage.
package twelvedata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// DefaultBaseURL is Twelve Data's API host. Overridden in tests.
const DefaultBaseURL = "https://api.twelvedata.com"

// ProviderName is the identifier used in health reporting, cache provenance,
// and budget rows.
const ProviderName = "twelvedata"

// Budget is the persistence port used to enforce the daily request allowance.
// Declared here, at the point of use, so this package does not depend on the
// storage package.
type Budget interface {
	ConsumeBudget(ctx context.Context, provider, period string, limit int) (allowed bool, remaining int, err error)
	BudgetUsage(ctx context.Context, provider, period string) (used int, err error)
}

// Client is the Twelve Data adapter.
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

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithClock replaces the time source, used by tests to cross a day boundary.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// New builds a Twelve Data adapter.
//
// A zero or negative dailyLimit disables the provider rather than allowing
// unlimited calls: an unbounded free-tier key gets suspended, not throttled.
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

// Name identifies this provider.
func (c *Client) Name() string { return ProviderName }

// Configured reports whether an API key was supplied.
func (c *Client) Configured() bool { return c.apiKey != "" }

// Period is the budget bucket: Twelve Data's free allowance resets daily.
func (c *Client) Period() string { return c.now().UTC().Format("2006-01-02") }

// Usage reports how much of today's allowance has been spent.
func (c *Client) Usage(ctx context.Context) (used, limit int, err error) {
	if c.budget == nil {
		return 0, c.dailyLimit, nil
	}
	used, err = c.budget.BudgetUsage(ctx, ProviderName, c.Period())
	return used, c.dailyLimit, err
}

// vendorParams renders a symbol in Twelve Data's dialect: a bare US ticker.
// Indices are left to the providers that carry them.
func vendorParams(sym marketdata.Symbol) (url.Values, error) {
	if sym.Exchange != marketdata.ExchangeUS {
		return nil, fmt.Errorf("%w: twelvedata has no mapping for exchange %q",
			marketdata.ErrNotSupported, sym.Exchange)
	}
	return url.Values{"symbol": {sym.Ticker}}, nil
}

// vendorInterval maps our interval onto Twelve Data's spelling.
func vendorInterval(iv marketdata.Interval) (string, error) {
	switch iv {
	case marketdata.Interval1m:
		return "1min", nil
	case marketdata.Interval5m:
		return "5min", nil
	case marketdata.Interval15m:
		return "15min", nil
	case marketdata.Interval1h:
		return "1h", nil
	case marketdata.Interval1d:
		return "1day", nil
	case marketdata.Interval1wk:
		return "1week", nil
	default:
		return "", fmt.Errorf("%w: twelvedata interval %q", marketdata.ErrNotSupported, iv)
	}
}

// maxOutputSize is Twelve Data's documented ceiling for one request.
const maxOutputSize = 5000

// spend consumes one unit of the daily allowance.
func (c *Client) spend(ctx context.Context) error {
	if !c.Configured() {
		return fmt.Errorf("%w: twelvedata has no API key", marketdata.ErrNotSupported)
	}
	if c.dailyLimit <= 0 {
		return fmt.Errorf("%w: twelvedata daily limit is %d", marketdata.ErrBudgetExhausted, c.dailyLimit)
	}
	if c.budget == nil {
		return nil
	}
	allowed, _, err := c.budget.ConsumeBudget(ctx, ProviderName, c.Period(), c.dailyLimit)
	if err != nil {
		return fmt.Errorf("twelvedata: budget check: %w", err)
	}
	if !allowed {
		return fmt.Errorf("%w: twelvedata daily limit of %d reached", marketdata.ErrBudgetExhausted, c.dailyLimit)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if err := c.spend(ctx); err != nil {
		return nil, err
	}
	params.Set("apikey", c.apiKey)
	// UTC throughout: bar timestamps are stored in UTC.
	params.Set("timezone", "UTC")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("twelvedata: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tradesys-dashboard/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("twelvedata: request: %w", redact(err, c.apiKey))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("twelvedata: read body: %w", err)
	}
	// Errors arrive as a JSON envelope, sometimes with a non-200 status and
	// sometimes with 200. ParseTimeSeries inspects the body either way, so the
	// status code alone is not the decision.
	return body, nil
}

// Candles returns up to limit recent bars, oldest first.
func (c *Client) Candles(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Bars, error) {
	params, err := vendorParams(sym)
	if err != nil {
		return marketdata.Bars{}, err
	}
	interval, err := vendorInterval(iv)
	if err != nil {
		return marketdata.Bars{}, err
	}
	if limit <= 0 {
		limit = 200
	}
	params.Set("interval", interval)
	params.Set("outputsize", strconv.Itoa(min(limit, maxOutputSize)))

	body, err := c.get(ctx, "/time_series", params)
	if err != nil {
		return marketdata.Bars{}, err
	}
	candles, err := ParseTimeSeries(body)
	if err != nil {
		return marketdata.Bars{}, err
	}
	if len(candles) == 0 {
		return marketdata.Bars{}, fmt.Errorf("%w: twelvedata returned no bars for %s", marketdata.ErrNoData, sym)
	}
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return marketdata.Bars{Candles: candles}, nil
}

// Quote returns the latest price.
//
// It reads the tail of the time series rather than the dedicated /quote
// endpoint: both cost one API credit, and this way the quote and the chart are
// guaranteed to agree.
func (c *Client) Quote(ctx context.Context, sym marketdata.Symbol) (marketdata.Quote, error) {
	bars, err := c.Candles(ctx, sym, marketdata.Interval1d, 2)
	if err != nil {
		return marketdata.Quote{}, err
	}
	candles := bars.Candles
	last := candles[len(candles)-1]

	q := marketdata.Quote{
		Symbol:   sym,
		Price:    last.Close,
		DayHigh:  last.High,
		DayLow:   last.Low,
		Volume:   last.Volume,
		Currency: sym.Currency(),
		AsOf:     last.Time,
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func redact(err error, key string) error {
	if key == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), key, "[redacted]"))
}

var _ marketdata.Provider = (*Client)(nil)
