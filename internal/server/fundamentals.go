package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/fundamentals"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// handleFundamentals returns valuation, reported periods and peer position for
// one company — the three things needed to judge whether it is worth owning,
// rather than merely what has been said about it.
func (s *Server) handleFundamentals(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "symbol")))
	if symbol == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A symbol is required.")
		return
	}

	snap, err := s.deps.Store.LatestSnapshot(r.Context(), symbol)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found",
			"No fundamentals have been collected for "+symbol+" yet.")
		return
	}
	if err != nil {
		s.deps.Log.Error("fundamentals lookup failed", "symbol", symbol, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read fundamentals.")
		return
	}

	quarters, err := s.deps.Store.Financials(r.Context(), symbol, "quarterly",
		clampInt(intParam(r, "quarters", 8), 1, 40), time.Time{})
	if err != nil {
		s.deps.Log.Warn("quarterly financials failed", "symbol", symbol, "err", err)
	}
	years, err := s.deps.Store.Financials(r.Context(), symbol, "annual",
		clampInt(intParam(r, "years", 5), 1, 20), time.Time{})
	if err != nil {
		s.deps.Log.Warn("annual financials failed", "symbol", symbol, "err", err)
	}

	// Peer position is best-effort: a company outside the index has no peer
	// group here, and that is a normal answer rather than an error.
	var peers *postgres.PeerComparison
	if pc, err := s.deps.Store.ComparePeers(r.Context(), symbol); err == nil {
		peers = &pc
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"symbol":   symbol,
		"snapshot": snap,
		// Stated rather than left for the reader to infer from missing
		// numbers. When a company files in a different currency from the one
		// its shares trade in, every ratio mixing the two is withheld, and
		// silently showing fewer metrics for one company than another invites
		// the conclusion that the data is simply patchy.
		"mixed_currency": snap.MixedCurrency(),
		"quarterly":      quarters,
		"annual":         years,
		"peers":          peers,
		"trends":         buildTrends(quarters),
	})
}

// buildTrends turns the reported quarters into the handful of directions a
// reader actually asks about.
//
// Quarters arrive newest first and are reversed here, because a trend reads
// forwards. Margins are computed rather than read: they are the ratio that
// says whether growth is being bought or earned.
func buildTrends(quarters []fundamentals.Period) []fundamentals.Trend {
	if len(quarters) < 2 {
		return nil
	}
	ordered := make([]fundamentals.Period, len(quarters))
	for i, p := range quarters {
		ordered[len(quarters)-1-i] = p
	}

	// Periods that do not report a metric are skipped, not fatal.
	//
	// Requiring every period to report it killed every trend on the first real
	// company tried: one quarter came back with two line items in it, and a
	// single sparse quarter is a normal fact of provider coverage rather than
	// a reason to say nothing about two years of margins. What is not
	// negotiable is that the reader be told — hence the span and gap count on
	// every trend, so a five-point line is never presented as eight quarters.
	collect := func(pick func(fundamentals.Period) *float64) (pts []float64, from, to string, gaps int) {
		for _, p := range ordered {
			v := pick(p)
			if v == nil {
				gaps++
				continue
			}
			if from == "" {
				from = p.PeriodEnd
			}
			to = p.PeriodEnd
			pts = append(pts, *v)
		}
		return pts, from, to, gaps
	}

	var out []fundamentals.Trend
	add := func(label string, higherIsBetter bool, pts []float64, from, to string, gaps int) {
		if t := fundamentals.BuildTrend(label, pts, higherIsBetter); t != nil {
			t.From, t.To, t.Gaps = from, to, gaps
			out = append(out, *t)
		}
	}
	for _, m := range []struct {
		label          string
		higherIsBetter bool
		pick           func(fundamentals.Period) *float64
	}{
		{"Revenue", true, func(p fundamentals.Period) *float64 { return p.Revenue }},
		{"Net income", true, func(p fundamentals.Period) *float64 { return p.NetIncome }},
		{"Operating margin", true, func(p fundamentals.Period) *float64 {
			return fundamentals.Margin(p.OperatingIncome, p.Revenue)
		}},
		{"EBITDA margin", true, func(p fundamentals.Period) *float64 {
			return fundamentals.Margin(p.EBITDA, p.Revenue)
		}},
		// Debt falling is the improvement here, which is why every trend
		// carries its own direction rather than assuming bigger is better.
		{"Total debt", false, func(p fundamentals.Period) *float64 { return p.TotalDebt }},
	} {
		pts, from, to, gaps := collect(m.pick)
		add(m.label, m.higherIsBetter, pts, from, to, gaps)
	}
	return out
}
