package algo

import (
	"fmt"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/indicators"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// paramKind describes which numeric parameters an indicator expects.
type paramKind int

const (
	// paramNone: a raw price or volume field.
	paramNone paramKind = iota
	// paramPeriod: a single lookback window.
	paramPeriod
	// paramMACD: fast, slow, and signal windows.
	paramMACD
)

// indicatorDef is one entry in the language's vocabulary.
type indicatorDef struct {
	// Kind says which parameters are legal.
	Kind paramKind
	// DefaultPeriod is used when the operator omits one. Zero means the
	// period is required.
	DefaultPeriod int
	// Describe renders a human label such as "RSI(14)" for alert text.
	Describe func(o Operand) string
	// MinBars is how much history the indicator needs before it produces a
	// value, used to fetch enough candles and to explain "not enough data".
	MinBars func(o Operand) int
	// Compute produces the aligned series.
	Compute func(candles []marketdata.Candle, o Operand, loc *time.Location) indicators.Series
}

// registry is the complete vocabulary of the rule language. Adding an
// indicator means adding one entry here; nothing else in the evaluator needs
// to change.
var registry = map[string]indicatorDef{
	"close":  priceField(func(c marketdata.Candle) float64 { return c.Close }, "close"),
	"open":   priceField(func(c marketdata.Candle) float64 { return c.Open }, "open"),
	"high":   priceField(func(c marketdata.Candle) float64 { return c.High }, "high"),
	"low":    priceField(func(c marketdata.Candle) float64 { return c.Low }, "low"),
	"volume": priceField(func(c marketdata.Candle) float64 { return c.Volume }, "volume"),

	"sma": {
		Kind:     paramPeriod,
		Describe: periodLabel("SMA"),
		MinBars:  func(o Operand) int { return o.Period },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.SMA(indicators.Closes(candles), o.Period)
		},
	},
	"ema": {
		Kind:     paramPeriod,
		Describe: periodLabel("EMA"),
		MinBars:  func(o Operand) int { return o.Period },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.EMA(indicators.Closes(candles), o.Period)
		},
	},
	"rsi": {
		Kind:          paramPeriod,
		DefaultPeriod: 14,
		Describe:      periodLabel("RSI"),
		// RSI(n) needs n changes, so n+1 closes.
		MinBars: func(o Operand) int { return o.Period + 1 },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.RSI(indicators.Closes(candles), o.Period)
		},
	},
	"atr": {
		Kind:          paramPeriod,
		DefaultPeriod: 14,
		Describe:      periodLabel("ATR"),
		MinBars:       func(o Operand) int { return o.Period + 1 },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.ATR(candles, o.Period)
		},
	},
	"vol_avg": {
		Kind:          paramPeriod,
		DefaultPeriod: 20,
		Describe:      periodLabel("VolAvg"),
		MinBars:       func(o Operand) int { return o.Period },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.VolAvg(candles, o.Period)
		},
	},
	"vwap": {
		Kind:          paramPeriod,
		DefaultPeriod: 20,
		Describe:      periodLabel("VWAP"),
		MinBars:       func(o Operand) int { return o.Period },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.RollingVWAP(candles, o.Period)
		},
	},
	"session_vwap": {
		Kind:     paramNone,
		Describe: func(o Operand) string { return withShift("SessionVWAP", o) },
		MinBars:  func(Operand) int { return 1 },
		Compute: func(candles []marketdata.Candle, _ Operand, loc *time.Location) indicators.Series {
			return indicators.SessionVWAP(candles, loc)
		},
	},
	"macd": {
		Kind:     paramMACD,
		Describe: macdLabel("MACD"),
		MinBars:  func(o Operand) int { return o.Slow + o.Signal },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.MACD(indicators.Closes(candles), o.Fast, o.Slow, o.Signal).MACD
		},
	},
	"macd_signal": {
		Kind:     paramMACD,
		Describe: macdLabel("MACDsig"),
		MinBars:  func(o Operand) int { return o.Slow + o.Signal },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.MACD(indicators.Closes(candles), o.Fast, o.Slow, o.Signal).Signal
		},
	},
	"macd_hist": {
		Kind:     paramMACD,
		Describe: macdLabel("MACDhist"),
		MinBars:  func(o Operand) int { return o.Slow + o.Signal },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.MACD(indicators.Closes(candles), o.Fast, o.Slow, o.Signal).Histogram
		},
	},
	"high_52w": {
		Kind:          paramPeriod,
		DefaultPeriod: indicators.TradingDaysPerYear,
		Describe:      func(o Operand) string { return withShift(fmt.Sprintf("%dbarHigh", o.Period), o) },
		MinBars:       func(o Operand) int { return o.Period },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.RollingMax(indicators.Highs(candles), o.Period)
		},
	},
	"low_52w": {
		Kind:          paramPeriod,
		DefaultPeriod: indicators.TradingDaysPerYear,
		Describe:      func(o Operand) string { return withShift(fmt.Sprintf("%dbarLow", o.Period), o) },
		MinBars:       func(o Operand) int { return o.Period },
		Compute: func(candles []marketdata.Candle, o Operand, _ *time.Location) indicators.Series {
			return indicators.RollingMin(indicators.Lows(candles), o.Period)
		},
	},
}

func priceField(pick func(marketdata.Candle) float64, label string) indicatorDef {
	return indicatorDef{
		Kind:     paramNone,
		Describe: func(o Operand) string { return withShift(label, o) },
		MinBars:  func(Operand) int { return 1 },
		Compute: func(candles []marketdata.Candle, _ Operand, _ *time.Location) indicators.Series {
			out := make(indicators.Series, len(candles))
			for i, c := range candles {
				out[i] = pick(c)
			}
			return out
		},
	}
}

func periodLabel(name string) func(Operand) string {
	return func(o Operand) string { return withShift(fmt.Sprintf("%s(%d)", name, o.Period), o) }
}

func macdLabel(name string) func(Operand) string {
	return func(o Operand) string {
		return withShift(fmt.Sprintf("%s(%d,%d,%d)", name, o.Fast, o.Slow, o.Signal), o)
	}
}

// withShift annotates a label with its lookback, so "prior 52-week high" is
// visibly different from "52-week high" in an alert message.
func withShift(label string, o Operand) string {
	if o.Shift == 0 {
		return label
	}
	return fmt.Sprintf("%s[-%d]", label, o.Shift)
}

// IndicatorNames lists the vocabulary, sorted, for the builder UI's dropdown
// and for error messages that suggest what the operator meant.
func IndicatorNames() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// IndicatorInfo describes one indicator for the builder UI.
type IndicatorInfo struct {
	Name string `json:"name"`
	// Params is "none", "period", or "macd".
	Params        string `json:"params"`
	DefaultPeriod int    `json:"default_period,omitempty"`
}

// Vocabulary describes every indicator, so the frontend builds its dropdowns
// from the backend's actual capabilities rather than a hardcoded copy that can
// drift out of sync.
func Vocabulary() []IndicatorInfo {
	kinds := map[paramKind]string{paramNone: "none", paramPeriod: "period", paramMACD: "macd"}
	out := make([]IndicatorInfo, 0, len(registry))
	for _, name := range IndicatorNames() {
		def := registry[name]
		out = append(out, IndicatorInfo{
			Name:          name,
			Params:        kinds[def.Kind],
			DefaultPeriod: def.DefaultPeriod,
		})
	}
	return out
}

// Operators lists the comparison operators the language accepts.
func Operators() []string {
	return []string{OpLT, OpLTE, OpGT, OpGTE, OpEQ, OpNEQ, OpCrossesAbove, OpCrossesBelow}
}
