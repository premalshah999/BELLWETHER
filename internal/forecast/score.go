package forecast

import (
	"math"
	"sort"
)

// A forecast of a distribution is judged with proper scoring rules, which
// no forecaster can game by reporting anything other than what it believes
// (Gneiting and Raftery, 2007):
//
//   - CRPS, the continuous ranked probability score: how far the whole
//     forecast distribution sits from the outcome, in return units. It
//     rewards being sharp and right, and punishes confident misses.
//   - Brier score for each probability (of rising, of beating the market).
//   - PIT, the probability integral transform: where each outcome fell in
//     its own forecast distribution. A calibrated forecaster's PITs are
//     uniform: 10% of outcomes land in each tenth.

// crpsSorted is the CRPS of an ensemble forecast (sorted ascending) for
// outcome y: E|X - y| - 0.5 E|X - X'|.
func crpsSorted(xs []float64, y float64) float64 {
	n := float64(len(xs))
	var a, b float64
	for i, x := range xs {
		a += math.Abs(x - y)
		b += (2*float64(i) - n + 1) * x
	}
	return a/n - b/(n*n)
}

// crpsNormal is the closed-form CRPS of a normal forecast.
func crpsNormal(mu, sigma, y float64) float64 {
	if sigma <= 0 {
		return math.Abs(y - mu)
	}
	z := (y - mu) / sigma
	return sigma * (z*(2*normCDF(z)-1) + 2*stdNormPDF(z) - 1/math.Sqrt(math.Pi))
}

func normCDF(z float64) float64    { return 0.5 * math.Erfc(-z/math.Sqrt2) }
func stdNormPDF(z float64) float64 { return math.Exp(-0.5*z*z) / math.Sqrt(2*math.Pi) }

// pitSorted is the fraction of the ensemble at or below y, with ties split.
func pitSorted(xs []float64, y float64) float64 {
	lo := sort.SearchFloat64s(xs, y)
	hi := lo
	for hi < len(xs) && xs[hi] == y {
		hi++
	}
	return (float64(lo) + float64(hi-lo)/2 + 0.5) / float64(len(xs)+1)
}

// quantileSorted is the level-q quantile of sorted xs, interpolated.
func quantileSorted(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	pos := q * float64(len(xs)-1)
	i := int(pos)
	if i >= len(xs)-1 {
		return xs[len(xs)-1]
	}
	f := pos - float64(i)
	return xs[i]*(1-f) + xs[i+1]*f
}

// Isotonic is a monotone map from a forecast probability to the frequency
// with which such forecasts came true, fitted by pool-adjacent-violators.
type Isotonic struct {
	X []float64 `json:"x"`
	Y []float64 `json:"y"`
}

// fitIsotonic fits the map on (forecast, outcome) pairs, pooled first into
// twenty equal-count bins so no block rests on a handful of extreme
// forecasts: fitted on single points, a top block of three that all came
// true maps a 67% forecast to certainty.
func fitIsotonic(p, o []float64) Isotonic {
	idx := make([]int, len(p))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return p[idx[a]] < p[idx[b]] })
	type block struct{ sumX, sumY, n float64 }
	per := max(1, len(idx)/20)
	var bins []block
	for k := 0; k < len(idx); k += per {
		var b block
		for _, i := range idx[k:min(k+per, len(idx))] {
			b.sumX += p[i]
			b.sumY += o[i]
			b.n++
		}
		bins = append(bins, b)
	}
	var st []block
	for _, b := range bins {
		st = append(st, b)
		for len(st) > 1 && st[len(st)-2].sumY/st[len(st)-2].n > st[len(st)-1].sumY/st[len(st)-1].n {
			a, c := st[len(st)-2], st[len(st)-1]
			st = st[:len(st)-2]
			st = append(st, block{a.sumX + c.sumX, a.sumY + c.sumY, a.n + c.n})
		}
	}
	var m Isotonic
	for _, b := range st {
		m.X = append(m.X, b.sumX/b.n)
		m.Y = append(m.Y, math.Min(0.99, math.Max(0.01, b.sumY/b.n)))
	}
	return m
}

