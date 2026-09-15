package indicators

// SMA is the simple moving average.
//
// The first period-1 positions are undefined rather than seeded with a partial
// average: a "20-day average" computed from 3 days is not a 20-day average,
// and quietly presenting one would let an algorithm trigger on a value that
// does not mean what its name says.
func SMA(values []float64, period int) Series {
	out := newSeries(len(values))
	if period <= 0 || len(values) < period {
		return out
	}

	var sum float64
	for i, v := range values {
		if !IsDefined(v) {
			// A hole in the input poisons every window containing it. Recover
			// by restarting the accumulation after the gap rather than
			// propagating a NaN sum forever.
			return smaSlow(values, period)
		}
		sum += v
		if i >= period {
			sum -= values[i-period]
		}
		if i >= period-1 {
			out[i] = sum / float64(period)
		}
	}
	return out
}

// smaSlow recomputes each window independently, skipping windows that contain
// an undefined value. It costs O(n*period) but only runs on dirty input.
func smaSlow(values []float64, period int) Series {
	out := newSeries(len(values))
	for i := period - 1; i < len(values); i++ {
		var sum float64
		ok := true
		for j := i - period + 1; j <= i; j++ {
			if !IsDefined(values[j]) {
				ok = false
				break
			}
			sum += values[j]
		}
		if ok {
			out[i] = sum / float64(period)
		}
	}
	return out
}

// EMA is the exponentially weighted moving average with the conventional
// smoothing factor 2/(period+1).
//
// It is seeded with the simple average of the first period values, which is
// the standard convention and the one every charting package's published
// reference values assume.
func EMA(values []float64, period int) Series {
	out := newSeries(len(values))
	if period <= 0 || len(values) < period {
		return out
	}

	var seed float64
	for i := 0; i < period; i++ {
		if !IsDefined(values[i]) {
			return out
		}
		seed += values[i]
	}
	ema := seed / float64(period)
	out[period-1] = ema

	k := 2.0 / (float64(period) + 1.0)
	for i := period; i < len(values); i++ {
		if !IsDefined(values[i]) {
			// Carry the last EMA forward but mark this bar undefined: we
			// cannot know what the average would have been.
			continue
		}
		ema = (values[i]-ema)*k + ema
		out[i] = ema
	}
	return out
}

// WilderSmooth is the smoothing Wilder used for RSI and ATR: a running average
// with weight 1/period, seeded with a simple average of the first period
// values. It is an EMA with k = 1/period rather than 2/(period+1), and getting
// this wrong is the single most common source of RSI values that disagree with
// every other charting package.
func WilderSmooth(values []float64, period int) Series {
	out := newSeries(len(values))
	if period <= 0 || len(values) < period {
		return out
	}

	var seed float64
	for i := 0; i < period; i++ {
		if !IsDefined(values[i]) {
			return out
		}
		seed += values[i]
	}
	avg := seed / float64(period)
	out[period-1] = avg

	for i := period; i < len(values); i++ {
		if !IsDefined(values[i]) {
			continue
		}
		avg = (avg*float64(period-1) + values[i]) / float64(period)
		out[i] = avg
	}
	return out
}
