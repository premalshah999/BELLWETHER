// Package forecast ranks the universe by how likely each stock is to beat
// the others over the next few sessions, and says how well that has worked.
//
// The model is deliberately plain: a handful of factors with decades of
// evidence behind them (momentum, short-term reversal, low volatility, the
// 52-week high, earnings surprise drift, opportunistic insider buying),
// rank-normalised across stocks each day and combined by ridge regression.
// A linear model on well-chosen factors is the honest baseline the research
// keeps returning to (Microsoft's Qlib reports rank ICs of 0.04 to 0.05 for
// its own Alpha158 models), and every weight it learns can be read.
//
// Everything is point in time: a factor on day t uses only bars that closed
// by t and filings known by t, and the model that scores a year is trained
// only on years before it, with a gap as long as the horizon so no training
// label overlaps the test.
package forecast

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Horizon is how many sessions ahead the model predicts.
const Horizon = 5

// Factor is one input the model reads.
type Factor struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Why is the evidence the factor rests on, in a sentence.
	Why string `json:"why"`
}

// Factors in the order of each row's X.
var Factors = []Factor{
	{"mom_12_1", "12-month momentum (skip the last month)", "Stocks that rose over the past year tend to keep outperforming for months (Jegadeesh and Titman, 1993)."},
	{"ret_1m", "1-month return", "Last month's winners tend to give some back (Jegadeesh, 1990)."},
	{"ret_5d", "5-day return", "Very short-term moves partly reverse within days."},
	{"vol_20", "20-day volatility", "Calmer stocks have earned more per unit of risk (the low-volatility anomaly)."},
	{"volume_trend", "Volume, 5 days vs 60", "A surge in volume signals news the price may not have finished absorbing."},
	{"high_52w", "Distance from the 52-week high", "Stocks near their high tend to break through rather than stall (George and Hwang, 2004)."},
	{"rsi_14", "RSI(14)", "Overbought and oversold readings, the most-watched oscillator."},
	{"surprise", "Latest earnings surprise (within a quarter)", "Prices drift in the direction of an earnings surprise after it is announced (post-earnings drift)."},
	{"insider_buy", "Opportunistic insider buying, 90 days", "Non-routine insider purchases predict returns (Cohen, Malloy and Pomorski, 2012)."},
	{"insider_sell", "Discretionary insider selling, 90 days", "Heavy discretionary selling is a weaker, opposite signal."},
}

// Row is one stock on one day.
type Row struct {
	Symbol string
	Date   time.Time
	X      []float64
	// Y is the forward excess return over Horizon sessions, NaN until known.
	Y float64
}

// Earning is an announcement as the model sees it.
type Earning struct {
	At          time.Time
	SurprisePct float64
}

// Insider is one Form 4 filing's open-market total.
type Insider struct {
	Filed time.Time
	Value float64
	Buy   bool
}

// Features builds a stock's rows from its daily bars and the filings known
// about it. Only every `step`-th session is kept for training; the last
// session is always kept, so today can be scored.
func Features(symbol string, bars []marketdata.Candle, earns []Earning, ins []Insider, step int) []Row {
	n := len(bars)
	if n < 260 {
		return nil
	}
	closes := make([]float64, n)
	for i, b := range bars {
		closes[i] = b.Close
	}
	var out []Row
	for t := 252; t < n; t++ {
		if (n-1-t)%step != 0 {
			continue
		}
		at := sessionClose(bars[t].Time)
		x := make([]float64, len(Factors))
		x[0] = closes[t-21]/closes[t-252] - 1
		x[1] = closes[t]/closes[t-21] - 1
		x[2] = closes[t]/closes[t-5] - 1
		x[3] = stdevReturns(closes[t-20 : t+1])
		x[4] = meanVolume(bars[t-4:t+1]) / math.Max(meanVolume(bars[t-59:t+1]), 1) // five days against sixty
		hi := 0.0
		for _, b := range bars[t-251 : t+1] {
			hi = math.Max(hi, b.High)
		}
		if hi > 0 {
			x[5] = closes[t]/hi - 1
		}
		x[6] = rsi(closes[t-14 : t+1])
		x[7] = latestSurprise(earns, at)
		x[8], x[9] = insiderFlow(ins, at)
		y := math.NaN()
		if t+Horizon < n {
			y = closes[t+Horizon]/closes[t] - 1
		}
		ok := true
		for _, v := range x {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				ok = false
			}
		}
		if ok {
			out = append(out, Row{Symbol: symbol, Date: bars[t].Time, X: x, Y: y})
		}
	}
	return out
}

func sessionClose(bar time.Time) time.Time {
	t, _ := marketdata.ExchangeUS.SessionClose(bar)
	return t
}

func stdevReturns(c []float64) float64 {
	var rs []float64
	for i := 1; i < len(c); i++ {
		if c[i-1] > 0 {
			rs = append(rs, c[i]/c[i-1]-1)
		}
	}
	_, sd := meanSD(rs)
	return sd
}

func meanVolume(bs []marketdata.Candle) float64 {
	var s float64
	for _, b := range bs {
		s += b.Volume
	}
	return s / float64(len(bs))
}

func rsi(c []float64) float64 {
	var up, down float64
	for i := 1; i < len(c); i++ {
		d := c[i] - c[i-1]
		if d > 0 {
			up += d
		} else {
			down -= d
		}
	}
	if up+down == 0 {
		return 50
	}
	return 100 * up / (up + down)
}

