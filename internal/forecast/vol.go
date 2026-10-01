package forecast

import (
	"math"
	"sort"
)

// Volatility is the part of a return distribution that can be forecast
// well, so it is modelled twice and the two are blended by their measured
// out-of-sample accuracy:
//
//   - HAR (Corsi, 2009): next-period variance regressed on yesterday's,
//     last week's and last month's realised variance, the cascade of
//     traders at different horizons. Fitted pooled across the universe on
//     range-based variance, with the market's own variance and VIX added.
//   - GJR-GARCH(1,1) with Student-t errors (Glosten, Jagannathan and Runkle,
//     1993): each stock's own variance recursion, with bad news raising
//     volatility more than good news, fitted by maximum likelihood.
//
// Both model diffusive variance only. Earnings days are excluded from what
// they learn and forecast, and the announcement is added back in the
// simulation as a jump drawn from the stock's own history (Andersen,
// Bollerslev and Diebold separate jumps from continuous variation the same
// way). An earnings day otherwise teaches a model that a stock is volatile
// for the month after every report.

// cleanVar is a series' daily variance proxies with earnings reaction days
// replaced by the average of the five sessions before, so neither model
// mistakes a scheduled jump for a volatility regime. Only running sums are
// kept: the rolling means are differences of them.
type cleanVar struct {
	// prefix sums of range-based variance and squared return, accumulated
	// in float64 and stored as float32: a day's value is recovered to within
	// about 3e-8, against typical values of 1e-4, at half the memory.
	prv, pr2 []float32
}

func (s *Series) clean() *cleanVar {
	s.derive()
	T := len(s.C)
	cv := &cleanVar{prv: make([]float32, T+1), pr2: make([]float32, T+1)}
	var arv, ar2 float64
	hist := make([][2]float64, 0, 5) // the last five clean days
	for i := 0; i < T; i++ {
		var rv, r2 float64
		if i > s.First {
			// A non-earnings day beyond a 25% move is almost always a
			// corporate action (a spin-off, a special dividend) or a bad
			// print; capped, it cannot set a quarter's volatility alone.
			rv, r2 = math.Min(float64(s.rv[i]), maxDayVar), math.Min(float64(s.r[i])*float64(s.r[i]), maxDayVar)
			if s.earnDay[i] && len(hist) > 0 {
				rv, r2 = 0, 0
				for _, h := range hist {
					rv += h[0]
					r2 += h[1]
				}
				rv /= float64(len(hist))
				r2 /= float64(len(hist))
			}
			if len(hist) == 5 {
				hist = hist[1:]
			}
			hist = append(hist, [2]float64{rv, r2})
		}
		arv += rv
		ar2 += r2
		cv.prv[i+1] = float32(arv)
		cv.pr2[i+1] = float32(ar2)
	}
	return cv
}

// rv is session t's cleaned range variance.
func (c *cleanVar) rv(t int) float64 { return float64(c.prv[t+1]) - float64(c.prv[t]) }

// r2 is session t's cleaned squared return.
func (c *cleanVar) r2(t int) float64 { return float64(c.pr2[t+1]) - float64(c.pr2[t]) }

// meanRV is the mean range variance over sessions (t-n, t].
func (c *cleanVar) meanRV(t, n int) float64 {
	return (float64(c.prv[t+1]) - float64(c.prv[t+1-n])) / float64(n)
}

// meanR2 is the mean squared return over sessions (t-n, t].
func (c *cleanVar) meanR2(t, n int) float64 {
	return (float64(c.pr2[t+1]) - float64(c.pr2[t+1-n])) / float64(n)
}

// future is the mean squared return over (t, t+h], the variance the forecast
// is judged against.
func (c *cleanVar) future(t, h int) float64 {
	return (float64(c.pr2[t+h+1]) - float64(c.pr2[t+1])) / float64(h)
}

const varFloor = 1e-8

// maxDayVar is the squared log return of a 25% move.
const maxDayVar = 0.0498

func lg(x float64) float64 { return math.Log(math.Max(x, varFloor)) }