// Apply maps a forecast probability through the fitted curve, linearly
// between blocks; an empty map is the identity.
func (m Isotonic) Apply(p float64) float64 {
	if len(m.X) == 0 {
		return p
	}
	if p <= m.X[0] {
		return m.Y[0]
	}
	if p >= m.X[len(m.X)-1] {
		return m.Y[len(m.Y)-1]
	}
	i := sort.SearchFloat64s(m.X, p)
	f := (p - m.X[i-1]) / (m.X[i] - m.X[i-1])
	return m.Y[i-1]*(1-f) + m.Y[i]*f
}

// PITMap recalibrates quantiles (Kuleshov, Fenner and Ermon, 2018): if past
// outcomes landed below the forecast's 10th percentile 14% of the time, the
// level that truly contains 10% is lower, and Level finds it from the
// sorted past PITs.
type PITMap struct {
	PITs []float64 `json:"-"`
	N    int       `json:"n"`
}

func newPITMap(pits []float64) PITMap {
	s := append([]float64(nil), pits...)
	sort.Float64s(s)
	return PITMap{PITs: s, N: len(s)}
}

// Level is the forecast level to read for a fraction tau of outcomes: half
// way from tau to the level that historically held tau. Half, because the
// misses of the years behind it are themselves noisy: on 2025 and 2026, the
// full correction overshot the year it was applied to.
func (m PITMap) Level(tau float64) float64 {
	if len(m.PITs) < 200 {
		return tau
	}
	full := quantileSorted(m.PITs, tau)
	return math.Min(math.Max(tau+0.5*(full-tau), 0.001), 0.999)
}

// Bin is one bucket of a reliability diagram.
type Bin struct {
	Forecast float64 `json:"forecast"`
	Observed float64 `json:"observed"`
	N        int     `json:"n"`
}

// reliability buckets forecast probabilities into tenths.
func reliability(p, o []float64) []Bin {
	var sp, so [10]float64
	var n [10]int
	for i := range p {
		b := min(9, max(0, int(p[i]*10)))
		sp[b] += p[i]
		so[b] += o[i]
		n[b]++
	}
	var out []Bin
	for b := 0; b < 10; b++ {
		if n[b] == 0 {
			continue
		}
		out = append(out, Bin{Forecast: round4(sp[b] / float64(n[b])), Observed: round4(so[b] / float64(n[b])), N: n[b]})
	}
	return out
}

// pitHistogram is the share of PITs in each tenth; uniform is calibrated.
func pitHistogram(pits []float64) []float64 {
	out := make([]float64, 10)
	for _, u := range pits {
		out[min(9, max(0, int(u*10)))]++
	}
	for i := range out {
		out[i] = round4(out[i] / math.Max(1, float64(len(pits))))
	}
	return out
}

// neweyWestT is the t statistic of a series' mean with Newey-West standard
// errors over `lags` lags, for series whose neighbours overlap (a 20-day
// forecast made every week shares most of its outcome with the next one).
func neweyWestT(xs []float64, lags int) float64 {
	n := len(xs)
	if n < 10 {
		return 0
	}
	m, _ := meanSD(xs)
	var g0 float64
	for _, x := range xs {
		g0 += (x - m) * (x - m)
	}
	g0 /= float64(n)
	v := g0
	for l := 1; l <= lags && l < n; l++ {
		var g float64
		for i := l; i < n; i++ {
			g += (xs[i] - m) * (xs[i-l] - m)
		}
		g /= float64(n)
		v += 2 * (1 - float64(l)/float64(lags+1)) * g
	}
	if v <= 0 {
		return 0
	}
	return m / math.Sqrt(v/float64(n))
}
