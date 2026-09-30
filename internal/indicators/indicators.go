// Package indicators computes technical indicators over candle series.
//
// Every function here returns a Series aligned index-for-index with its input:
// output[i] describes the bar at input[i]. Positions where the indicator is
// not yet defined hold NaN, never zero.
//
// That alignment rule is the whole point. The algorithm evaluator compares
// operands bar-for-bar and needs to distinguish "no value yet" from "the value
// is zero" — a 200-day moving average that silently reads 0 for its first 199
// bars would fire a "close crossed above SMA200" alert on day one of every
// symbol. Missing data must stay missing all the way to the evaluator.
package indicators

import (
	"math"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Series is an indicator's output, aligned to the candles it was computed
// from. Undefined positions are NaN.
type Series []float64

// NaN is the value used for undefined positions.
var NaN = math.NaN()

// At returns the value at index i, or NaN when i is out of range. Callers must
// still test the result with IsDefined.
func (s Series) At(i int) float64 {
	if i < 0 || i >= len(s) {
		return NaN
	}
	return s[i]
}

// Last returns the most recent value, or NaN for an empty series.
func (s Series) Last() float64 { return s.At(len(s) - 1) }

// IsDefined reports whether a value is usable — present, finite, and not NaN.
func IsDefined(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// newSeries returns a series of length n filled with NaN, so any position a
// computation does not write stays explicitly undefined.
func newSeries(n int) Series {
	s := make(Series, n)
	for i := range s {
		s[i] = NaN
	}
	return s
}

// Closes extracts closing prices.
func Closes(candles []marketdata.Candle) []float64 {
	return field(candles, func(c marketdata.Candle) float64 { return c.Close })
}

// Highs extracts high prices.
func Highs(candles []marketdata.Candle) []float64 {
	return field(candles, func(c marketdata.Candle) float64 { return c.High })
}

// Lows extracts low prices.
func Lows(candles []marketdata.Candle) []float64 {
	return field(candles, func(c marketdata.Candle) float64 { return c.Low })
}

// Volumes extracts volumes.
func Volumes(candles []marketdata.Candle) []float64 {
	return field(candles, func(c marketdata.Candle) float64 { return c.Volume })
}

func field(candles []marketdata.Candle, pick func(marketdata.Candle) float64) []float64 {
	out := make([]float64, len(candles))
	for i, c := range candles {
		out[i] = pick(c)
	}
	return out
}
