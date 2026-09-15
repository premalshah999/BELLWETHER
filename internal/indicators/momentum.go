package indicators

// RSI is Wilder's Relative Strength Index.
//
// Values are defined from index `period` onwards: computing RSI(14) needs 14
// price changes, which requires 15 closes.
//
// A period with no losses yields RSI 100 by definition. The reverse — no gains
// — yields 0, which falls out of the formula naturally.
func RSI(closes []float64, period int) Series {
	out := newSeries(len(closes))
	if period <= 0 || len(closes) <= period {
		return out
	}

	gains := make([]float64, len(closes)-1)
	losses := make([]float64, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		if !IsDefined(closes[i]) || !IsDefined(closes[i-1]) {
			gains[i-1], losses[i-1] = NaN, NaN
			continue
		}
		change := closes[i] - closes[i-1]
		if change > 0 {
			gains[i-1] = change
		} else {
			losses[i-1] = -change
		}
	}

	avgGain := WilderSmooth(gains, period)
	avgLoss := WilderSmooth(losses, period)

	// gains[j] describes the change into closes[j+1], so the smoothed value at
	// gains index j lands on close index j+1.
	for j := range avgGain {
		g, l := avgGain[j], avgLoss[j]
		if !IsDefined(g) || !IsDefined(l) {
			continue
		}
		out[j+1] = rsiFrom(g, l)
	}
	return out
}

func rsiFrom(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		// Unbroken gains: RS is infinite, so RSI saturates at 100.
		if avgGain == 0 {
			// Flat prices: neither side dominates.
			return 50
		}
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs))
}

// MACDResult holds the three lines of the MACD indicator.
type MACDResult struct {
	// MACD is the fast EMA minus the slow EMA.
	MACD Series
	// Signal is an EMA of the MACD line.
	Signal Series
	// Histogram is MACD minus Signal.
	Histogram Series
}

// MACD computes the Moving Average Convergence Divergence with the given
// periods, conventionally 12, 26 and 9.
//
// The signal line is an EMA of the MACD line, so it is seeded from the MACD
// line's own first defined value rather than from the raw prices.
func MACD(closes []float64, fast, slow, signal int) MACDResult {
	n := len(closes)
	res := MACDResult{MACD: newSeries(n), Signal: newSeries(n), Histogram: newSeries(n)}
	if fast <= 0 || slow <= 0 || signal <= 0 || fast >= slow || n == 0 {
		return res
	}

	fastEMA := EMA(closes, fast)
	slowEMA := EMA(closes, slow)

	// The MACD line begins where the slower EMA does.
	var defined []float64
	firstDefined := -1
	for i := 0; i < n; i++ {
		if IsDefined(fastEMA[i]) && IsDefined(slowEMA[i]) {
			res.MACD[i] = fastEMA[i] - slowEMA[i]
			if firstDefined < 0 {
				firstDefined = i
			}
			defined = append(defined, res.MACD[i])
		}
	}
	if firstDefined < 0 {
		return res
	}

	// Run the signal EMA over the compacted MACD values, then map the result
	// back onto the original index space.
	sig := EMA(defined, signal)
	for j, v := range sig {
		if !IsDefined(v) {
			continue
		}
		i := firstDefined + j
		res.Signal[i] = v
		res.Histogram[i] = res.MACD[i] - v
	}
	return res
}
