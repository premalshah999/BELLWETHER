package indicators

// TradingDaysPerYear is the conventional bar count for a 52-week window on
// daily data.
const TradingDaysPerYear = 252

// RollingMax is the highest value in each trailing window of `period` bars,
// inclusive of the current bar.
func RollingMax(values []float64, period int) Series {
	return rollingExtreme(values, period, func(a, b float64) bool { return a > b })
}

// RollingMin is the lowest value in each trailing window.
func RollingMin(values []float64, period int) Series {
	return rollingExtreme(values, period, func(a, b float64) bool { return a < b })
}

// rollingExtreme walks each window directly. A monotonic deque would be
// asymptotically better, but these windows are at most a few hundred bars over
// a few hundred bars, and the straightforward version is the one that is
// obviously correct about skipping undefined values.
func rollingExtreme(values []float64, period int, better func(a, b float64) bool) Series {
	out := newSeries(len(values))
	if period <= 0 || len(values) < period {
		return out
	}
	for i := period - 1; i < len(values); i++ {
		best := NaN
		for j := i - period + 1; j <= i; j++ {
			v := values[j]
			if !IsDefined(v) {
				continue
			}
			if !IsDefined(best) || better(v, best) {
				best = v
			}
		}
		out[i] = best
	}
	return out
}
