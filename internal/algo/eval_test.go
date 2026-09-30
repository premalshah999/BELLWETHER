package algo

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

var testSymbol = marketdata.MustParseSymbol("XOM")

// bars builds a candle series from closes. Highs and lows straddle the close
// so volatility indicators have something to work with.
func bars(closes ...float64) []marketdata.Candle {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]marketdata.Candle, len(closes))
	for i, c := range closes {
		out[i] = marketdata.Candle{
			Time:   base.Add(time.Duration(i) * 24 * time.Hour),
			Open:   c,
			High:   c * 1.01,
			Low:    c * 0.99,
			Close:  c,
			Volume: 1000,
		}
	}
	return out
}

// barsWithVolume builds candles with explicit volumes.
func barsWithVolume(closes, volumes []float64) []marketdata.Candle {
	out := bars(closes...)
	for i := range out {
		if i < len(volumes) {
			out[i].Volume = volumes[i]
		}
	}
	return out
}

// ramp builds n closes rising linearly from start.
func ramp(n int, start, step float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = start + float64(i)*step
	}
	return out
}

func mustAlgo(t *testing.T, jsonSrc string) *Algorithm {
	t.Helper()
	a, err := Parse([]byte(jsonSrc))
	if err != nil {
		t.Fatalf("Parse: %v\nsource: %s", err, jsonSrc)
	}
	return a
}

func evaluate(t *testing.T, jsonSrc string, candles []marketdata.Candle) Result {
	t.Helper()
	return NewEvaluator().Evaluate(mustAlgo(t, jsonSrc), testSymbol, candles)
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

func TestEveryOperatorAgainstAConstant(t *testing.T) {
	// close is 50 on the final bar. Each operator is checked on both sides of
	// its boundary and exactly at it, because the boundary is where an
	// off-by-one in a comparison hides.
	candles := bars(10, 20, 50)

	tests := []struct {
		op    string
		value float64
		want  Status
	}{
		{op: "<", value: 60, want: StatusTriggered},
		{op: "<", value: 50, want: StatusNotMet},
		{op: "<", value: 40, want: StatusNotMet},

		{op: "<=", value: 60, want: StatusTriggered},
		{op: "<=", value: 50, want: StatusTriggered},
		{op: "<=", value: 40, want: StatusNotMet},

		{op: ">", value: 40, want: StatusTriggered},
		{op: ">", value: 50, want: StatusNotMet},
		{op: ">", value: 60, want: StatusNotMet},

		{op: ">=", value: 40, want: StatusTriggered},
		{op: ">=", value: 50, want: StatusTriggered},
		{op: ">=", value: 60, want: StatusNotMet},

		{op: "==", value: 50, want: StatusTriggered},
		{op: "==", value: 50.0000000001, want: StatusTriggered}, // within tolerance
		{op: "==", value: 51, want: StatusNotMet},

		{op: "!=", value: 51, want: StatusTriggered},
		{op: "!=", value: 50, want: StatusNotMet},
	}

	for _, tc := range tests {
		name := "close " + tc.op + " " + formatNumber(tc.value)
		t.Run(name, func(t *testing.T) {
			src := `{"name":"t","symbols":["XOM"],"interval":"1d",
                     "all":[{"indicator":"close","op":"` + tc.op + `","value":` + trimFloat(tc.value) + `}],
                     "cooldown_hours":1,"notify":{}}`
			got := evaluate(t, src, candles)
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q (summary: %s)", got.Status, tc.want, got.Summary)
			}
		})
	}
}

