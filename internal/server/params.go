package server

// Small helpers for reading and bounding query parameters.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

// sparklineBars is how many daily closes the left rail draws per symbol.
const sparklineBars = 30

// maxCandles caps a chart request so a hand-crafted URL cannot ask for a
// million bars and stall the process.
const maxCandles = 2000

type metaResponse struct {
	App        string       `json:"app"`
	Version    string       `json:"version"`
	DisplayTZ  string       `json:"display_tz"`
	Disclaimer string       `json:"disclaimer"`
	ServerTime time.Time    `json:"server_time"`
	Features   metaFeatures `json:"features"`
	Providers  []string     `json:"marketdata_providers"`
	Intervals  []string     `json:"intervals"`
}

// metaFeatures tells the frontend which panels to render at all, so an
// unconfigured feature shows a clear "not configured" state instead of a
// button that always fails.
type metaFeatures struct {
	AI           bool `json:"ai"`
	Telegram     bool `json:"telegram"`
	Search       bool `json:"search"`
	AlphaVantage bool `json:"alphavantage"`
}

type budgetView struct {
	Used      int  `json:"used"`
	Limit     int  `json:"limit"`
	Exhausted bool `json:"exhausted"`
}

type healthResponse struct {
	Providers []storage.ProviderHealth `json:"providers"`
	// MarketDataDegraded is true only when every configured provider is
	// failing, i.e. prices may be stale.
	MarketDataDegraded bool `json:"market_data_degraded"`
	// DegradedProviders names failing providers even when a fallback is
	// covering them, so a single dead source is visible rather than silent.
	DegradedProviders []string              `json:"degraded_providers"`
	Budgets           map[string]budgetView `json:"budgets"`
	CheckedAt         time.Time             `json:"checked_at"`
}

// watchlistItem is one left-rail row: identity, latest quote, and a sparkline.
// Quote and Spark are omitted rather than zeroed when they cannot be fetched,
// and Error explains why, so the UI can show a per-row problem without
// blanking the rail.
type watchlistItem struct {
	Symbol   string    `json:"symbol"`
	Ticker   string    `json:"ticker"`
	Exchange string    `json:"exchange"`
	Currency string    `json:"currency"`
	Note     string    `json:"note"`
	AddedAt  time.Time `json:"added_at"`

	Price          *float64  `json:"price,omitempty"`
	Change         *float64  `json:"change,omitempty"`
	ChangePercent  *float64  `json:"change_percent,omitempty"`
	Spark          []float64 `json:"spark,omitempty"`
	Source         string    `json:"source,omitempty"`
	ResolvedSymbol string    `json:"resolved_symbol,omitempty"`
	Stale          bool      `json:"stale"`
	Error          string    `json:"error,omitempty"`
}

type addWatchlistRequest struct {
	Symbol string `json:"symbol"`
	Note   string `json:"note"`
}

type candlesResponse struct {
	Symbol   string              `json:"symbol"`
	Interval string              `json:"interval"`
	Currency string              `json:"currency"`
	Candles  []marketdata.Candle `json:"candles"`
	Source   string              `json:"source"`
	// ResolvedSymbol is set when the provider served a different listing than
	// the one requested, so the UI can say which venue the prices came from.
	ResolvedSymbol string    `json:"resolved_symbol,omitempty"`
	FetchedAt      time.Time `json:"fetched_at"`
	Stale          bool      `json:"stale"`
}

type quoteResponse struct {
	Symbol    string           `json:"symbol"`
	Currency  string           `json:"currency"`
	Quote     marketdata.Quote `json:"quote"`
	Source    string           `json:"source"`
	FetchedAt time.Time        `json:"fetched_at"`
	Stale     bool             `json:"stale"`
}

func intParam(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return def
	}
	return n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// marketdataParse and marketdataInterval are thin aliases so the algorithm
// handlers read cleanly without importing the package under a second name.
func marketdataParse(s string) (marketdata.Symbol, error) { return marketdata.ParseSymbol(s) }