// latestSurprise is the most recent surprise announced by `at` within the
// last 90 days, clipped, and zero when there is none.
func latestSurprise(earns []Earning, at time.Time) float64 {
	best := time.Time{}
	val := 0.0
	for _, e := range earns {
		if e.At.Before(at) && at.Sub(e.At) <= 90*24*time.Hour && e.At.After(best) {
			best, val = e.At, math.Max(-50, math.Min(50, e.SurprisePct))
		}
	}
	return val
}

// insiderFlow is the log of opportunistic buying and selling filed in the 90
// days to `at`.
func insiderFlow(ins []Insider, at time.Time) (buy, sell float64) {
	for _, f := range ins {
		if f.Filed.Before(at) && at.Sub(f.Filed) <= 90*24*time.Hour {
			if f.Buy {
				buy += f.Value
			} else {
				sell += f.Value
			}
		}
	}
	return math.Log1p(buy), math.Log1p(sell)
}

// normalise replaces each day's features and targets with cross-sectional
// ranks scaled to [-1, 1], so no single outlier or regime dominates and
// every factor speaks the same units.
func normalise(rows []Row) map[string][]Row {
	byDay := map[string][]Row{}
	for _, r := range rows {
		k := r.Date.Format("2006-01-02")
		byDay[k] = append(byDay[k], r)
	}
	for k, day := range byDay {
		if len(day) < 30 {
			delete(byDay, k)
			continue
		}
		out := make([]Row, len(day))
		for i, r := range day {
			out[i] = Row{Symbol: r.Symbol, Date: r.Date, X: make([]float64, len(r.X)), Y: r.Y}
		}
		for f := range Factors {
			vals := make([]float64, len(day))
			for i, r := range day {
				vals[i] = r.X[f]
			}
			ranks := rankScale(vals)
			for i := range out {
				out[i].X[f] = ranks[i]
			}
		}
		// Targets are ranked among the stocks whose forward return is known;
		// the rest stay unknown rather than mixing raw returns with ranks.
		var ys []float64
		var at []int
		for i, r := range day {
			if !math.IsNaN(r.Y) {
				ys, at = append(ys, r.Y), append(at, i)
			}
		}
		for i := range out {
			out[i].Y = math.NaN()
		}
		if len(ys) >= 30 {
			for j, r := range rankScale(ys) {
				out[at[j]].Y = r
			}
		}
		byDay[k] = out
	}
	return byDay
}

// rankScale maps values to evenly spaced ranks in [-1, 1], ties averaged.
func rankScale(v []float64) []float64 {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	out := make([]float64, len(v))
	n := float64(len(v) - 1)
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && v[idx[j+1]] == v[idx[i]] {
			j++
		}
		r := (float64(i+j)/2)/math.Max(n, 1)*2 - 1
		for k := i; k <= j; k++ {
			out[idx[k]] = r
		}
		i = j + 1
	}
	return out
}

// Model is a fitted ridge regression over the normalised factors.
type Model struct {
	Coef      []float64 `json:"coef"`
	Intercept float64   `json:"intercept"`
}

// Score is a stock's predicted rank from its normalised factors.
func (m Model) Score(x []float64) float64 {
	s := m.Intercept
	for i, c := range m.Coef {
		s += c * x[i]
	}
	return s
}

// fit solves (XᵀX + λI)β = Xᵀy on rows with a known target.
func fit(rows []Row, lambda float64) (Model, error) {
	k := len(Factors) + 1
	a := make([][]float64, k)
	for i := range a {
		a[i] = make([]float64, k+1)
	}
	n := 0
	for _, r := range rows {
		if math.IsNaN(r.Y) {
			continue
		}
		n++
		x := append([]float64{1}, r.X...)
		for i := 0; i < k; i++ {
			for j := 0; j < k; j++ {
				a[i][j] += x[i] * x[j]
			}
			a[i][k] += x[i] * r.Y
		}
	}
	if n < 1000 {
		return Model{}, fmt.Errorf("forecast: %d training rows, too few to fit", n)
	}
	for i := 1; i < k; i++ {
		a[i][i] += lambda * float64(n)
	}
	beta, err := solve(a)
	if err != nil {
		return Model{}, err
	}
	return Model{Intercept: beta[0], Coef: beta[1:]}, nil
}

// solve runs Gaussian elimination with partial pivoting on an augmented matrix.
func solve(a [][]float64) ([]float64, error) {
	n := len(a)
	for c := 0; c < n; c++ {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		if math.Abs(a[p][c]) < 1e-12 {
			return nil, errors.New("forecast: singular system")
		}
		a[c], a[p] = a[p], a[c]
		for r := c + 1; r < n; r++ {
			f := a[r][c] / a[c][c]
			for j := c; j <= n; j++ {
				a[r][j] -= f * a[c][j]
			}
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		s := a[r][n]
		for j := r + 1; j < n; j++ {
			s -= a[r][j] * x[j]
		}
		x[r] = s / a[r][r]
	}
	return x, nil
}

func meanSD(xs []float64) (float64, float64) {
	if len(xs) < 2 {
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
	return m, math.Sqrt(ss / float64(len(xs)-1))
}
