package ai

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/forecast"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// ForecastSource supplies the quantitative engine's latest forecast for a
// stock and the engine's measured record.
type ForecastSource interface {
	LatestPrediction(ctx context.Context, symbol string) (*forecast.Prediction, time.Time, error)
	LatestForecastReport(ctx context.Context) (*forecast.Report, error)
}

// WithForecasts gives outlooks the engine's distribution as their prior.
func WithForecasts(src ForecastSource) ServiceOption { return func(s *Service) { s.forecasts = src } }

// Prior is a return distribution over the outlook's horizon and the three
// scenarios read off it.
//
// The scenarios are fixed per stock rather than chosen per outlook: bear is a
// fall of more than one normal-sized move over the horizon (the stock's own
// past-year volatility), bull a rise of more than one, base the rest. So
// their probabilities move with what the distribution says - wider when an
// earnings report falls inside the window or the market is stressed, skewed
// when the model leans - instead of settling on whatever split a writer
// finds plausible.
type Prior struct {
	Source string `json:"source"` // "engine" or "history"
	AsOf   string `json:"as_of,omitempty"`
	// Quantiles are simple returns at forecast.QuantileLevels.
	Quantiles []float64 `json:"quantiles"`
	PUp       float64   `json:"p_up"`
	PBeat     *float64  `json:"p_beat,omitempty"`
	// ThresholdPct is the scenario boundary: one normal-sized move.
	ThresholdPct float64 `json:"threshold_pct"`
	Bull         float64 `json:"bull"`
	Base         float64 `json:"base"`
	Bear         float64 `json:"bear"`
	BullMovePct  float64 `json:"bull_move_pct"`
	BaseMovePct  float64 `json:"base_move_pct"`
	BearMovePct  float64 `json:"bear_move_pct"`
	// EarningsSession is the report's session inside the window, if any.
	EarningsSession int     `json:"earnings_session,omitempty"`
	EarningsMovePct float64 `json:"earnings_move_pct,omitempty"`
	VolPct          float64 `json:"vol_pct,omitempty"`
	NormalVolPct    float64 `json:"normal_vol_pct,omitempty"`
}

// Adjustment is what the writer changed about the prior, and why.
type Adjustment struct {
	// ShiftSigma moves the whole distribution by that many of its own
	// standard deviations; VolScale widens or narrows it about its median.
	ShiftSigma float64  `json:"shift_sigma"`
	VolScale   float64  `json:"vol_scale"`
	Evidence   []string `json:"evidence,omitempty"`
	Reasoning  string   `json:"reasoning,omitempty"`
}

// maxShift and the vol scale bounds keep a writer from replacing the model:
// it may lean on news the engine cannot read, not overrule it.
const (
	maxShift    = 0.5
	minVolScale = 0.7
	maxVolScale = 1.6
)

// priorFrom reads the engine's distribution at the outlook's horizon.
func priorFrom(d *forecast.Distribution, asOf time.Time, horizon int) *Prior {
	if d == nil {
		return nil
	}
	for _, h := range d.Horizons {
		if h.Horizon != horizon || len(h.Quantiles) != len(forecast.QuantileLevels) {
			continue
		}
		pb := h.PBeat
		p := &Prior{Source: "engine", AsOf: asOf.Format("2006-01-02"), Quantiles: append([]float64(nil), h.Quantiles...),
			PUp: h.PUp, PBeat: &pb, VolPct: d.VolPct, NormalVolPct: d.NormalVolPct}
		if e := d.Earnings; e != nil && e.Session <= horizon {
			p.EarningsSession, p.EarningsMovePct = e.Session, e.TypicalMovePct
		}
		normal := d.NormalVolPct / 100
		if normal <= 0 {
			normal = d.VolPct / 100
		}
		p.ThresholdPct = normal * math.Sqrt(float64(horizon)/252) * 100
		p.scenarios()
		return p
	}
	return nil
}

