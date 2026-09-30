package server

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/forecast"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

type forecastPick struct {
	forecast.Prediction
	Name   string `json:"name,omitempty"`
	Sector string `json:"sector,omitempty"`
}

func (s *Server) picks(ps []forecast.Prediction) []forecastPick {
	out := make([]forecastPick, len(ps))
	for i, p := range ps {
		out[i] = forecastPick{Prediction: p}
		if s.deps.Companies != nil {
			if c, ok := s.deps.Companies.Lookup(p.Symbol); ok {
				out[i].Name = c.Name
			}
			out[i].Sector, _ = s.deps.Companies.Sector(p.Symbol)
		}
	}
	return out
}

// handleForecast is the model's latest ranking, its walk-forward record, and
// how its live predictions have fared since.
func (s *Server) handleForecast(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rep, preds, err := s.deps.Store.LatestForecast(ctx)
	if err != nil {
		s.deps.Log.Error("forecast read failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the forecast.")
		return
	}
	if rep == nil {
		writeError(w, http.StatusNotFound, "not_found", "The forecast has not run yet. It runs after each close.")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	if q := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		var hit []forecast.Prediction
		for _, p := range preds {
			if strings.Contains(p.Symbol, q) {
				hit = append(hit, p)
			}
		}
		preds = hit
	}
	top := preds[:min(limit, len(preds))]
	bottom := make([]forecast.Prediction, 0, limit)
	for i := len(preds) - 1; i >= 0 && len(bottom) < limit; i-- {
		bottom = append(bottom, preds[i])
	}
	live, err := s.deps.Store.LiveForecasts(ctx, rep.Horizon, s.deps.Now().AddDate(0, -6, 0))
	if err != nil {
		live = nil
	}
	summary := map[string]any{"runs": len(live)}
	if len(live) > 0 {
		var ic, edge []float64
		for _, l := range live {
			ic, edge = append(ic, l.RankIC), append(edge, l.TopPct-l.AvgPct)
		}
		m, t := meanT(ic)
		e, _ := meanT(edge)
		summary["rank_ic"], summary["t"], summary["top_edge_pct"] = m, t, e
	}
	if live == nil {
		live = []postgres.LiveRun{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"report": rep, "top": s.picks(top), "bottom": s.picks(bottom), "total": len(preds),
		"live": live, "live_summary": summary,
	})
}

// handleSymbolForecast is one stock's latest score and its recent history.
func (s *Server) handleSymbolForecast(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(chi.URLParam(r, "symbol"))
	hist, err := s.deps.Store.SymbolForecasts(r.Context(), symbol, 30)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the forecast.")
		return
	}
	if len(hist) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "The model has not scored this stock.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"symbol": symbol, "latest": hist[0], "history": hist, "horizon": forecast.Horizon,
		"stale": s.deps.Now().Sub(hist[0].AsOf) > 5*24*time.Hour})
}

func meanT(xs []float64) (float64, float64) {
	if len(xs) < 2 {
		if len(xs) == 1 {
			return xs[0], 0
		}
		return 0, 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	m := s / float64(len(xs))
	var ss float64
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	sd := math.Sqrt(ss / float64(len(xs)-1))
	if sd == 0 {
		return m, 0
	}
	return math.Round(m*10000) / 10000, math.Round(m/(sd/math.Sqrt(float64(len(xs))))*100) / 100
}
