package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

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

// handleCalendar returns scheduled corporate events inside a horizon: what is
// already on the calendar, which a position needs before it is entered. The
// historical base rate belongs to the event type, so the page asks
// /api/eventstudy for it once.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
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

	rows, err := s.deps.Store.UpcomingCatalysts(r.Context(), time.Duration(days)*24*time.Hour, symbols, limit)
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
