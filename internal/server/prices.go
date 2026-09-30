package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Price and symbol lookup: candles, quotes, and symbol search.

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
		Symbol:    sym.String(),
		Interval:  string(interval),
		Currency:  sym.Currency(),
		Candles:   series.Candles,
		Source:    series.Source,
		FetchedAt: series.FetchedAt,
		Stale:     series.Stale,
	})
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
// This is what makes the watchlist more than the symbols it ships with:
// without it an operator has to already know a company's ticker to add it.
func (s *Server) handleSearchSymbols(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": []searchResult{}})
		return
	}
	if s.deps.Searcher == nil {
		writeError(w, http.StatusServiceUnavailable, "search_unavailable",
			"Symbol search needs the yfinance provider. You can still add a symbol by typing it exactly, for example AAPL.")
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
