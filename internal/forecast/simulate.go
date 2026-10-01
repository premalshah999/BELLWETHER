package forecast

import (
	"math"
	"math/rand"
	"sort"
)

// The return distribution is simulated rather than assumed, because the
// parts that matter most are the parts a normal curve gets wrong: fat
// tails, a left skew in falling markets, and the earnings day that can move
// a stock more than the month around it.
//
// Each path is filtered historical simulation (Barone-Adesi, Giannopoulos
// and Vosper, 1999): the stock's own past shocks, standardised by the
// volatility at the time, are redrawn and rescaled by today's volatility
// forecast. So a stock whose history holds more large down days than up
// days carries that skew into its forecast. Every path is
//
//	stock = alpha + beta x market + idiosyncratic shock (+ earnings jump)
//
// and the market itself moves through the two regimes, calm and stressed,
// with the odds of switching the regime model estimated. All stocks on a day
// share the same market paths, so "probability of beating the S&P 500" is
// measured against the same simulated markets.

// marketPaths are simulated daily market log returns: paths x longest.
type marketPaths [][]float64

// simMarket draws n market paths.
//
// dailyVar is the forecast variance of each day ahead; the regime model
// sets how that variance and the drift split between calm and stress, so
// the expected variance matches the forecast while a path that enters
// stress gets the stressed state's volatility and drift.
func simMarket(rng *rand.Rand, g Regime, pStress float64, dailyVar []float64, pool []float64, n int) marketPaths {
	q := [2]float64{1 - pStress, pStress}
	var scale [longest][2]float64
	var drift [2]float64
	stat := stationary(g)
	mBar := stat[0]*g.Mu[0] + stat[1]*g.Mu[1]
	for s := 0; s < 2; s++ {
		// Regime means are estimated in sample and noisy; half of the
		// difference from the long-run mean is kept.
		drift[s] = 0.5*g.Mu[s] + 0.5*mBar
	}
	for d := 0; d < longest; d++ {
		if d > 0 {
			q = [2]float64{q[0]*g.A[0][0] + q[1]*g.A[1][0], q[0]*g.A[0][1] + q[1]*g.A[1][1]}
		}
		mix := q[0]*g.Sigma[0]*g.Sigma[0] + q[1]*g.Sigma[1]*g.Sigma[1]
		for s := 0; s < 2; s++ {
			scale[d][s] = g.Sigma[s] / math.Sqrt(mix)
		}
	}
	out := make(marketPaths, n)
	for p := range out {
		path := make([]float64, longest)
		st := 0
		if rng.Float64() < pStress {
			st = 1
		}
		for d := 0; d < longest; d++ {
			if d > 0 && rng.Float64() > g.A[st][st] {
				st = 1 - st
			}
			z := pool[rng.Intn(len(pool))]
			path[d] = drift[st] + math.Sqrt(dailyVar[d])*scale[d][st]*z
		}
		out[p] = path
	}
	return out
}

func stationary(g Regime) [2]float64 {
	a, b := g.A[0][1], g.A[1][0]
	if a+b <= 0 {
		return [2]float64{1, 0}
	}
	return [2]float64{b / (a + b), a / (a + b)}
}

// stockInputs is what a stock's simulation needs on one day.
type stockInputs struct {
	alpha   [longest]float64 // expected abnormal log return on each day ahead
	beta    float64
	idioVar [longest]float64 // idiosyncratic variance on each day ahead
	pool    []float64        // standardised past shocks, mean 0, variance 1
	earnDay int              // 1..longest when a report falls in the window
	jumps   []float64        // earnings-day moves in today's units, mean 0
}

// simResult is the simulated cumulative log return of the stock and the
// market at each horizon, sorted where noted.
type simResult struct {
	stock  map[int][]float64 // horizon -> sorted cumulative log returns
	pUp    map[int]float64
	pBeat  map[int]float64
	mean   map[int]float64
	fan    [][]float64 // per day: quantiles at fanLevels, log returns
	jumpSD float64
}

var fanLevels = []float64{0.05, 0.1, 0.25, 0.5, 0.75, 0.9, 0.95}

func simStock(rng *rand.Rand, in stockInputs, mkt marketPaths, fan bool) simResult {
	n := len(mkt)
	res := simResult{stock: map[int][]float64{}, pUp: map[int]float64{}, pBeat: map[int]float64{}, mean: map[int]float64{}}
	var sd [longest]float64
	for d := range sd {
		sd[d] = math.Sqrt(in.idioVar[d])
	}
	cum := make([][]float64, 0)
	if fan {
		cum = make([][]float64, longest)
		for d := range cum {
			cum[d] = make([]float64, n)
		}
	}
	for _, h := range Horizons {
		res.stock[h] = make([]float64, n)
	}
	up := map[int]int{}
	beat := map[int]int{}
	for p := 0; p < n; p++ {
		var cs, cm float64
		for d := 0; d < longest; d++ {
			z := in.pool[rng.Intn(len(in.pool))]
			r := in.alpha[d] + in.beta*mkt[p][d] + sd[d]*z
			if in.earnDay == d+1 && len(in.jumps) > 0 {
				r += in.jumps[rng.Intn(len(in.jumps))]
			}
			cs += r
			cm += mkt[p][d]
			if fan {
				cum[d][p] = cs
			}
			switch d + 1 {
			case 5, 10, 20:
				res.stock[d+1][p] = cs
				if cs > 0 {
					up[d+1]++
				}
				if cs > cm {
					beat[d+1]++
				}
			}
		}
	}
	for _, h := range Horizons {
		xs := res.stock[h]
		var s float64
		for _, x := range xs {
			s += math.Exp(x) - 1
		}
		res.mean[h] = s / float64(n)
		sort.Float64s(xs)
		res.pUp[h] = float64(up[h]) / float64(n)
		res.pBeat[h] = float64(beat[h]) / float64(n)
	}
	if fan {
		res.fan = make([][]float64, longest)
		for d := range cum {
			sort.Float64s(cum[d])
			row := make([]float64, len(fanLevels))
			for i, q := range fanLevels {
				row[i] = quantileSorted(cum[d], q)
			}
			res.fan[d] = row
		}
	}
	return res
}

// standardise rescales xs in place to mean 0 and variance 1.
func standardise(xs []float64) []float64 {
	m, sd := meanSD(xs)
	if sd <= 0 {
		return xs
	}
	for i := range xs {
		xs[i] = (xs[i] - m) / sd
	}
	return xs
}
