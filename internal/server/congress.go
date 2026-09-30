package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// congressFilingEnvelope is one filing as the UI receives it -- the stored
// record plus the two fields that are computed rather than stored (the
// member's display name and the filing's own PDF url), so the frontend
// never has to reimplement either.
type congressFilingEnvelope struct {
	DocID                   string   `json:"doc_id"`
	Chamber                 string   `json:"chamber"`
	Member                  string   `json:"member"`
	StateDistrict           string   `json:"state_district"`
	FilingType              string   `json:"filing_type"`
	FilingDate              string   `json:"filing_date"`
	Symbols                 []string `json:"symbols"`
	UnresolvedTickers       []string `json:"unresolved_tickers,omitempty"`
	EarliestTransactionDate string   `json:"earliest_transaction_date,omitempty"`
	DisclosureDelayDays     *int     `json:"disclosure_delay_days,omitempty"`
	DocURL                  string   `json:"doc_url"`
}

// handleCongressFilings lists recent congressional PTR filings, optionally
// narrowed to one symbol -- the query the Congress page's "who traded this"
// view runs directly, and the same endpoint a symbol's own working-set
// panel can call to show its own congressional activity.
func (s *Server) handleCongressFilings(w http.ResponseWriter, r *http.Request) {

	f := postgres.CongressFilingFilter{
		Symbol: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol"))),
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			f.Limit = n
		}
	}

	filings, err := s.deps.Store.ListCongressFilings(r.Context(), f)
	if err != nil {
		s.deps.Log.Error("congress filings lookup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read congressional filings.")
		return
	}

	out := make([]congressFilingEnvelope, 0, len(filings))
	for _, filing := range filings {
		env := congressFilingEnvelope{
			DocID:               filing.DocID,
			Chamber:             filing.Chamber,
			Member:              filing.Name(),
			StateDistrict:       filing.StateDistrict,
			FilingType:          filing.FilingType,
			FilingDate:          filing.FilingDate.Format("2006-01-02"),
			Symbols:             filing.Symbols,
			UnresolvedTickers:   filing.UnresolvedTickers,
			DisclosureDelayDays: filing.DisclosureDelayDays,
			DocURL:              filing.DocURL(),
		}
		if !filing.EarliestTransactionDate.IsZero() {
			env.EarliestTransactionDate = filing.EarliestTransactionDate.Format("2006-01-02")
		}
		out = append(out, env)
	}
	writeJSON(w, http.StatusOK, map[string]any{"filings": out})
}
