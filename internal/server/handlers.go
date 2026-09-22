package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

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

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	c := s.deps.Config
	writeJSON(w, http.StatusOK, metaResponse{
		App:        "TradeSys",
		Version:    s.deps.Version,
		DisplayTZ:  c.DisplayTZID,
		Disclaimer: Disclaimer,
		ServerTime: s.deps.Now(),
		Features: metaFeatures{
			AI:           c.LLMConfigured(),
			Telegram:     c.TelegramConfigured(),
			Search:       c.SearchConfigured(),
			AlphaVantage: c.AlphaVantageConfigured(),
		},
		Providers: s.deps.Router.ProviderNames(),
		Intervals: []string{"1m", "5m", "15m", "1h", "1d", "1wk"},
	})
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	degraded := s.deps.Health.DegradedProviders()
	if degraded == nil {
		degraded = []string{}
	}
	resp := healthResponse{
		Providers:          s.deps.Health.Snapshot(),
		MarketDataDegraded: s.deps.Health.MarketDataDegraded(),
		DegradedProviders:  degraded,
		Budgets:            map[string]budgetView{},
		CheckedAt:          s.deps.Now(),
	}
	for name, b := range s.deps.Budgets {
		used, limit, err := b.Usage(r.Context())
		if err != nil {
			// A budget we cannot read is not worth failing the health check
			// over — the health check is what the operator looks at when
			// things are already going wrong.
			s.deps.Log.Warn("could not read provider budget", "provider", name, "err", err)
			continue
		}
		resp.Budgets[name] = budgetView{Used: used, Limit: limit, Exhausted: limit > 0 && used >= limit}
	}
	writeJSON(w, http.StatusOK, resp)
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

func (s *Server) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	entries, err := s.deps.Store.ListWatchlist(r.Context())
	if err != nil {
		s.deps.Log.Error("could not list watchlist", "err", err)
		writeError(w, http.StatusInternalServerError, "watchlist_unavailable", "Could not read the watchlist.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": s.enrichWatchlist(r.Context(), entries),
	})
}

// enrichWatchlist attaches a quote and a sparkline to every row.
//
// Shared by the legacy single list and by each of the five named lists, so
// both render identically -- the rail should not look different depending on
// which endpoint filled it.
func (s *Server) enrichWatchlist(ctx context.Context, entries []storage.WatchlistEntry) []watchlistItem {
	items := make([]watchlistItem, len(entries))
	var wg sync.WaitGroup
	// A modest cap: enough parallelism to keep the rail snappy, few enough
	// concurrent calls that we do not look like an attack to an upstream.
	sem := make(chan struct{}, 6)

	for i, e := range entries {
		items[i] = watchlistItem{
			Symbol:   e.Symbol.String(),
			Ticker:   e.Symbol.Ticker,
			Exchange: string(e.Symbol.Exchange),
			Currency: e.Symbol.Currency(),
			Note:     e.Note,
			AddedAt:  e.AddedAt,
		}
		wg.Add(1)
		go func(i int, sym marketdata.Symbol) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.fillWatchlistItem(ctx, &items[i], sym)
		}(i, e.Symbol)
	}
	wg.Wait()
	return items
}

// enrichWatchlistCached is the fast first paint for the watchlist rail. It
// reads only persisted bars and never waits on an upstream provider. The
// browser follows it with the regular enriched request and swaps in fresh
// prices when that completes.
func (s *Server) enrichWatchlistCached(ctx context.Context, entries []storage.WatchlistEntry) []watchlistItem {
	items := make([]watchlistItem, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, e := range entries {
		items[i] = watchlistItem{
			Symbol:   e.Symbol.String(),
			Ticker:   e.Symbol.Ticker,
			Exchange: string(e.Symbol.Exchange),
			Currency: e.Symbol.Currency(),
			Note:     e.Note,
			AddedAt:  e.AddedAt,
			Stale:    true,
		}
		wg.Add(1)
		go func(i int, sym marketdata.Symbol) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			series, err := s.deps.Store.LoadCandles(ctx, sym, marketdata.Interval1d, sparklineBars)
			if err != nil || len(series.Candles) == 0 {
				items[i].Error = "Refreshing price…"
				return
			}
			s.fillWatchlistItemFromSeries(&items[i], series)
		}(i, e.Symbol)
	}
	wg.Wait()
	return items
}

// fillWatchlistItem attaches quote and sparkline data, recording a per-row
// error instead of propagating a failure.
func (s *Server) fillWatchlistItem(ctx context.Context, item *watchlistItem, sym marketdata.Symbol) {
	series, err := s.deps.Router.Candles(ctx, sym, marketdata.Interval1d, sparklineBars)
	if err != nil {
		item.Error = "No price data available."
		s.deps.Log.Debug("watchlist sparkline unavailable", "symbol", sym, "err", err)
		return
	}
	s.fillWatchlistItemFromSeries(item, marketdata.CachedSeries{
		Candles: series.Candles, Source: series.Source,
		ResolvedSymbol: series.ResolvedSymbol, FetchedAt: series.FetchedAt,
	})
	item.Stale = series.Stale
}

func (s *Server) fillWatchlistItemFromSeries(item *watchlistItem, series marketdata.CachedSeries) {
	item.Source = series.Source
	item.ResolvedSymbol = series.ResolvedSymbol
	item.Spark = make([]float64, 0, len(series.Candles))
	for _, c := range series.Candles {
		item.Spark = append(item.Spark, c.Close)
	}

	// Derive the quote from the same series rather than making a second
	// upstream call: the two numbers then always agree, and the left rail
	// costs one request per symbol instead of two.
	if n := len(series.Candles); n > 0 {
		last := series.Candles[n-1]
		price := last.Close
		item.Price = &price
		if n > 1 {
			prev := series.Candles[n-2].Close
			change := price - prev
			item.Change = &change
			if prev != 0 {
				pct := change / prev * 100
				item.ChangePercent = &pct
			}
		}
	}
}