func TestCrossoverSemantics(t *testing.T) {
	// "crossed above" and "is above" are different questions. These cases pin
	// down the difference, which is the single most common source of
	// false alerts in rule engines.
	tests := []struct {
		name       string
		closes     []float64
		op         string
		threshold  float64
		wantCross  Status
		wantIsSide Status
	}{
		{
			name:       "rises through the level on the final bar",
			closes:     []float64{10, 20, 30},
			op:         OpCrossesAbove,
			threshold:  25,
			wantCross:  StatusTriggered,
			wantIsSide: StatusTriggered,
		},
		{
			name:       "already above on both bars: above, but did not cross",
			closes:     []float64{30, 40, 50},
			op:         OpCrossesAbove,
			threshold:  25,
			wantCross:  StatusNotMet,
			wantIsSide: StatusTriggered,
		},
		{
			name:       "touched the level then rose: equality counts as below",
			closes:     []float64{10, 25, 30},
			op:         OpCrossesAbove,
			threshold:  25,
			wantCross:  StatusTriggered,
			wantIsSide: StatusTriggered,
		},
		{
			name:       "rose to exactly the level: not strictly above",
			closes:     []float64{10, 20, 25},
			op:         OpCrossesAbove,
			threshold:  25,
			wantCross:  StatusNotMet,
			wantIsSide: StatusNotMet,
		},
		{
			name:       "falls through the level",
			closes:     []float64{30, 30, 20},
			op:         OpCrossesBelow,
			threshold:  25,
			wantCross:  StatusTriggered,
			wantIsSide: StatusTriggered,
		},
		{
			name:       "already below on both bars",
			closes:     []float64{10, 15, 20},
			op:         OpCrossesBelow,
			threshold:  25,
			wantCross:  StatusNotMet,
			wantIsSide: StatusTriggered,
		},
		{
			name:       "crossed up two bars ago, not on this bar",
			closes:     []float64{10, 30, 40},
			op:         OpCrossesAbove,
			threshold:  25,
			wantCross:  StatusNotMet,
			wantIsSide: StatusTriggered,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candles := bars(tc.closes...)
			thr := trimFloat(tc.threshold)

			crossSrc := `{"name":"t","symbols":["XOM"],"interval":"1d",
                          "all":[{"indicator":"close","op":"` + tc.op + `","value":` + thr + `}],
                          "cooldown_hours":1,"notify":{}}`
			if got := evaluate(t, crossSrc, candles).Status; got != tc.wantCross {
				t.Errorf("%s: status = %q, want %q", tc.op, got, tc.wantCross)
			}

			// The corresponding "is above"/"is below" question.
			plainOp := ">"
			if tc.op == OpCrossesBelow {
				plainOp = "<"
			}
			plainSrc := `{"name":"t","symbols":["XOM"],"interval":"1d",
                          "all":[{"indicator":"close","op":"` + plainOp + `","value":` + thr + `}],
                          "cooldown_hours":1,"notify":{}}`
			if got := evaluate(t, plainSrc, candles).Status; got != tc.wantIsSide {
				t.Errorf("%s: status = %q, want %q", plainOp, got, tc.wantIsSide)
			}
		})
	}
}

func TestCrossoverNeedsAPreviousBar(t *testing.T) {
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"close","op":"crosses_above","value":5}],
             "cooldown_hours":1,"notify":{}}`
	got := evaluate(t, src, bars(10))
	if got.Status != StatusInsufficientData {
		t.Errorf("status = %q, want insufficient_data on a single bar", got.Status)
	}
	if !strings.Contains(got.Reason, "previous bar") {
		t.Errorf("reason = %q, want it to mention the previous bar", got.Reason)
	}
}

func TestCrossoverBetweenTwoIndicators(t *testing.T) {
	// A golden-cross shape: a short average rising through a long one.
	closes := append(ramp(40, 100, -1), ramp(20, 61, 4)...)
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"sma","period":5,"op":"crosses_above",
                     "compare":{"indicator":"sma","period":20}}],
             "cooldown_hours":1,"notify":{}}`

	// Walk the series forward and count how many bars report a cross. A real
	// crossover happens on exactly one bar, not on every bar afterwards.
	crosses := 0
	for i := 25; i <= len(closes); i++ {
		if evaluate(t, src, bars(closes[:i]...)).Triggered() {
			crosses++
		}
	}
	if crosses != 1 {
		t.Errorf("detected %d crossover bars, want exactly 1", crosses)
	}
}

// ---------------------------------------------------------------------------
// Missing data
// ---------------------------------------------------------------------------

