package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// CalendarStore is the slice of storage the calendar route needs.
type CalendarStore interface {
	UpcomingCatalysts(ctx context.Context, within time.Duration, symbols []string, limit int) ([]postgres.UpcomingCatalyst, error)
}

func (s *Server) calendarStore() (CalendarStore, bool) {
	st, ok := s.deps.Store.(CalendarStore)
	return st, ok
}

const (
	defaultCalendarDays = 30
	// Beyond a quarter the upstream's dates are estimates rather than
	// announcements, and the ingest side already refuses to store them.
	maxCalendarDays = 120
)

type calendarResponse struct {
	Catalysts []postgres.UpcomingCatalyst `json:"catalysts"`
	Days      int                         `json:"days"`
	Total     int                         `json:"total"`
}

// handleCalendar returns scheduled corporate events inside a horizon.
//
// This is the only endpoint in the app that answers a question about the
// future. Everything else reports what the archive observed; this reports
// what is already on the schedule -- which is the half a position needs
// before it is entered rather than after.
//
// The historical half is deliberately not computed here. A base rate belongs
// to an event *type*, not to a symbol, so the page asks /api/eventstudy once
// for it rather than having this endpoint run the same study for every row.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	store, ok := s.calendarStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The calendar is not available.")
		return
	}
	q := r.URL.Query()

	days := defaultCalendarDays
	if raw := strings.TrimSpace(q.Get("days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > maxCalendarDays {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"days must be a whole number between 1 and "+strconv.Itoa(maxCalendarDays)+".")
			return
		}
		days = n
	}

	// Symbols are canonicalised before they reach SQL, so a caller passing
	// "aapl" or "reliance.nse" matches the same rows the rest of the app
	// stores under AAPL and RELIANCE.NSE.
	var symbols []string
	if raw := strings.TrimSpace(q.Get("symbols")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			sym, err := marketdata.ParseSymbol(part)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request",
					"Not a symbol this app recognises: "+part)
				return
			}
			symbols = append(symbols, sym.String())
		}
	}

	limit := atoiDefault(q.Get("limit"), 200)

	rows, err := store.UpcomingCatalysts(r.Context(), time.Duration(days)*24*time.Hour, symbols, limit)
	if err != nil {
		s.deps.Log.Error("upcoming catalysts failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the calendar.")
		return
	}
	if rows == nil {
		rows = []postgres.UpcomingCatalyst{}
	}

	writeJSON(w, http.StatusOK, calendarResponse{Catalysts: rows, Days: days, Total: len(rows)})
}