type addWatchlistRequest struct {
	Symbol string `json:"symbol"`
	Note   string `json:"note"`
}

func (s *Server) handleWatchlistAdd(w http.ResponseWriter, r *http.Request) {
	var req addWatchlistRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	sym, err := marketdata.ParseSymbol(req.Symbol)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_symbol", err.Error())
		return
	}
	if err := s.deps.Store.AddWatchlist(r.Context(), sym, req.Note); err != nil {
		s.deps.Log.Error("could not add to watchlist", "symbol", sym, "err", err)
		writeError(w, http.StatusInternalServerError, "watchlist_write_failed", "Could not save to the watchlist.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"symbol": sym.String()})
}

func (s *Server) handleWatchlistRemove(w http.ResponseWriter, r *http.Request) {
	sym, err := marketdata.ParseSymbol(chi.URLParam(r, "symbol"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_symbol", err.Error())
		return
	}
	if err := s.deps.Store.RemoveWatchlist(r.Context(), sym); err != nil {
		s.deps.Log.Error("could not remove from watchlist", "symbol", sym, "err", err)
		writeError(w, http.StatusInternalServerError, "watchlist_write_failed", "Could not update the watchlist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

func (s *Server) handleCandles(w http.ResponseWriter, r *http.Request) {
	sym, err := marketdata.ParseSymbol(chi.URLParam(r, "symbol"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_symbol", err.Error())
		return
	}
	interval := marketdata.Interval1d
	if raw := r.URL.Query().Get("interval"); raw != "" {
		interval, err = marketdata.ParseInterval(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_interval", err.Error())
			return
		}
	}
	limit := clampInt(intParam(r, "limit", 300), 1, maxCandles)

	series, err := s.deps.Router.Candles(r.Context(), sym, interval, limit)
	if err != nil {
		s.deps.Log.Warn("candles unavailable", "symbol", sym, "interval", interval, "err", err)
		status, code := http.StatusBadGateway, "no_market_data"
		if errors.Is(err, marketdata.ErrNoData) {
			status, code = http.StatusNotFound, "unknown_symbol"
		}
		writeError(w, status, code, "No price data is available for "+sym.String()+" right now.")
		return
	}
	writeJSON(w, http.StatusOK, candlesResponse{
		Symbol:         sym.String(),
		Interval:       string(interval),
		Currency:       sym.Currency(),
		Candles:        series.Candles,
		Source:         series.Source,
		ResolvedSymbol: series.ResolvedSymbol,
		FetchedAt:      series.FetchedAt,
		Stale:          series.Stale,
	})
}

type quoteResponse struct {
	Symbol    string           `json:"symbol"`
	Currency  string           `json:"currency"`
	Quote     marketdata.Quote `json:"quote"`
	Source    string           `json:"source"`
	FetchedAt time.Time        `json:"fetched_at"`
	Stale     bool             `json:"stale"`
}

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	sym, err := marketdata.ParseSymbol(chi.URLParam(r, "symbol"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_symbol", err.Error())
		return
	}
	res, err := s.deps.Router.Quote(r.Context(), sym)
	if err != nil {
		s.deps.Log.Warn("quote unavailable", "symbol", sym, "err", err)
		status, code := http.StatusBadGateway, "no_market_data"
		if errors.Is(err, marketdata.ErrNoData) {
			status, code = http.StatusNotFound, "unknown_symbol"
		}
		writeError(w, status, code, "No quote is available for "+sym.String()+" right now.")
		return
	}
	writeJSON(w, http.StatusOK, quoteResponse{
		Symbol:    sym.String(),
		Currency:  sym.Currency(),
		Quote:     res.Quote,
		Source:    res.Source,
		FetchedAt: res.FetchedAt,
		Stale:     res.Stale,
	})
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

func marketdataInterval(s string) (marketdata.Interval, error) { return marketdata.ParseInterval(s) }

// searchResult is one row of the symbol picker.
type searchResult struct {
	Symbol   string `json:"symbol"`
	Ticker   string `json:"ticker"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	Currency string `json:"currency"`
	Kind     string `json:"type"`
}

// handleSearchSymbols finds instruments by company name or ticker.
//
// This is what makes the watchlist more than the two symbols it ships with:
// without discovery, an operator has to already know that Infosys is
// INFY.NSE before they can add it.
func (s *Server) handleSearchSymbols(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": []searchResult{}})
		return
	}
	if s.deps.Searcher == nil {
		writeError(w, http.StatusServiceUnavailable, "search_unavailable",
			"Symbol search needs the yfinance provider. You can still add a symbol by typing it exactly, for example RELIANCE.NSE.")
		return
	}

	limit := clampInt(intParam(r, "limit", 10), 1, 25)
	found, err := s.deps.Searcher.SearchSymbols(r.Context(), query, limit)
	if err != nil {
		s.deps.Log.Warn("symbol search failed", "query", query, "err", err)
		writeError(w, http.StatusBadGateway, "search_unavailable",
			"Symbol search is unavailable right now. You can still add a symbol by typing it exactly.")
		return
	}

	out := make([]searchResult, 0, len(found))
	for _, f := range found {
		out = append(out, searchResult{
			Symbol:   f.Symbol.String(),
			Ticker:   f.Symbol.Ticker,
			Name:     f.Name,
			Exchange: f.Exchange,
			Currency: f.Symbol.Currency(),
			Kind:     f.Kind,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}