func TestMissingDataIsNeverZero(t *testing.T) {
	// The heart of the matter: SMA(200) on 50 bars has no value. If it were
	// read as 0, "close > SMA(200)" would be trivially true and this
	// algorithm would fire on every symbol from day one.
	candles := bars(ramp(50, 100, 1)...)
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"close","op":">","compare":{"indicator":"sma","period":200}}],
             "cooldown_hours":1,"notify":{}}`

	got := evaluate(t, src, candles)
	if got.Status != StatusInsufficientData {
		t.Fatalf("status = %q, want insufficient_data — a missing average must not read as zero", got.Status)
	}
	if got.Triggered() {
		t.Error("an algorithm fired on data it does not have")
	}
	if !strings.Contains(got.Reason, "200") {
		t.Errorf("reason = %q, want it to say how many bars are needed", got.Reason)
	}

	// The condition snapshot must record the shortfall, not a zero.
	if len(got.Conditions) != 1 {
		t.Fatalf("got %d conditions, want 1", len(got.Conditions))
	}
	c := got.Conditions[0]
	if c.Result != "unknown" {
		t.Errorf("condition result = %q, want unknown", c.Result)
	}
	if c.Detail == "" {
		t.Error("condition has no detail explaining the unknown")
	}
}

func TestInsufficientDataForEachIndicator(t *testing.T) {
	// Every indicator must report insufficient data rather than inventing one.
	tests := []struct {
		indicator string
		params    string
		bars      int
	}{
		{indicator: "sma", params: `,"period":20`, bars: 5},
		{indicator: "ema", params: `,"period":20`, bars: 5},
		{indicator: "rsi", params: `,"period":14`, bars: 5},
		{indicator: "atr", params: `,"period":14`, bars: 5},
		{indicator: "vol_avg", params: `,"period":20`, bars: 5},
		{indicator: "vwap", params: `,"period":20`, bars: 5},
		{indicator: "macd", params: "", bars: 5},
		{indicator: "macd_signal", params: "", bars: 5},
		{indicator: "high_52w", params: `,"period":252`, bars: 5},
		{indicator: "low_52w", params: `,"period":252`, bars: 5},
	}

	for _, tc := range tests {
		t.Run(tc.indicator, func(t *testing.T) {
			src := `{"name":"t","symbols":["XOM"],"interval":"1d",
                     "all":[{"indicator":"` + tc.indicator + `"` + tc.params + `,"op":">","value":0}],
                     "cooldown_hours":1,"notify":{}}`
			got := evaluate(t, src, bars(ramp(tc.bars, 100, 1)...))
			if got.Status != StatusInsufficientData {
				t.Errorf("status = %q, want insufficient_data (summary: %s)", got.Status, got.Summary)
			}
		})
	}
}

func TestNoCandlesIsInsufficientNotFalse(t *testing.T) {
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"close","op":">","value":0}],
             "cooldown_hours":1,"notify":{}}`
	got := evaluate(t, src, nil)
	if got.Status != StatusInsufficientData {
		t.Errorf("status = %q, want insufficient_data", got.Status)
	}
	if got.Triggered() {
		t.Error("fired with no data at all")
	}
}

func TestNonFiniteInputDoesNotTrigger(t *testing.T) {
	// A provider bug that lets a NaN or an infinity through must not become a
	// comparison that accidentally succeeds.
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		candles := bars(10, 20, 30)
		candles[len(candles)-1].Close = bad

		src := `{"name":"t","symbols":["XOM"],"interval":"1d",
                 "all":[{"indicator":"close","op":">","value":0}],
                 "cooldown_hours":1,"notify":{}}`
		got := evaluate(t, src, candles)
		if got.Triggered() {
			t.Errorf("close=%v triggered an alert", bad)
		}
		if got.Status != StatusInsufficientData {
			t.Errorf("close=%v: status = %q, want insufficient_data", bad, got.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// Three-valued logic
// ---------------------------------------------------------------------------

func TestKleeneLogic(t *testing.T) {
	tests := []struct {
		name string
		in   []Tri
		and  Tri
		or   Tri
	}{
		{name: "all true", in: []Tri{TriTrue, TriTrue}, and: TriTrue, or: TriTrue},
		{name: "all false", in: []Tri{TriFalse, TriFalse}, and: TriFalse, or: TriFalse},
		{name: "true and false", in: []Tri{TriTrue, TriFalse}, and: TriFalse, or: TriTrue},
		{name: "unknown alone", in: []Tri{TriUnknown}, and: TriUnknown, or: TriUnknown},
		{
			// A definite false settles an AND regardless of unknowns.
			name: "false beside unknown", in: []Tri{TriFalse, TriUnknown},
			and: TriFalse, or: TriUnknown,
		},
		{
			// A definite true settles an OR regardless of unknowns.
			name: "true beside unknown", in: []Tri{TriTrue, TriUnknown},
			and: TriUnknown, or: TriTrue,
		},
		{name: "empty", in: nil, and: TriTrue, or: TriFalse},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := and(tc.in); got != tc.and {
				t.Errorf("and = %v, want %v", got, tc.and)
			}
			if got := or(tc.in); got != tc.or {
				t.Errorf("or = %v, want %v", got, tc.or)
			}
		})
	}
}

func TestAnyGroupFiresDespiteAnUnknownSibling(t *testing.T) {
	// One branch is definitely satisfied; the other needs history we lack.
	// The answer is still definitely yes.
	candles := bars(ramp(50, 100, 1)...)
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "any":[{"indicator":"close","op":">","value":10},
                    {"indicator":"close","op":">","compare":{"indicator":"sma","period":200}}],
             "cooldown_hours":1,"notify":{}}`

	got := evaluate(t, src, candles)
	if got.Status != StatusTriggered {
		t.Errorf("status = %q, want triggered — a satisfied branch settles an OR", got.Status)
	}
}

func TestAllGroupRejectsDespiteAnUnknownSibling(t *testing.T) {
	// One branch is definitely false, so the AND is false whatever the
	// unknown branch would have been.
	candles := bars(ramp(50, 100, 1)...)
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"close","op":"<","value":10},
                    {"indicator":"close","op":">","compare":{"indicator":"sma","period":200}}],
             "cooldown_hours":1,"notify":{}}`

	got := evaluate(t, src, candles)
	if got.Status != StatusNotMet {
		t.Errorf("status = %q, want not_met — a false branch settles an AND", got.Status)
	}
}

