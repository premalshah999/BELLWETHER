// Package marketdata defines the provider-independent view of prices that the
// rest of the application consumes. Nothing outside this package's adapter
// subdirectories may know that Alpha Vantage or Yahoo exist.
package marketdata

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Interval is a candle width. Only the values below are legal; adapters map
// them onto whatever their upstream calls the same thing.
type Interval string

const (
	Interval1m  Interval = "1m"
	Interval5m  Interval = "5m"
	Interval15m Interval = "15m"
	Interval1h  Interval = "1h"
	Interval1d  Interval = "1d"
	Interval1wk Interval = "1wk"
)

// ParseInterval validates an interval arriving from config, the API, or an
// algorithm definition.
func ParseInterval(s string) (Interval, error) {
	switch Interval(s) {
	case Interval1m, Interval5m, Interval15m, Interval1h, Interval1d, Interval1wk:
		return Interval(s), nil
	default:
		return "", fmt.Errorf("unsupported interval %q", s)
	}
}

// Duration is the wall-clock width of one candle at this interval.
func (i Interval) Duration() time.Duration {
	switch i {
	case Interval1m:
		return time.Minute
	case Interval5m:
		return 5 * time.Minute
	case Interval15m:
		return 15 * time.Minute
	case Interval1h:
		return time.Hour
	case Interval1d:
		return 24 * time.Hour
	case Interval1wk:
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// Intraday reports whether this interval subdivides a trading day. Intraday
// data has a much shorter useful cache life than daily data.
func (i Interval) Intraday() bool {
	return i != Interval1d && i != Interval1wk
}

// Candle is one OHLCV bar. Time is always the UTC open of the bar.
type Candle struct {
	Time   time.Time `json:"t"`
	Open   float64   `json:"o"`
	High   float64   `json:"h"`
	Low    float64   `json:"l"`
	Close  float64   `json:"c"`
	Volume float64   `json:"v"`
}

// Quote is the latest observed price for a symbol.
type Quote struct {
	Symbol        Symbol    `json:"-"`
	Price         float64   `json:"price"`
	PrevClose     float64   `json:"prev_close"`
	Change        float64   `json:"change"`
	ChangePercent float64   `json:"change_percent"`
	DayHigh       float64   `json:"day_high"`
	DayLow        float64   `json:"day_low"`
	Volume        float64   `json:"volume"`
	Currency      string    `json:"currency"`
	AsOf          time.Time `json:"as_of"`
}

// Series is a run of candles plus the provenance the UI needs in order to be
// honest about what the operator is looking at.
type Series struct {
	Symbol    Symbol    `json:"-"`
	Interval  Interval  `json:"interval"`
	Candles   []Candle  `json:"candles"`
	Source    string    `json:"source"`
	FetchedAt time.Time `json:"fetched_at"`
	// Stale is set when every live provider failed and this data came out of
	// the cache past its TTL. The UI must surface this, never hide it.
	Stale bool `json:"stale"`
}

// ErrNotSupported is returned by an adapter that cannot serve a request at all
// (wrong asset class, interval it does not carry). The router treats it as a
// reason to move to the next provider without counting a failure against the
// provider's health.
var ErrNotSupported = errors.New("marketdata: request not supported by this provider")

// ErrBudgetExhausted is returned when an adapter's own rate budget is spent for
// the period. Like ErrNotSupported this is an expected condition, not a fault.
var ErrBudgetExhausted = errors.New("marketdata: provider request budget exhausted")

// ErrNoData is returned when a provider answered successfully but has nothing
// for this symbol.
var ErrNoData = errors.New("marketdata: provider has no data for symbol")

// Bars is what a provider returns for a candle request: the data, plus any
// provenance only the provider knows.
type Bars struct {
	// Candles are the bars, oldest first.
	Candles []Candle
}

// Provider is the seam every upstream price source sits behind. Adapters must
// be safe for concurrent use.
type Provider interface {
	// Name is the stable identifier used in health reporting, cache
	// provenance, and logs.
	Name() string
	// Candles returns up to limit most-recent bars, oldest first.
	Candles(ctx context.Context, sym Symbol, interval Interval, limit int) (Bars, error)
	// Quote returns the latest price.
	Quote(ctx context.Context, sym Symbol) (Quote, error)
}

// SortCandles orders bars oldest-first and drops exact timestamp duplicates,
// keeping the last occurrence. Adapters run their parsed output through this so
// downstream indicator math can assume monotonic time.
func SortCandles(in []Candle) []Candle {
	if len(in) == 0 {
		return nil
	}
	byTime := make(map[int64]Candle, len(in))
	order := make([]int64, 0, len(in))
	for _, c := range in {
		k := c.Time.UTC().Unix()
		if _, seen := byTime[k]; !seen {
			order = append(order, k)
		}
		byTime[k] = c
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && order[j] < order[j-1]; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	out := make([]Candle, 0, len(order))
	for _, k := range order {
		out = append(out, byTime[k])
	}
	return out
}

// CachedSeries is candles read back out of the cache, with the provenance
// needed to judge whether they are still fresh.
type CachedSeries struct {
	Candles   []Candle
	Source    string
	FetchedAt time.Time
	// RequestedLimit is the largest number of bars previously asked of a
	// provider for this symbol and interval. Without it the cache cannot
	// tell "we only ever wanted 30 bars" apart from "30 bars is all that
	// exists", and a small warm-up request would starve every larger one for
	// the rest of the TTL.
	RequestedLimit int
}

// CachedQuote is a quote read back out of the cache.
type CachedQuote struct {
	Quote     Quote
	Source    string
	FetchedAt time.Time
}

// Cache is the persistence port the Router needs. Every datum a provider
// returns is written through it, so the app can keep answering from disk when
// every upstream is unreachable.
type Cache interface {
	LoadCandles(ctx context.Context, sym Symbol, interval Interval, limit int) (CachedSeries, error)
	SaveCandles(ctx context.Context, sym Symbol, interval Interval, source string, bars Bars, requestedLimit int) error
	LoadQuote(ctx context.Context, sym Symbol) (CachedQuote, error)
	SaveQuote(ctx context.Context, source string, q Quote) error
}

// Outcome records one attempt against one provider, so health reporting lives
// outside this package and the Router stays free of storage types.
type Outcome struct {
	Provider string
	// OK is true when the provider returned usable data.
	OK bool
	// Skipped marks an expected non-answer — no credentials, budget spent, or
	// an unsupported symbol or interval. These must not count as faults, or
	// an unconfigured Alpha Vantage key would light up a red dot forever.
	Skipped bool
	Err     error
}

// OutcomeSink receives every provider attempt. It must not block.
type OutcomeSink func(ctx context.Context, o Outcome)

// SearchResult is one instrument matched by a symbol search.
type SearchResult struct {
	// Symbol is canonical and directly usable: adding it to a watchlist or an
	// algorithm requires no further translation.
	Symbol   Symbol `json:"-"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	// Kind is "EQUITY" or "ETF".
	Kind string `json:"type"`
}

// SymbolSearcher finds instruments by name or ticker.
//
// It is separate from Provider because looking a company up and fetching its
// prices are different capabilities: a provider may do one without the other,
// and the watchlist needs discovery even when the price chain is degraded.
type SymbolSearcher interface {
	SearchSymbols(ctx context.Context, query string, limit int) ([]SearchResult, error)
}
