package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// journalEntryView is one closed trade with its catalyst attribution,
// dates rendered plainly (see tradeViewOf's own note on why).
type journalEntryView struct {
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

	// Catalyst is the most recent classified event naming this symbol,
	// discovered on or before entry and within the lookback window -- nil
	// when nothing in the archive qualifies. See postgres.TradesWithCatalysts
	// for why "discovered", never "published" or "occurred".
	Catalyst *journalCatalystView `json:"catalyst,omitempty"`
}

type journalCatalystView struct {
	EventID         int64   `json:"event_id"`
	EventType       string  `json:"event_type"`
	Headline        string  `json:"headline"`
	Official        bool    `json:"official"`
	BestTrust       int     `json:"best_trust"`
	DaysBeforeEntry float64 `json:"days_before_entry"`
}

// handleJournal answers the question no trading journal and no alt-data
// platform can answer alone: of the trades that actually closed, which
// were preceded by something this app's own event archive classified, and
// did having one (especially an official one) correlate with a better
// outcome?
func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	trades, err := s.deps.Store.TradesWithCatalysts(r.Context(), limit)
	if err != nil {
		s.deps.Log.Error("journal query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the trade journal.")
		return
	}

	// A catalyst older than the hot window lives on the news archive, so an
	// entry the primary could not attribute is asked of the archive too.
	if a := s.deps.NewsArchive; a != nil {
		for i, t := range trades {
			if t.EventID != nil {
				continue
			}
			if c, err := a.DB().LatestCatalyst(r.Context(), t.Symbol, t.OpenedAt); err == nil && c != nil {
				c.Trade = t.Trade
				trades[i] = *c
			}
		}
	}

	entries := make([]journalEntryView, len(trades))
	for i, t := range trades {
		entries[i] = journalEntryView{
			ID: t.ID, Symbol: t.Symbol, Quantity: t.Quantity,
			EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice,
			OpenedAt: t.OpenedAt.Format("2006-01-02"), ClosedAt: t.ClosedAt.Format("2006-01-02"),
			RealizedPnL: t.RealizedPnL, Account: t.Account, Notes: t.Notes,
		}
		if t.EventID != nil {
			entries[i].Catalyst = &journalCatalystView{
				EventID: *t.EventID, EventType: t.EventType, Headline: t.Headline,
				Official: t.Official, BestTrust: t.BestTrust,
				DaysBeforeEntry: derefOr(t.DaysBeforeEntry, 0),
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"trades": entries})
}

func derefOr(f *float64, fallback float64) float64 {
	if f == nil {
		return fallback
	}
	return *f
}

// handleRecordTrade stores a closed trade entered by hand.
func (s *Server) handleRecordTrade(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Symbol     string  `json:"symbol"`
		Quantity   float64 `json:"quantity"`
		EntryPrice float64 `json:"entry_price"`
		ExitPrice  float64 `json:"exit_price"`
		OpenedAt   string  `json:"opened_at"`
		ClosedAt   string  `json:"closed_at"`
		Account    string  `json:"account"`
		Notes      string  `json:"notes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is not valid JSON.")
		return
	}
	sym, err := marketdata.ParseSymbol(body.Symbol)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "That is not a symbol this system recognises.")
		return
	}
	opened, err1 := time.Parse("2006-01-02", body.OpenedAt)
	closed, err2 := time.Parse("2006-01-02", body.ClosedAt)
	switch {
	case err1 != nil || err2 != nil:
		writeError(w, http.StatusBadRequest, "invalid_request", "Dates must look like 2026-03-15.")
		return
	case closed.Before(opened):
		writeError(w, http.StatusBadRequest, "invalid_request", "A trade cannot close before it opened.")
		return
	case body.Quantity <= 0 || body.EntryPrice <= 0 || body.ExitPrice <= 0:
		writeError(w, http.StatusBadRequest, "invalid_request", "Quantity and both prices must be positive.")
		return
	}
	t, err := s.deps.Store.RecordTrade(r.Context(), postgres.Trade{
		Symbol: sym.String(), Quantity: body.Quantity, EntryPrice: body.EntryPrice, ExitPrice: body.ExitPrice,
		OpenedAt: opened, ClosedAt: closed, Account: strings.TrimSpace(body.Account), Notes: strings.TrimSpace(body.Notes),
	})
	if err != nil {
		s.deps.Log.Error("record trade failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not record the trade.")
		return
	}
	writeJSON(w, http.StatusCreated, tradeViewOf(t))
}

// handleDeleteTrade removes one trade from the journal.
func (s *Server) handleDeleteTrade(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "trade")
	if !ok {
		return
	}
	if err := s.deps.Store.DeleteTrade(r.Context(), id); errors.Is(err, postgres.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such trade.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not delete the trade.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