func TestNestedGroups(t *testing.T) {
	candles := barsWithVolume(
		[]float64{100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 50},
		[]float64{1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 5000},
	)

	tests := []struct {
		name string
		src  string
		want Status
	}{
		{
			name: "all with a nested any, one branch true",
			src: `{"name":"t","symbols":["XOM"],"interval":"1d",
                   "all":[{"indicator":"close","op":"<","value":60},
                          {"any":[{"indicator":"close","op":">","value":999},
                                  {"indicator":"volume","op":">","value":4000}]}],
                   "cooldown_hours":1,"notify":{}}`,
			want: StatusTriggered,
		},
		{
			name: "all with a nested any, no branch true",
			src: `{"name":"t","symbols":["XOM"],"interval":"1d",
                   "all":[{"indicator":"close","op":"<","value":60},
                          {"any":[{"indicator":"close","op":">","value":999},
                                  {"indicator":"volume","op":">","value":9999}]}],
                   "cooldown_hours":1,"notify":{}}`,
			want: StatusNotMet,
		},
		{
			name: "any with a nested all, all satisfied",
			src: `{"name":"t","symbols":["XOM"],"interval":"1d",
                   "any":[{"indicator":"close","op":">","value":999},
                          {"all":[{"indicator":"close","op":"<","value":60},
                                  {"indicator":"volume","op":">","value":4000}]}],
                   "cooldown_hours":1,"notify":{}}`,
			want: StatusTriggered,
		},
		{
			name: "any with a nested all, one member fails",
			src: `{"name":"t","symbols":["XOM"],"interval":"1d",
                   "any":[{"indicator":"close","op":">","value":999},
                          {"all":[{"indicator":"close","op":"<","value":60},
                                  {"indicator":"volume","op":">","value":9999}]}],
                   "cooldown_hours":1,"notify":{}}`,
			want: StatusNotMet,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluate(t, tc.src, candles)
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q (summary: %s)", got.Status, tc.want, got.Summary)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Operand scaling
// ---------------------------------------------------------------------------

func TestMultiplierOnCompare(t *testing.T) {
	// The spec's canonical example: volume above 1.5x its 20-bar average.
	volumes := make([]float64, 25)
	for i := range volumes {
		volumes[i] = 1000
	}
	closes := make([]float64, 25)
	for i := range closes {
		closes[i] = 100
	}

	tests := []struct {
		name       string
		lastVolume float64
		want       Status
	}{
		{name: "well above the threshold", lastVolume: 2000, want: StatusTriggered},
		{name: "just above the threshold", lastVolume: 1600, want: StatusTriggered},
		{name: "just below the threshold", lastVolume: 1400, want: StatusNotMet},
		{name: "at the average", lastVolume: 1000, want: StatusNotMet},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := append([]float64(nil), volumes...)
			v[len(v)-1] = tc.lastVolume
			candles := barsWithVolume(closes, v)

			src := `{"name":"t","symbols":["XOM"],"interval":"1d",
                     "all":[{"indicator":"volume","op":">",
                             "compare":{"indicator":"vol_avg","period":20,"mult":1.5}}],
                     "cooldown_hours":1,"notify":{}}`
			got := evaluate(t, src, candles)
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q (summary: %s)", got.Status, tc.want, got.Summary)
			}
		})
	}
}

