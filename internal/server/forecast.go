package server

import (
	"math"
	"net/http"
	"sort"
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

// compact drops the 20-session fan from a list row; the stock's own page
// fetches it.
func compact(p forecast.Prediction) forecast.Prediction {
	if p.Dist != nil {
		d := *p.Dist
		d.Fan = nil
		p.Dist = &d
	}
	return p
}

// horizonOf is a prediction's forecast over h sessions, or nil.
func horizonOf(p forecast.Prediction, h int) *forecast.HorizonDist {
	if p.Dist == nil {
		return nil
	}
	for i := range p.Dist.Horizons {
		if p.Dist.Horizons[i].Horizon == h {
			return &p.Dist.Horizons[i]
		}
	}
	return nil
}

// forecastSorts order the universe by what a reader may want first.
var forecastSorts = map[string]func(p forecast.Prediction) float64{
	"score": func(p forecast.Prediction) float64 { return p.Score },
	"p_beat": func(p forecast.Prediction) float64 {
		return metric(p, 5, func(h *forecast.HorizonDist) float64 { return h.PBeat })
	},
	"p_up": func(p forecast.Prediction) float64 {
		return metric(p, 5, func(h *forecast.HorizonDist) float64 { return h.PUp })
	},
	"alpha": func(p forecast.Prediction) float64 {
		return metric(p, 20, func(h *forecast.HorizonDist) float64 { return h.Alpha })
	},
	"risk": func(p forecast.Prediction) float64 {
		return -metric(p, 5, func(h *forecast.HorizonDist) float64 { return h.ES5 })
	},
	"vol_change": func(p forecast.Prediction) float64 {
		if p.Dist == nil || p.Dist.NormalVolPct == 0 {
			return math.Inf(-1)
		}
		return p.Dist.VolPct / p.Dist.NormalVolPct
	},
	"earnings": func(p forecast.Prediction) float64 {
		if p.Dist == nil || p.Dist.Earnings == nil {
			return math.Inf(-1)
		}
		return -float64(p.Dist.Earnings.Session)
	},
}

func metric(p forecast.Prediction, h int, f func(*forecast.HorizonDist) float64) float64 {
	if hd := horizonOf(p, h); hd != nil {
		return f(hd)
	}
	return math.Inf(-1)
}

// handleForecast is the engine's latest forecasts, its walk-forward record,
// and how its live forecasts have fared since.
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
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	offset = max(0, offset)
	if term := strings.ToUpper(strings.TrimSpace(q.Get("q"))); term != "" {
		var hit []forecast.Prediction
		for _, p := range preds {
			name := ""
			if s.deps.Companies != nil {
				if c, ok := s.deps.Companies.Lookup(p.Symbol); ok {
					name = strings.ToUpper(c.Name)
				}
			}
			if strings.Contains(p.Symbol, term) || strings.Contains(name, term) {
				hit = append(hit, p)
			}
		}
		preds = hit
	}
	if sector := strings.TrimSpace(q.Get("sector")); sector != "" && s.deps.Companies != nil {
		var hit []forecast.Prediction
		for _, p := range preds {
			if sec, _ := s.deps.Companies.Sector(p.Symbol); strings.EqualFold(sec, sector) {
				hit = append(hit, p)
			}
		}
		preds = hit
	}
	key := q.Get("sort")
	by, ok := forecastSorts[key]
	if !ok {
		key, by = "score", forecastSorts["score"]
	}
	desc := q.Get("dir") != "asc"
	sort.SliceStable(preds, func(i, j int) bool {
		a, b := by(preds[i]), by(preds[j])
		if desc {
			return a > b
		}
		return a < b
	})
	page := preds[min(offset, len(preds)):min(offset+limit, len(preds))]
	rows := make([]forecast.Prediction, len(page))
	for i, p := range page {
		rows[i] = compact(p)
	}
	live, err := s.deps.Store.LiveForecasts(ctx, rep.Horizon, s.deps.Now().AddDate(0, -6, 0))
	if err != nil {
		live = nil
	}
	summary := map[string]any{"runs": len(live)}
	if len(live) > 0 {
		var ic, edge, in50, in80, bu, bub, bb, bbb []float64
		for _, l := range live {
			ic, edge = append(ic, l.RankIC), append(edge, l.TopPct-l.AvgPct)
			if l.Scored > 0 {
				in50, in80 = append(in50, l.In50Pct), append(in80, l.In80Pct)
				bu, bub, bb, bbb = append(bu, l.BrierUp), append(bub, l.BrierUpBase), append(bb, l.BrierBeat), append(bbb, l.BrierBeatBase)
			}
		}
		m, t := meanT(ic)
		e, _ := meanT(edge)
		summary["rank_ic"], summary["t"], summary["top_edge_pct"] = m, t, e
		if len(in80) > 0 {
			summary["scored_runs"] = len(in80)
			summary["in50_pct"], _ = meanT(in50)
			summary["in80_pct"], _ = meanT(in80)
			summary["brier_up"], _ = meanT(bu)
			summary["brier_up_base"], _ = meanT(bub)
			summary["brier_beat"], _ = meanT(bb)
			summary["brier_beat_base"], _ = meanT(bbb)
		}
	}
	if live == nil {
		live = []postgres.LiveRun{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"report": rep, "rows": s.picks(rows), "total": len(preds), "sort": key, "offset": offset,
		"live": live, "live_summary": summary,
	})
}

// handleSymbolForecast is one stock's latest forecast, with its path fan,
// and its recent history.
func (s *Server) handleSymbolForecast(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(chi.URLParam(r, "symbol"))
	hist, err := s.deps.Store.SymbolForecasts(r.Context(), symbol, 30)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the forecast.")
		return
	}
	if len(hist) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "The model has not forecast this stock.")
		return
	}
	for i := 1; i < len(hist); i++ {
		hist[i].Prediction = compact(hist[i].Prediction)
	}
	out := map[string]any{"symbol": symbol, "latest": hist[0], "history": hist, "horizons": forecast.Horizons,
		"stale": s.deps.Now().Sub(hist[0].AsOf) > 5*24*time.Hour}
	if rep, err := s.deps.Store.LatestForecastReport(r.Context()); err == nil && rep != nil {
		out["market"], out["regime"], out["calibration"] = rep.Market, rep.Regime, rep.Dist
	}
	writeJSON(w, http.StatusOK, out)
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
