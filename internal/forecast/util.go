package forecast

import (
	"errors"
	"math"
)

// Model is a fitted ridge regression over the ranked factors.
type Model struct {
	Coef      []float64 `json:"coef"`
	Intercept float64   `json:"intercept"`
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

// summarise is a series' mean and its t statistic.
func summarise(xs []float64) (float64, float64) {
	m, sd := meanSD(xs)
	t := 0.0
	if sd > 0 {
		t = m / (sd / math.Sqrt(float64(len(xs))))
	}
	return round4(m), round(t)
}

func round(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*100) / 100
}

func round4(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*10000) / 10000
}