func TestAbsentMultiplierDoesNotZeroTheOperand(t *testing.T) {
	// Mult defaults to 1, not to its zero value. Getting this wrong would
	// make every "compare" operand read as zero.
	candles := bars(ramp(30, 100, 1)...)
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"close","op":">","compare":{"indicator":"sma","period":20}}],
             "cooldown_hours":1,"notify":{}}`

	got := evaluate(t, src, candles)
	if len(got.Conditions) != 1 {
		t.Fatalf("got %d conditions, want 1", len(got.Conditions))
	}
	if got.Conditions[0].RightValue == 0 {
		t.Error("the compare operand evaluated to zero; an unset multiplier must mean 1")
	}
	if got.Status != StatusTriggered {
		t.Errorf("status = %q, want triggered on a rising series", got.Status)
	}
}

func TestOffset(t *testing.T) {
	candles := bars(ramp(30, 100, 1)...)
	// close must exceed SMA(20) plus 50, which it does not on a gentle ramp.
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"close","op":">","compare":{"indicator":"sma","period":20,"offset":50}}],
             "cooldown_hours":1,"notify":{}}`
	if got := evaluate(t, src, candles); got.Status != StatusNotMet {
		t.Errorf("status = %q, want not_met", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Snapshots and summaries
// ---------------------------------------------------------------------------

func TestSnapshotRecordsEveryCondition(t *testing.T) {
	candles := barsWithVolume(ramp(30, 100, 2), constantSlice(30, 1000))
	src := `{"name":"Momentum watch","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"rsi","period":14,"op":">","value":10},
                    {"indicator":"close","op":">","compare":{"indicator":"sma","period":20}},
                    {"indicator":"volume","op":">=","compare":{"indicator":"vol_avg","period":20,"mult":1.0}}],
             "cooldown_hours":24,"notify":{"telegram":true}}`

	got := evaluate(t, src, candles)
	if got.Status != StatusTriggered {
		t.Fatalf("status = %q, want triggered (summary: %s)", got.Status, got.Summary)
	}
	if len(got.Conditions) != 3 {
		t.Fatalf("recorded %d conditions, want 3 — every condition must be stored", len(got.Conditions))
	}
	for i, c := range got.Conditions {
		if c.Label == "" {
			t.Errorf("condition %d has no label", i)
		}
		if c.Text == "" {
			t.Errorf("condition %d has no human-readable text", i)
		}
		if c.Result != "true" {
			t.Errorf("condition %d result = %q, want true", i, c.Result)
		}
	}
	if !strings.Contains(got.Conditions[0].Label, "RSI(14)") {
		t.Errorf("first label = %q, want it to name RSI(14)", got.Conditions[0].Label)
	}
	if got.Summary == "" {
		t.Error("no summary was produced")
	}
	if got.BarTime.IsZero() {
		t.Error("the evaluated bar's time was not recorded")
	}
	if got.Price == 0 {
		t.Error("the price at evaluation was not recorded")
	}
}

func TestSummaryReadsLikeTheSpecExample(t *testing.T) {
	candles := barsWithVolume(ramp(30, 100, 2), constantSlice(30, 1000))
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"close","op":">","compare":{"indicator":"sma","period":20}}],
             "cooldown_hours":1,"notify":{}}`

	got := evaluate(t, src, candles)
	// Expected shape: "close=158.00 > SMA(20)=139.00"
	if !strings.Contains(got.Summary, "close=") || !strings.Contains(got.Summary, "SMA(20)=") {
		t.Errorf("summary = %q, want it to name both operands and their values", got.Summary)
	}
	if !strings.Contains(got.Summary, ">") {
		t.Errorf("summary = %q, want it to show the operator", got.Summary)
	}
}

func TestRequiredBars(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{
			name: "close only",
			src:  `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","value":1}],"notify":{}}`,
			want: 3, // 1 bar + 2 headroom
		},
		{
			name: "takes the deepest lookback",
			src: `{"name":"t","symbols":["AAPL"],"interval":"1d",
                   "all":[{"indicator":"rsi","period":14,"op":"<","value":35},
                          {"indicator":"close","op":">","compare":{"indicator":"sma","period":200}}],
                   "notify":{}}`,
			want: 202,
		},
		{
			name: "looks inside nested groups",
			src: `{"name":"t","symbols":["AAPL"],"interval":"1d",
                   "all":[{"indicator":"close","op":">","value":1},
                          {"any":[{"indicator":"sma","period":50,"op":">","value":1}]}],
                   "notify":{}}`,
			want: 52,
		},
		{
			name: "macd needs slow plus signal",
			src:  `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"macd","op":">","value":0}],"notify":{}}`,
			want: 37, // 26 + 9 + 2
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustAlgo(t, tc.src).RequiredBars(); got != tc.want {
				t.Errorf("RequiredBars = %d, want %d", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Determinism and safety
// ---------------------------------------------------------------------------

func TestEvaluationIsDeterministic(t *testing.T) {
	candles := barsWithVolume(ramp(300, 100, 0.7), constantSlice(300, 1000))
	src := `{"name":"t","symbols":["XOM"],"interval":"1d",
             "all":[{"indicator":"rsi","period":14,"op":">","value":20},
                    {"indicator":"close","op":">","compare":{"indicator":"sma","period":200}},
                    {"any":[{"indicator":"macd","op":">","compare":{"indicator":"macd_signal"}},
                            {"indicator":"volume","op":">","compare":{"indicator":"vol_avg","period":20,"mult":2}}]}],
             "cooldown_hours":1,"notify":{}}`

	a := mustAlgo(t, src)
	e := NewEvaluator()
	first := e.Evaluate(a, testSymbol, candles)
	for i := 0; i < 20; i++ {
		got := e.Evaluate(a, testSymbol, candles)
		if got.Status != first.Status || got.Summary != first.Summary {
			t.Fatalf("run %d differs: %q/%q vs %q/%q", i, got.Status, got.Summary, first.Status, first.Summary)
		}
	}
}

func TestEvaluatorNeverPanics(t *testing.T) {
	// Degenerate inputs must produce a status, not a crash. The scheduler runs
	// this against every watched symbol unattended.
	srcs := []string{
		`{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","value":0}],"notify":{}}`,
		`{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"rsi","period":2,"op":">","value":0}],"notify":{}}`,
		`{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"macd","op":">","compare":{"indicator":"macd_signal"}}],"notify":{}}`,
		`{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"session_vwap","op":">","value":0}],"notify":{}}`,
	}
	inputs := [][]marketdata.Candle{
		nil,
		{},
		bars(1),
		bars(0, 0, 0),
		bars(ramp(400, 100, 0.1)...),
	}

	e := NewEvaluator()
	for _, src := range srcs {
		a := mustAlgo(t, src)
		for _, in := range inputs {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic on %d candles: %v", len(in), r)
					}
				}()
				got := e.Evaluate(a, testSymbol, in)
				if got.Status == "" {
					t.Error("evaluation produced an empty status")
				}
			}()
		}
	}
}