// harX is the HAR regressors at t: the stock's own variance at the daily,
// weekly, monthly and yearly scales, then the market's monthly variance and
// the variance VIX implies.
func harX(c, m *cleanVar, vix float64, t int) []float64 {
	mk := m.meanRV(t, 22)
	implied := mk
	if vix > 0 {
		implied = (vix / 100) * (vix / 100) / 252
	}
	return []float64{1, lg(c.rv(t)), lg(c.meanRV(t, 5)), lg(c.meanRV(t, 22)), lg(c.meanR2(t, 252)), lg(mk), lg(implied)}
}

// HAR is a fitted log-HAR model for one horizon.
type HAR struct {
	Horizon int       `json:"horizon"`
	Coef    []float64 `json:"coef"`
	// Smear is Duan's smearing factor: the mean of the exponentiated
	// residuals, which turns a forecast of log variance into an unbiased
	// forecast of variance.
	Smear float64 `json:"smear"`
	Rows  int     `json:"rows"`
}

// harSample is one regression row.
type harSample struct {
	x []float64
	y float64
}

// fitHAR fits log(future variance) on the regressors by least squares.
func fitHAR(h int, rows []harSample) (HAR, bool) {
	if len(rows) < 200 {
		return HAR{}, false
	}
	k := len(rows[0].x)
	a := make([][]float64, k)
	for i := range a {
		a[i] = make([]float64, k+1)
	}
	for _, r := range rows {
		for i := 0; i < k; i++ {
			for j := 0; j < k; j++ {
				a[i][j] += r.x[i] * r.x[j]
			}
			a[i][k] += r.x[i] * r.y
		}
	}
	for i := 1; i < k; i++ {
		a[i][i] += 1e-6 * float64(len(rows))
	}
	beta, err := solve(a)
	if err != nil {
		return HAR{}, false
	}
	m := HAR{Horizon: h, Coef: beta, Rows: len(rows)}
	// Smearing, with the ratios capped at their 99th percentile: a crash
	// in the training window otherwise sets the level of every forecast
	// for the three years after it.
	ratios := make([]float64, len(rows))
	for i, r := range rows {
		ratios[i] = math.Exp(r.y - m.logPredict(r.x))
	}
	sorted := append([]float64(nil), ratios...)
	sort.Float64s(sorted)
	capAt := sorted[len(sorted)*99/100]
	var s float64
	for _, v := range ratios {
		s += math.Min(v, capAt)
	}
	m.Smear = s / float64(len(rows))
	return m, true
}

func (m HAR) logPredict(x []float64) float64 {
	var s float64
	for i, c := range m.Coef {
		s += c * x[i]
	}
	return s
}

// Daily is the forecast mean daily variance over the horizon.
func (m HAR) Daily(x []float64) float64 { return math.Exp(m.logPredict(x)) * m.Smear }

// GARCH is a GJR-GARCH(1,1) with Student-t innovations, fitted with
// variance targeting: the long-run variance is the sample's, and the
// likelihood chooses how shocks decay toward it.
type GARCH struct {
	Omega, Alpha, Gamma, Beta float64
	Nu                        float64
	Mean                      float64
	ok                        bool
}

// persistence is how much of today's variance survives to tomorrow.
func (g GARCH) persistence() float64 { return g.Alpha + g.Gamma/2 + g.Beta }

