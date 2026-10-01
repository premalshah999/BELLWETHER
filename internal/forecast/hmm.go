package forecast

import "math"

// Regime is a two-state Gaussian hidden Markov model of the market's daily
// returns (Hamilton, 1989): a calm state with a positive drift and low
// volatility, a stressed one with a negative drift and high volatility, and
// the odds of moving between them. Fitted by Baum-Welch; read forward only,
// so the state probability on a day uses no later return.
type Regime struct {
	Mu    [2]float64    `json:"mu"`
	Sigma [2]float64    `json:"sigma"`
	A     [2][2]float64 `json:"transition"`
	Start [2]float64    `json:"start"`
}

// fitRegime estimates the model on returns xs.
func fitRegime(xs []float64) (Regime, bool) {
	n := len(xs)
	if n < 250 {
		return Regime{}, false
	}
	m, sd := meanSD(xs)
	g := Regime{
		Mu:    [2]float64{m + 0.2*sd/10, m - 0.5*sd/10},
		Sigma: [2]float64{0.7 * sd, 1.8 * sd},
		A:     [2][2]float64{{0.98, 0.02}, {0.05, 0.95}},
		Start: [2]float64{0.8, 0.2},
	}
	alpha := make([][2]float64, n)
	beta := make([][2]float64, n)
	scale := make([]float64, n)
	prev := math.Inf(-1)
	for iter := 0; iter < 200; iter++ {
		// Forward, scaled.
		for t := 0; t < n; t++ {
			var s float64
			for j := 0; j < 2; j++ {
				var p float64
				if t == 0 {
					p = g.Start[j]
				} else {
					p = alpha[t-1][0]*g.A[0][j] + alpha[t-1][1]*g.A[1][j]
				}
				alpha[t][j] = p * normPDF(xs[t], g.Mu[j], g.Sigma[j])
				s += alpha[t][j]
			}
			if s <= 0 || math.IsNaN(s) {
				return Regime{}, false
			}
			scale[t] = s
			alpha[t][0] /= s
			alpha[t][1] /= s
		}
		var ll float64
		for _, s := range scale {
			ll += math.Log(s)
		}
		// Backward, with the same scaling.
		beta[n-1] = [2]float64{1, 1}
		for t := n - 2; t >= 0; t-- {
			for i := 0; i < 2; i++ {
				var s float64
				for j := 0; j < 2; j++ {
					s += g.A[i][j] * normPDF(xs[t+1], g.Mu[j], g.Sigma[j]) * beta[t+1][j]
				}
				beta[t][i] = s / scale[t+1]
			}
		}
		// Re-estimate.
		var gammaSum, wx, wxx [2]float64
		var xiSum [2][2]float64
		var next Regime
		for t := 0; t < n; t++ {
			var gm [2]float64
			norm := alpha[t][0]*beta[t][0] + alpha[t][1]*beta[t][1]
			for j := 0; j < 2; j++ {
				gm[j] = alpha[t][j] * beta[t][j] / norm
				gammaSum[j] += gm[j]
				wx[j] += gm[j] * xs[t]
			}
			if t == 0 {
				next.Start = gm
			}
			if t < n-1 {
				var z float64
				var xi [2][2]float64
				for i := 0; i < 2; i++ {
					for j := 0; j < 2; j++ {
						xi[i][j] = alpha[t][i] * g.A[i][j] * normPDF(xs[t+1], g.Mu[j], g.Sigma[j]) * beta[t+1][j]
						z += xi[i][j]
					}
				}
				for i := 0; i < 2; i++ {
					for j := 0; j < 2; j++ {
						xiSum[i][j] += xi[i][j] / z
					}
				}
			}
		}
		for j := 0; j < 2; j++ {
			next.Mu[j] = wx[j] / gammaSum[j]
		}
		for t := 0; t < n; t++ {
			norm := alpha[t][0]*beta[t][0] + alpha[t][1]*beta[t][1]
			for j := 0; j < 2; j++ {
				d := xs[t] - next.Mu[j]
				wxx[j] += alpha[t][j] * beta[t][j] / norm * d * d
			}
		}
		for j := 0; j < 2; j++ {
			next.Sigma[j] = math.Sqrt(math.Max(wxx[j]/gammaSum[j], 1e-10))
		}
		for i := 0; i < 2; i++ {
			row := xiSum[i][0] + xiSum[i][1]
			for j := 0; j < 2; j++ {
				next.A[i][j] = xiSum[i][j] / row
			}
		}
		g = next
		if ll-prev < 1e-6 {
			break
		}
		prev = ll
	}
	// State 0 is the calm one.
	if g.Sigma[0] > g.Sigma[1] {
		g.Mu[0], g.Mu[1] = g.Mu[1], g.Mu[0]
		g.Sigma[0], g.Sigma[1] = g.Sigma[1], g.Sigma[0]
		g.A = [2][2]float64{{g.A[1][1], g.A[1][0]}, {g.A[0][1], g.A[0][0]}}
		g.Start[0], g.Start[1] = g.Start[1], g.Start[0]
	}
	return g, true
}

// Filter returns, for each day, the probability of the stressed state given
// returns up to and including that day.
func (g Regime) Filter(xs []float64) []float64 {
	out := make([]float64, len(xs))
	p := g.Start
	for t, x := range xs {
		var pred [2]float64
		if t == 0 {
			pred = p
		} else {
			pred[0] = p[0]*g.A[0][0] + p[1]*g.A[1][0]
			pred[1] = p[0]*g.A[0][1] + p[1]*g.A[1][1]
		}
		a := pred[0] * normPDF(x, g.Mu[0], g.Sigma[0])
		b := pred[1] * normPDF(x, g.Mu[1], g.Sigma[1])
		if s := a + b; s > 0 {
			p = [2]float64{a / s, b / s}
		} else {
			p = pred
		}
		out[t] = p[1]
	}
	return out
}

func normPDF(x, mu, sigma float64) float64 {
	z := (x - mu) / sigma
	return math.Exp(-0.5*z*z) / (sigma * math.Sqrt(2*math.Pi))
}
