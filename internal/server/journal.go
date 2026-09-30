package server

import (
	"net/http"
	"strconv"
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
