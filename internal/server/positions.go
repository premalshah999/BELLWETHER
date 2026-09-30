package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// positionView is one position with the market data that turns "100 shares
// at $150" into "worth this, up or down that much" -- the whole reason to
// enter a position rather than leave it in a spreadsheet.
type positionView struct {
	ID            int64    `json:"id"`
	Symbol        string   `json:"symbol"`
	Quantity      float64  `json:"quantity"`
	CostBasis     float64  `json:"cost_basis"`
	OpenedAt      string   `json:"opened_at"`
	Account       string   `json:"account,omitempty"`
	Notes         string   `json:"notes,omitempty"`
	Price         *float64 `json:"price,omitempty"`
	MarketValue   *float64 `json:"market_value,omitempty"`
	UnrealizedPnL *float64 `json:"unrealized_pnl,omitempty"`
	UnrealizedPct *float64 `json:"unrealized_pct,omitempty"`
	PriceStale    bool     `json:"price_stale,omitempty"`
	PriceError    string   `json:"price_error,omitempty"`
}

// handleListPositions returns every open position, priced live.
func (s *Server) handleListPositions(w http.ResponseWriter, r *http.Request) {
	positions, err := s.deps.Store.ListPositions(r.Context())
	if err != nil {
		s.deps.Log.Error("could not list positions", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read positions.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"positions": s.enrichPositions(r.Context(), positions)})
}

// enrichPositions attaches a live price and derived P&L to each position,
// fetched concurrently the same way the watchlist rail does -- a portfolio
// page is exactly the kind of view where a dozen sequential price lookups
// would be the difference between snappy and sluggish.
func (s *Server) enrichPositions(ctx context.Context, positions []postgres.Position) []positionView {
	out := make([]positionView, len(positions))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)

	for i, p := range positions {
		out[i] = positionView{
			ID: p.ID, Symbol: p.Symbol, Quantity: p.Quantity, CostBasis: p.CostBasis,
			OpenedAt: p.OpenedAt.Format("2006-01-02"), Account: p.Account, Notes: p.Notes,
		}
		sym, err := marketdata.ParseSymbol(p.Symbol)
		if err != nil || s.deps.Router == nil {
			continue
		}
		wg.Add(1)
		go func(i int, sym marketdata.Symbol) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.fillPositionPrice(ctx, &out[i], sym)
		}(i, sym)
	}
	wg.Wait()
	return out
}

func (s *Server) fillPositionPrice(ctx context.Context, item *positionView, sym marketdata.Symbol) {
	series, err := s.deps.Router.Candles(ctx, sym, marketdata.Interval1d, 2)
	if err != nil || len(series.Candles) == 0 {
		item.PriceError = "No price data available."
		return
	}
	price := series.Candles[len(series.Candles)-1].Close
	item.Price = &price
	item.PriceStale = series.Stale

	value := price * item.Quantity
	item.MarketValue = &value
	pnl := (price - item.CostBasis) * item.Quantity
	item.UnrealizedPnL = &pnl
	if item.CostBasis > 0 {
		pct := (price/item.CostBasis - 1) * 100
		item.UnrealizedPct = &pct
	}
}

// handleCreatePosition adds a new open position.
func (s *Server) handleCreatePosition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Symbol    string  `json:"symbol"`
		Quantity  float64 `json:"quantity"`
		CostBasis float64 `json:"cost_basis"`
		OpenedAt  string  `json:"opened_at"`
		Account   string  `json:"account"`
		Notes     string  `json:"notes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body describing the position.")
		return
	}
	sym, err := marketdata.ParseSymbol(strings.TrimSpace(body.Symbol))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "That is not a symbol this app recognizes: "+err.Error())
		return
	}
	if body.Quantity <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Quantity must be greater than zero.")
		return
	}
	if body.CostBasis < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Cost basis cannot be negative.")
		return
	}
	opened, err := parseDateOrToday(body.OpenedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "opened_at must be a date like 2026-03-15.")
		return
	}
	id, err := s.deps.Store.SavePosition(r.Context(), postgres.Position{
		Symbol: sym.String(), Quantity: body.Quantity, CostBasis: body.CostBasis,
		OpenedAt: opened, Account: strings.TrimSpace(body.Account), Notes: body.Notes,
	})
	if err != nil {
		s.deps.Log.Error("could not save position", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not save the position.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// handleDeletePosition removes a position with no trade recorded -- for
// fixing a mis-entered position, not for exiting a real one.
func (s *Server) handleDeletePosition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "position")
	if !ok {
		return
	}
	if err := s.deps.Store.DeletePosition(r.Context(), id); err != nil {
		s.deps.Log.Error("could not delete position", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not delete the position.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleClosePosition reduces or fully closes a position and records the
// closed portion as a trade.
func (s *Server) handleClosePosition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "position")
	if !ok {
		return
	}
	var body struct {
		Quantity  float64 `json:"quantity"`
		ExitPrice float64 `json:"exit_price"`
		ClosedAt  string  `json:"closed_at"`
		Notes     string  `json:"notes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body describing the close.")
		return
	}
	if body.ExitPrice < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Exit price cannot be negative.")
		return
	}
	closed, err := parseDateOrToday(body.ClosedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "closed_at must be a date like 2026-03-15.")
		return
	}
	trade, err := s.deps.Store.ClosePosition(r.Context(), id, body.Quantity, body.ExitPrice, closed, body.Notes)
	if err != nil {
		// Every failure here (not found, over-close, bad quantity) is
		// something the operator typed wrong, not a system fault.
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tradeViewOf(trade))
}

// tradeView is a Trade with its DATE columns rendered as plain calendar
// strings rather than full RFC3339 timestamps -- opened_at and closed_at
// carry no time-of-day, and showing one (always midnight UTC) would read as
// a fact the data does not contain.
type tradeView struct {
	ID          int64   `json:"id"`
	Symbol      string  `json:"symbol"`
	Quantity    float64 `json:"quantity"`
	EntryPrice  float64 `json:"entry_price"`
	ExitPrice   float64 `json:"exit_price"`
	OpenedAt    string  `json:"opened_at"`
	ClosedAt    string  `json:"closed_at"`
	RealizedPnL float64 `json:"realized_pnl"`
	Account     string  `json:"account,omitempty"`
	Notes       string  `json:"notes,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

func tradeViewOf(t postgres.Trade) tradeView {
	return tradeView{
		ID: t.ID, Symbol: t.Symbol, Quantity: t.Quantity,
		EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice,
		OpenedAt: t.OpenedAt.Format("2006-01-02"), ClosedAt: t.ClosedAt.Format("2006-01-02"),
		RealizedPnL: t.RealizedPnL, Account: t.Account, Notes: t.Notes,
		CreatedAt: t.CreatedAt.Format(time.RFC3339),
	}
}

func parseDateOrToday(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC().Truncate(24 * time.Hour), nil
	}
	return time.Parse("2006-01-02", s)
}
