// Package storage declares the persistence ports the application depends on.
// Only internal/storage/sqlite implements them today; the interfaces exist so a
// Postgres implementation can be dropped in without touching call sites.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/search"
)

// PriceCache is satisfied by the same methods marketdata.Cache requires, so a
// Store can be handed straight to a marketdata.Router.
var _ marketdata.Cache = (PriceCache)(nil)

// PriceCache persists every datum we fetch. Writes are upserts keyed by
// (symbol, interval, bar time) so repeated polling converges rather than
// duplicating rows.
type PriceCache interface {
	LoadCandles(ctx context.Context, sym marketdata.Symbol, interval marketdata.Interval, limit int) (marketdata.CachedSeries, error)
	SaveCandles(ctx context.Context, sym marketdata.Symbol, interval marketdata.Interval, source string, bars marketdata.Bars, requestedLimit int) error
	LoadQuote(ctx context.Context, sym marketdata.Symbol) (marketdata.CachedQuote, error)
	SaveQuote(ctx context.Context, source string, q marketdata.Quote) error
}

// WatchlistEntry is one row of the operator's watchlist.
type WatchlistEntry struct {
	Symbol   marketdata.Symbol
	Note     string
	Position int
	AddedAt  time.Time
}

// ErrNotFound is returned when an addressed row does not exist.
//
// It lives here rather than in an implementation package so that HTTP
// handlers can map it to a 404 without knowing which database is behind them.
var ErrNotFound = errors.New("storage: not found")

// Stats is what the storage layer holds, for the health page.
type Stats struct {
	RawItems     int   `json:"raw_items"`
	PendingItems int   `json:"pending_items"`
	Events       int   `json:"events"`
	Classified   int   `json:"classified_events"`
	Evidence     int   `json:"evidence_links"`
	Entities     int   `json:"entity_links"`
	SizeBytes    int64 `json:"size_bytes"`
}

// EventFilter narrows an event query.
//
// It lives here rather than in an implementation package because both the
// SQLite and Postgres stores accept it, and because the HTTP layer builds one
// without wanting to know which is in use.
type EventFilter struct {
	Symbol        string
	Types         []string
	Sectors       []string
	MinImportance int
	Since         time.Time
	// Query is free text, matched against the headline and summary.
	Query        string
	Limit        int
	Offset       int
	OfficialOnly bool
	// IndexOnly restricts the feed to index constituents, plus events that
	// name no company at all — macro and sector items, whose whole point is
	// that they are not about one filer.
	//
	// The default view of a market, rather than a filter to go looking for:
	// without it the feed is the exchange's filing queue, in which the long
	// tail of listed companies outnumbers the ones anyone follows by an order
	// of magnitude.
	IndexOnly bool
	// MacroTypes are the event types exempt from IndexOnly, because they name
	// no company by nature rather than by failing to resolve one. Supplied by
	// the caller so the taxonomy stays defined in one place.
	MacroTypes []string
	// IncludeUnattributedWatchlist admits events that came only from a
	// per-instrument watchlist search and resolved to no listed company.
	//
	// Off by default on the market feed and on for a symbol's own page,
	// because those answer different questions. A watchlist search for a
	// foreign holding returns a hundred routine items — insider-sale filings,
	// analyst notes — which belong on that instrument's page and drown the
	// market feed if allowed into it.
	IncludeUnattributedWatchlist bool
	// OrderByContentAge ranks by publication time where one is known,
	// falling back to discovery.
	//
	// The market feed deliberately does not do this: it is a timeline of what
	// arrived, and reordering it by publisher timestamps would let one source
	// with a skewed clock dominate. A single company's page is the opposite
	// case — the reader wants the latest news about that company, and an
	// article resurfaced from July is not it.
	OrderByContentAge bool
}

// WatchlistStore holds the symbols the operators care about. It drives the left
// rail, the morning brief, and the news poller's query set.
type WatchlistStore interface {
	ListWatchlist(ctx context.Context) ([]WatchlistEntry, error)
	AddWatchlist(ctx context.Context, sym marketdata.Symbol, note string) error
	RemoveWatchlist(ctx context.Context, sym marketdata.Symbol) error
}

// BudgetStore enforces per-provider request budgets that reset on a period
// boundary. Alpha Vantage's free tier (~25 requests/day) is the reason this
// exists: overrunning it degrades the whole app, so the count must survive
// process restarts.
type BudgetStore interface {
	// ConsumeBudget atomically increments the period's counter if doing so
	// stays within limit, reporting whether the caller may proceed and how
	// many requests remain afterwards.
	ConsumeBudget(ctx context.Context, provider, period string, limit int) (allowed bool, remaining int, err error)
	// BudgetUsage reports current consumption without consuming anything.
	BudgetUsage(ctx context.Context, provider, period string) (used int, err error)
}

// HealthStatus is the traffic light the top bar renders per dependency.
type HealthStatus string

const (
	// HealthOK means the last interaction succeeded.
	HealthOK HealthStatus = "ok"
	// HealthDegraded means the dependency is failing but the feature it backs
	// is still being served, typically from cache or a fallback provider.
	HealthDegraded HealthStatus = "degraded"
	// HealthDown means the dependency is failing and nothing is covering it.
	HealthDown HealthStatus = "down"
	// HealthUnconfigured means no credentials were supplied, so the
	// dependency was never contacted. This is a normal state, not a fault.
	HealthUnconfigured HealthStatus = "unconfigured"
)

// ProviderHealth is the persisted health record for one external dependency.
type ProviderHealth struct {
	Provider    string       `json:"provider"`
	Kind        string       `json:"kind"`
	Status      HealthStatus `json:"status"`
	Message     string       `json:"message"`
	LastOKAt    *time.Time   `json:"last_ok_at"`
	LastErrorAt *time.Time   `json:"last_error_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// HealthStore persists dependency health so the status dots survive a restart
// and so we can tell "never tried" apart from "tried and failed".
type HealthStore interface {
	RecordHealth(ctx context.Context, h ProviderHealth) error
	ListHealth(ctx context.Context) ([]ProviderHealth, error)
}

// Store is the aggregate handed to wiring code in main.
type Store interface {
	PriceCache
	WatchlistStore
	BudgetStore
	HealthStore
	alerts.AlgorithmStore
	alerts.AlertStore
	ai.BudgetStore
	ai.OutputStore
	news.Store
	// WatchedSymbols is the watchlist as parsed symbols, used by the news
	// poller and the morning brief.
	WatchedSymbols(ctx context.Context) ([]marketdata.Symbol, error)
	// RecentAlertSummaries feeds the morning brief.
	RecentAlertSummaries(ctx context.Context, since time.Time, limit int) ([]ai.RecentAlert, error)
	// LoadSearch and SaveSearch back the search cache.
	LoadSearch(ctx context.Context, key string) (search.Results, bool, error)
	SaveSearch(ctx context.Context, key string, r search.Results) error
	Close() error
}
