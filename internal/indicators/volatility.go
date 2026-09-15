package indicators

import "github.com/tradesys/dashboard/internal/marketdata"

// TrueRange is the greatest of: the current bar's range, the distance from the
// previous close up to this high, and the distance from the previous close
// down to this low. The first bar has no previous close, so it is undefined.
func TrueRange(candles []marketdata.Candle) Series {
	out := newSeries(len(candles))
	for i := 1; i < len(candles); i++ {
		c, prev := candles[i], candles[i-1]
		if !IsDefined(c.High) || !IsDefined(c.Low) || !IsDefined(prev.Close) {
			continue
		}
		hl := c.High - c.Low
		hc := abs(c.High - prev.Close)
		lc := abs(c.Low - prev.Close)
		out[i] = max3(hl, hc, lc)
	}
	return out
}

// ATR is Wilder's Average True Range: true range smoothed the same way RSI
// smooths its gains and losses.
func ATR(candles []marketdata.Candle, period int) Series {
	out := newSeries(len(candles))
	if period <= 0 || len(candles) <= period {
		return out
	}

	tr := TrueRange(candles)
	// The first true range sits at index 1, so hand the smoother a slice that
	// starts there and shift the result back afterwards.
	smoothed := WilderSmooth(tr[1:], period)
	for j, v := range smoothed {
		if IsDefined(v) {
			out[j+1] = v
		}
	}
	return out
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func max3(a, b, c float64) float64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}