// priorFromHistory is the fallback for a stock the engine does not cover:
// the stock's own past returns over the horizon, two years of them, rescaled
// to its current volatility. Plainer than the engine, but measured rather
// than imagined.
func priorFromHistory(candles []marketdata.Candle, horizon int) *Prior {
	n := len(candles)
	if n < 260 {
		return nil
	}
	closes := make([]float64, n)
	for i, c := range candles {
		closes[i] = c.Close
	}
	from := max(1, n-504)
	var daily []float64
	for i := from; i < n; i++ {
		if closes[i-1] > 0 && closes[i] > 0 {
			daily = append(daily, math.Log(closes[i]/closes[i-1]))
		}
	}
	long := stdev(daily)
	recent := stdev(daily[max(0, len(daily)-20):])
	scale := 1.0
	if long > 0 && recent > 0 {
		scale = math.Max(0.6, math.Min(2.5, recent/long))
	}
	var rets []float64
	for i := from + horizon; i < n; i++ {
		if closes[i-horizon] > 0 {
			rets = append(rets, math.Log(closes[i]/closes[i-horizon])*scale)
		}
	}
	if len(rets) < 100 {
		return nil
	}
	sort.Float64s(rets)
	p := &Prior{Source: "history"}
	up := 0
	for _, r := range rets {
		if r > 0 {
			up++
		}
	}
	p.PUp = float64(up) / float64(len(rets))
	for _, q := range forecast.QuantileLevels {
		p.Quantiles = append(p.Quantiles, math.Exp(quantile(rets, q))-1)
	}
	year := daily[max(0, len(daily)-252):]
	p.NormalVolPct = stdev(year) * math.Sqrt(252) * 100
	p.VolPct = recent * math.Sqrt(252) * 100
	p.ThresholdPct = stdev(year) * math.Sqrt(float64(horizon)) * 100
	p.scenarios()
	return p
}

func stdev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var m float64
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	var ss float64
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	i := int(pos)
	if i >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	f := pos - float64(i)
	return sorted[i]*(1-f) + sorted[i+1]*f
}

// grid is the distribution's quantile function at 199 levels, in log
// returns, with the tails beyond 5% and 95% extended at the slope of the
// outermost band.
func (p *Prior) grid() []float64 {
	levels := forecast.QuantileLevels
	lq := make([]float64, len(p.Quantiles))
	for i, q := range p.Quantiles {
		lq[i] = math.Log1p(math.Max(q, -0.99))
	}
	out := make([]float64, 0, 199)
	for k := 1; k <= 199; k++ {
		u := float64(k) / 200
		var v float64
		switch {
		case u <= levels[0]:
			slope := (lq[1] - lq[0]) / (levels[1] - levels[0])
			v = lq[0] - slope*(levels[0]-u)*1.5
		case u >= levels[len(levels)-1]:
			n := len(levels) - 1
			slope := (lq[n] - lq[n-1]) / (levels[n] - levels[n-1])
			v = lq[n] + slope*(u-levels[n])*1.5
		default:
			j := sort.SearchFloat64s(levels, u)
			f := (u - levels[j-1]) / (levels[j] - levels[j-1])
			v = lq[j-1]*(1-f) + lq[j]*f
		}
		out = append(out, v)
	}
	return out
}

// scenarios fills the three probabilities and the average move in each.
func (p *Prior) scenarios() {
	t := math.Log1p(p.ThresholdPct / 100)
	var n [3]float64
	var sum [3]float64
	for _, v := range p.grid() {
		k := 1
		switch {
		case v > t:
			k = 0
		case v < -t:
			k = 2
		}
		n[k]++
		sum[k] += math.Expm1(v) * 100
	}
	total := n[0] + n[1] + n[2]
	p.Bull, p.Base, p.Bear = n[0]/total, n[1]/total, n[2]/total
	mv := func(k int, fallback float64) float64 {
		if n[k] == 0 {
			return fallback
		}
		return round2(sum[k] / n[k])
	}
	p.BullMovePct = mv(0, p.ThresholdPct*1.5)
	p.BaseMovePct = mv(1, 0)
	p.BearMovePct = mv(2, -p.ThresholdPct*1.5)
	p.Bull, p.Base, p.Bear = round4(p.Bull), round4(p.Base), round4(p.Bear)
}

// adjusted applies a writer's adjustment, clamped to its bounds, to a copy
// of the prior.
func (p *Prior) adjusted(a *Adjustment) *Prior {
	a.ShiftSigma = math.Max(-maxShift, math.Min(maxShift, a.ShiftSigma))
	if a.VolScale == 0 {
		a.VolScale = 1
	}
	a.VolScale = math.Max(minVolScale, math.Min(maxVolScale, a.VolScale))
	g := p.grid()
	med := g[99]
	sd := stdevOfGrid(g)
	out := *p
	out.Quantiles = nil
	for i, l := range forecast.QuantileLevels {
		_ = i
		v := g[int(math.Round(l*200))-1]
		v = med + a.VolScale*(v-med) + a.ShiftSigma*sd
		out.Quantiles = append(out.Quantiles, math.Expm1(v))
	}
	// The probability of rising, read off the adjusted distribution.
	up := 0
	for _, v := range g {
		if med+a.VolScale*(v-med)+a.ShiftSigma*sd > 0 {
			up++
		}
	}
	out.PUp = round4(float64(up) / float64(len(g)))
	out.PBeat = nil
	out.scenarios()
	return &out
}

func stdevOfGrid(g []float64) float64 { return stdev(g) }

func round2(x float64) float64 { return math.Round(x*100) / 100 }
func round4(x float64) float64 { return math.Round(x*10000) / 10000 }
