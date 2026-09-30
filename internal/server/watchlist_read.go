package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

// The single-list watchlist routes, and the enrichment that turns a list of
// symbols into something worth looking at.

func (s *Server) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	entries, err := s.deps.Store.ListWatchlist(r.Context())
	if err != nil {
		s.deps.Log.Error("could not list watchlist", "err", err)
		writeError(w, http.StatusInternalServerError, "watchlist_unavailable", "Could not read the watchlist.")
		return
	}

	// ?cached=1 reads persisted bars only, for callers that need the list
	// rather than live prices -- a cold quote refresh took five seconds, and
	// the page asking only wanted to know which symbols were on it.
	items := []watchlistItem(nil)
	if r.URL.Query().Get("cached") == "1" {
		items = s.enrichWatchlistCached(r.Context(), entries)
	} else {
		items = s.enrichWatchlist(r.Context(), entries)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
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