func TestZeroPricesDoNotTriggerGreaterThanZero(t *testing.T) {
	// A genuine zero is data, and must compare as data — unlike a missing
	// value, which must not.
	src := `{"name":"t","symbols":["AAPL"],"interval":"1d",
             "all":[{"indicator":"close","op":">","value":0}],"notify":{}}`
	got := evaluate(t, src, bars(0, 0, 0))
	if got.Status != StatusNotMet {
		t.Errorf("status = %q, want not_met: zero is a real value that fails > 0", got.Status)
	}
}

func constantSlice(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// trimFloat renders a float as compact JSON-safe literal text.
func trimFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// Session indicators bucket by the market's own day (ET), not the evaluator's
// display timezone: these bars share a UTC date but straddle ET midnight
// (05:00Z), so the session VWAP must reset before the second bar.
func TestSessionVWAPBucketsByMarketDay(t *testing.T) {
	candles := []marketdata.Candle{
		{Time: time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC),
			Open: 100, High: 100, Low: 100, Close: 100, Volume: 100},
		{Time: time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC),
			Open: 200, High: 200, Low: 200, Close: 200, Volume: 100},
	}
	const src = `{"name":"t","symbols":["X"],"interval":"1h",
		"all":[{"indicator":"session_vwap","op":">","value":180}],"notify":{}}`
	got := NewEvaluator().Evaluate(mustAlgo(t, src), marketdata.MustParseSymbol("AAPL"), candles)
	if !got.Triggered() {
		t.Errorf("session_vwap should have reset at ET midnight, giving ~200 > 180; got status=%s reason=%q",
			got.Status, got.Reason)
	}
}