func logistic(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// garchFrom maps unconstrained parameters to a stationary model.
func garchFrom(p []float64, uncond, mean float64) GARCH {
	pi := 0.999 * logistic(p[0])
	b := logistic(p[1])
	g := logistic(p[2])
	shock := pi * (1 - b)
	return GARCH{
		Beta: pi * b, Alpha: shock * g, Gamma: 2 * shock * (1 - g),
		Nu: 2.1 + math.Exp(p[3]), Omega: uncond * (1 - pi), Mean: mean, ok: true,
	}
}

// negLogLik is the Student-t negative log likelihood of returns xs.
func (g GARCH) negLogLik(xs []float64, uncond float64) float64 {
	nu := g.Nu
	c := lgamma((nu+1)/2) - lgamma(nu/2) - 0.5*math.Log(math.Pi*(nu-2))
	h := uncond
	var ll float64
	for _, x := range xs {
		e := x - g.Mean
		ll += c - 0.5*math.Log(h) - (nu+1)/2*math.Log(1+e*e/((nu-2)*h))
		neg := 0.0
		if e < 0 {
			neg = 1
		}
		h = g.Omega + (g.Alpha+g.Gamma*neg)*e*e + g.Beta*h
		if h < varFloor {
			h = varFloor
		}
	}
	return -ll
}

func lgamma(x float64) float64 { v, _ := math.Lgamma(x); return v }

// fitGARCH estimates the model on returns xs by Nelder-Mead.
func fitGARCH(xs []float64) GARCH {
	if len(xs) < 250 {
		return GARCH{}
	}
	mean, sd := meanSD(xs)
	uncond := sd * sd
	if uncond <= 0 {
		return GARCH{}
	}
	f := func(p []float64) float64 { return garchFrom(p, uncond, mean).negLogLik(xs, uncond) }
	// Start at the textbook equity model: persistence 0.97, mostly carried
	// by beta, symmetric-ish shocks, fat tails (nu about 7).
	best := nelderMead(f, []float64{3.5, 2.2, 0, 1.6}, 0.6, 300)
	return garchFrom(best, uncond, mean)
}

// filter runs the variance recursion over xs and returns the one-step-ahead
// variance after the last observation.
func (g GARCH) filter(xs []float64) float64 {
	uncond := g.Omega / math.Max(1-g.persistence(), 1e-6)
	h := uncond
	for _, x := range xs {
		e := x - g.Mean
		neg := 0.0
		if e < 0 {
			neg = 1
		}
		h = g.Omega + (g.Alpha+g.Gamma*neg)*e*e + g.Beta*h
		if h < varFloor {
			h = varFloor
		}
	}
	return h
}

// path is the forecast variance for each of the next n sessions, starting
// from the one-step variance h1: shocks decay toward the long-run level at
// the model's persistence.
func (g GARCH) path(h1 float64, n int) []float64 {
	out := make([]float64, n)
	p := g.persistence()
	h := h1
	for k := 0; k < n; k++ {
		out[k] = h
		h = g.Omega + p*h
	}
	return out
}

// nelderMead minimises f from x0 with the standard reflection, expansion,
// contraction and shrink steps.
func nelderMead(f func([]float64) float64, x0 []float64, step float64, iters int) []float64 {
	n := len(x0)
	pts := make([][]float64, n+1)
	vals := make([]float64, n+1)
	for i := range pts {
		p := append([]float64(nil), x0...)
		if i > 0 {
			p[i-1] += step
		}
		pts[i], vals[i] = p, f(p)
	}
	order := make([]int, n+1)
	for it := 0; it < iters; it++ {
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool { return vals[order[a]] < vals[order[b]] })
		bestI, worstI, secondI := order[0], order[n], order[n-1]
		if math.Abs(vals[worstI]-vals[bestI]) < 1e-7*(1+math.Abs(vals[bestI])) {
			break
		}
		cen := make([]float64, n)
		for _, i := range order[:n] {
			for j := range cen {
				cen[j] += pts[i][j] / float64(n)
			}
		}
		at := func(t float64) []float64 {
			p := make([]float64, n)
			for j := range p {
				p[j] = cen[j] + t*(pts[worstI][j]-cen[j])
			}
			return p
		}
		xr := at(-1)
		fr := f(xr)
		switch {
		case fr < vals[bestI]:
			xe := at(-2)
			if fe := f(xe); fe < fr {
				pts[worstI], vals[worstI] = xe, fe
			} else {
				pts[worstI], vals[worstI] = xr, fr
			}
		case fr < vals[secondI]:
			pts[worstI], vals[worstI] = xr, fr
		default:
			xc := at(0.5)
			if fr < vals[worstI] {
				xc = at(-0.5)
			}
			if fc := f(xc); fc < math.Min(fr, vals[worstI]) {
				pts[worstI], vals[worstI] = xc, fc
			} else {
				for _, i := range order[1:] {
					for j := range pts[i] {
						pts[i][j] = pts[bestI][j] + 0.5*(pts[i][j]-pts[bestI][j])
					}
					vals[i] = f(pts[i])
				}
			}
		}
	}
	bi := 0
	for i := range vals {
		if vals[i] < vals[bi] {
			bi = i
		}
	}
	return pts[bi]
}

// qlike is the loss that ranks variance forecasts consistently under a
// noisy proxy (Patton, 2011): zero when the forecast equals the outcome.
func qlike(forecast, realised float64) float64 {
	forecast = math.Max(forecast, varFloor)
	realised = math.Max(realised, varFloor)
	q := realised / forecast
	return q - math.Log(q) - 1
}
