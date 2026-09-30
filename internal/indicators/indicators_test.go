package indicators

import (
	"math"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// wilderCloses is the dataset Wilder used to introduce RSI, and the one
// StockCharts publishes worked values for. Verifying against it is the whole
// reason these numbers can be trusted.
var wilderCloses = []float64{
	44.3389, 44.0902, 44.1497, 43.6124, 44.3278, 44.8264, 45.0955, 45.4245,
	45.8433, 46.0826, 45.8931, 46.0328, 45.6140, 46.2820, 46.2820, 46.0028,
	46.0328, 46.4116, 46.2220, 45.6439, 46.2120, 46.2520, 45.7128, 46.4515,
	45.7876, 45.3574, 44.0288, 44.1774, 44.2181, 44.5714, 43.4204, 42.6628,
	43.1314,
}

// tolerance for comparisons against published values, which are quoted to two
// decimal places.
const tol = 0.005

func closeTo(t *testing.T, got, want float64, label string) {
	t.Helper()
	if math.IsNaN(got) {
		t.Errorf("%s: got NaN, want %.4f", label, want)
		return
	}
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %.4f, want %.4f (diff %.4f)", label, got, want, math.Abs(got-want))
	}
}

func TestRSIAgainstPublishedValues(t *testing.T) {
	got := RSI(wilderCloses, 14)

	if len(got) != len(wilderCloses) {
		t.Fatalf("series length %d, want %d — output must align with input", len(got), len(wilderCloses))
	}

	// RSI(14) needs 14 changes, so nothing is defined before the 15th close.
	for i := 0; i < 14; i++ {
		if IsDefined(got[i]) {
			t.Errorf("index %d: got %.4f, want undefined — RSI(14) needs 15 closes", i, got[i])
		}
	}

	// Values published by StockCharts for this dataset.
	want := map[int]float64{
		14: 70.5328, 15: 66.3186, 16: 66.5498, 17: 69.4063, 18: 66.3521,
		19: 57.9750, 20: 62.9282, 21: 63.2566, 22: 56.0492, 23: 62.3742,
		24: 54.7484, 25: 50.4447, 26: 39.9895, 27: 41.4512, 28: 41.8688,
		29: 45.5027, 30: 37.3180, 31: 33.0980, 32: 37.7844,
	}
	for i, w := range want {
		closeTo(t, got[i], w, "RSI(14) at "+itoa(i))
	}
}

func TestEMAAgainstReference(t *testing.T) {
	got := EMA(wilderCloses, 10)

	for i := 0; i < 9; i++ {
		if IsDefined(got[i]) {
			t.Errorf("index %d: got %.4f, want undefined", i, got[i])
		}
	}
	// Seeded with the simple average of the first 10 closes.
	closeTo(t, got[9], 44.7791, "EMA(10) seed")
	closeTo(t, got[15], 45.6676, "EMA(10) at 15")
	closeTo(t, got[32], 44.1207, "EMA(10) final")
}

func TestSMA(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		period int
		want   map[int]float64
		undef  []int
	}{
		{
			name:   "simple sequence",
			values: []float64{1, 2, 3, 4, 5},
			period: 3,
			want:   map[int]float64{2: 2, 3: 3, 4: 4},
			undef:  []int{0, 1},
		},
		{
			name:   "period of one is the identity",
			values: []float64{7, 8, 9},
			period: 1,
			want:   map[int]float64{0: 7, 1: 8, 2: 9},
		},
		{
			name:   "period equals length",
			values: []float64{2, 4, 6},
			period: 3,
			want:   map[int]float64{2: 4},
			undef:  []int{0, 1},
		},
		{
			name:   "period longer than input yields nothing",
			values: []float64{1, 2},
			period: 5,
			undef:  []int{0, 1},
		},
		{
			name:   "negative values average correctly",
			values: []float64{-2, -4, -6, -8},
			period: 2,
			want:   map[int]float64{1: -3, 2: -5, 3: -7},
			undef:  []int{0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SMA(tc.values, tc.period)
			if len(got) != len(tc.values) {
				t.Fatalf("length %d, want %d", len(got), len(tc.values))
			}
			for i, w := range tc.want {
				closeTo(t, got[i], w, "index "+itoa(i))
			}
			for _, i := range tc.undef {
				if IsDefined(got[i]) {
					t.Errorf("index %d: got %.4f, want undefined", i, got[i])
				}
			}
		})
	}
}

func TestSMASkipsWindowsContainingHoles(t *testing.T) {
	// A hole must invalidate only the windows that contain it. Treating it as
	// zero would drag the average down and could trigger a "price below its
	// average" rule that is not true.
	values := []float64{1, 2, NaN, 4, 5, 6}
	got := SMA(values, 3)

	for _, i := range []int{0, 1, 2, 3, 4} {
		if i < 2 {
			continue
		}
		if i <= 4 && IsDefined(got[i]) && i != 4 {
			t.Errorf("index %d: got %.4f, want undefined (window contains a hole)", i, got[i])
		}
	}
	closeTo(t, got[5], 5, "index 5, the first clean window")
}

func TestSMAZeroAndNegativePeriod(t *testing.T) {
	for _, p := range []int{0, -1, -10} {
		got := SMA([]float64{1, 2, 3}, p)
		for i, v := range got {
			if IsDefined(v) {
				t.Errorf("period %d index %d: got %.4f, want undefined", p, i, v)
			}
		}
	}
}

func TestWilderSmoothDiffersFromEMA(t *testing.T) {
	// Wilder smoothing uses k = 1/period, not the EMA's 2/(period+1).
	// Conflating them is the classic reason RSI values disagree with every
	// other charting package, so pin the distinction down.
	values := []float64{10, 12, 11, 13, 14, 15, 14, 16}
	w := WilderSmooth(values, 4)
	e := EMA(values, 4)

	if !IsDefined(w[3]) || !IsDefined(e[3]) {
		t.Fatal("both should be seeded at index 3")
	}
	// Both seed with the same simple average.
	closeTo(t, w[3], e[3], "seed values agree")
	// After that they must diverge.
	if math.Abs(w[7]-e[7]) < 1e-6 {
		t.Errorf("WilderSmooth and EMA agree at index 7 (%.6f); they must not", w[7])
	}
	// Wilder is the slower of the two, so it lags a rising series more.
	if w[7] >= e[7] {
		t.Errorf("Wilder %.4f should lag EMA %.4f on a rising series", w[7], e[7])
	}
}

func TestRSIBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		closes []float64
		want   float64
	}{
		{
			name:   "unbroken gains saturate at 100",
			closes: []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
			want:   100,
		},
		{
			name:   "unbroken losses bottom out at 0",
			closes: []float64{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
			want:   0,
		},
		{
			name:   "a flat series is neutral",
			closes: []float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5},
			want:   50,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RSI(tc.closes, 14).Last()
			closeTo(t, got, tc.want, "final RSI")
		})
	}
}

func TestRSIStaysInRange(t *testing.T) {
	got := RSI(wilderCloses, 14)
	for i, v := range got {
		if !IsDefined(v) {
			continue
		}
		if v < 0 || v > 100 {
			t.Errorf("index %d: RSI %.4f is outside [0, 100]", i, v)
		}
	}
}

func TestRSIInsufficientData(t *testing.T) {
	// 14 closes give only 13 changes: not enough for RSI(14).
	got := RSI(wilderCloses[:14], 14)
	for i, v := range got {
		if IsDefined(v) {
			t.Errorf("index %d: got %.4f, want undefined", i, v)
		}
	}
}

func TestMACD(t *testing.T) {
	// A long ramp then a decline, enough bars for 26 + 9 to be defined.
	closes := make([]float64, 80)
	for i := range closes {
		if i < 50 {
			closes[i] = 100 + float64(i)
		} else {
			closes[i] = 150 - float64(i-50)*2
		}
	}

	res := MACD(closes, 12, 26, 9)
	if len(res.MACD) != len(closes) || len(res.Signal) != len(closes) || len(res.Histogram) != len(closes) {
		t.Fatal("all three lines must align with the input")
	}

	// The MACD line begins where the 26-period EMA does.
	for i := 0; i < 25; i++ {
		if IsDefined(res.MACD[i]) {
			t.Errorf("MACD index %d: got %.4f, want undefined", i, res.MACD[i])
		}
	}
	if !IsDefined(res.MACD[25]) {
		t.Error("MACD should be defined at index 25")
	}
	// The signal line needs 9 MACD values, so it starts 8 bars later.
	if IsDefined(res.Signal[32]) {
		t.Error("signal should not be defined at index 32")
	}
	if !IsDefined(res.Signal[33]) {
		t.Error("signal should be defined at index 33")
	}

	// The histogram is the difference of the other two, wherever both exist.
	for i := range res.MACD {
		if IsDefined(res.MACD[i]) && IsDefined(res.Signal[i]) {
			closeTo(t, res.Histogram[i], res.MACD[i]-res.Signal[i], "histogram at "+itoa(i))
		}
	}

	// On a sustained rise the fast EMA leads, so MACD is positive.
	if res.MACD[49] <= 0 {
		t.Errorf("MACD at the top of the ramp = %.4f, want positive", res.MACD[49])
	}
	// After a sharp reversal it must go negative.
	if res.MACD[79] >= 0 {
		t.Errorf("MACD after the decline = %.4f, want negative", res.MACD[79])
	}
}

func TestMACDRejectsBadPeriods(t *testing.T) {
	closes := make([]float64, 50)
	for i := range closes {
		closes[i] = float64(i)
	}
	tests := []struct {
		name               string
		fast, slow, signal int
	}{
		{"fast equals slow", 12, 12, 9},
		{"fast slower than slow", 26, 12, 9},
		{"zero signal", 12, 26, 0},
		{"negative fast", -1, 26, 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := MACD(closes, tc.fast, tc.slow, tc.signal)
			for i, v := range res.MACD {
				if IsDefined(v) {
					t.Errorf("index %d: got %.4f, want undefined for invalid periods", i, v)
				}
			}
		})
	}
}

func candlesFrom(highs, lows, closes []float64) []marketdata.Candle {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]marketdata.Candle, len(closes))
	for i := range closes {
		out[i] = marketdata.Candle{
			Time:   base.Add(time.Duration(i) * 24 * time.Hour),
			Open:   closes[i],
			High:   highs[i],
			Low:    lows[i],
			Close:  closes[i],
			Volume: 1000,
		}
	}
	return out
}

func TestTrueRange(t *testing.T) {
	// Bar 1: range 2. Bar 2 gaps up, so the distance from the previous close
	// to this high exceeds the bar's own range.
	candles := candlesFrom(
		[]float64{12, 20, 14},
		[]float64{10, 18, 9},
		[]float64{11, 19, 10},
	)
	got := TrueRange(candles)

	if IsDefined(got[0]) {
		t.Error("the first bar has no previous close, so true range is undefined")
	}
	// max(20-18, |20-11|, |18-11|) = 9
	closeTo(t, got[1], 9, "gap up")
	// max(14-9, |14-19|, |9-19|) = 10
	closeTo(t, got[2], 10, "gap down")
}

func TestATR(t *testing.T) {
	// A steady series where every bar has a true range of exactly 2: the ATR
	// must converge on 2 regardless of smoothing.
	n := 30
	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	for i := 0; i < n; i++ {
		closes[i] = 100
		highs[i] = 101
		lows[i] = 99
	}
	got := ATR(candlesFrom(highs, lows, closes), 14)

	for i := 0; i < 14; i++ {
		if IsDefined(got[i]) {
			t.Errorf("index %d: got %.4f, want undefined", i, got[i])
		}
	}
	closeTo(t, got[14], 2, "ATR(14) first value")
	closeTo(t, got[29], 2, "ATR(14) final value")
}

func TestATRIsNeverNegative(t *testing.T) {
	got := ATR(candlesFrom(
		[]float64{12, 20, 14, 15, 16, 17, 11, 13, 19, 21, 22, 20, 18, 17, 16, 15},
		[]float64{10, 18, 9, 11, 12, 13, 8, 9, 15, 17, 18, 16, 14, 13, 12, 11},
		[]float64{11, 19, 10, 13, 14, 15, 9, 11, 17, 19, 20, 18, 16, 15, 14, 13},
	), 5)
	for i, v := range got {
		if IsDefined(v) && v < 0 {
			t.Errorf("index %d: ATR %.4f is negative", i, v)
		}
	}
}

func TestSessionVWAP(t *testing.T) {
	// Two New York sessions of two bars each: 14:30Z is 09:30 ET, 15:30Z the
	// same day; then the same times the following day.
	day1 := time.Date(2026, 3, 2, 14, 30, 0, 0, time.UTC)
	mk := func(at time.Time, price, vol float64) marketdata.Candle {
		return marketdata.Candle{Time: at, Open: price, High: price, Low: price, Close: price, Volume: vol}
	}
	candles := []marketdata.Candle{
		mk(day1, 100, 100),
		mk(day1.Add(time.Hour), 110, 300),
		mk(day1.Add(24*time.Hour), 200, 100),
		mk(day1.Add(25*time.Hour), 220, 100),
	}

	got := SessionVWAP(candles)

	closeTo(t, got[0], 100, "session 1 bar 1")
	// (100*100 + 110*300) / 400 = 107.5
	closeTo(t, got[1], 107.5, "session 1 bar 2")
	// The new session resets: not a continuation of the previous average.
	closeTo(t, got[2], 200, "session 2 bar 1")
	closeTo(t, got[3], 210, "session 2 bar 2")
}

func TestRollingVWAP(t *testing.T) {
	mk := func(i int, price, vol float64) marketdata.Candle {
		return marketdata.Candle{
			Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 24 * time.Hour),
			Open: price, High: price, Low: price, Close: price, Volume: vol,
		}
	}
	candles := []marketdata.Candle{mk(0, 10, 100), mk(1, 20, 300), mk(2, 30, 100)}

	got := RollingVWAP(candles, 2)
	if IsDefined(got[0]) {
		t.Error("index 0 should be undefined for a 2-bar window")
	}
	// (10*100 + 20*300) / 400 = 17.5
	closeTo(t, got[1], 17.5, "window 1")
	// (20*300 + 30*100) / 400 = 22.5
	closeTo(t, got[2], 22.5, "window 2")
}

func TestVolAvg(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var candles []marketdata.Candle
	for i, v := range []float64{100, 200, 300, 400} {
		candles = append(candles, marketdata.Candle{
			Time: base.Add(time.Duration(i) * 24 * time.Hour),
			Open: 1, High: 1, Low: 1, Close: 1, Volume: v,
		})
	}
	got := VolAvg(candles, 2)
	closeTo(t, got[1], 150, "avg of 100 and 200")
	closeTo(t, got[3], 350, "avg of 300 and 400")
}

func TestRollingExtremes(t *testing.T) {
	values := []float64{5, 3, 8, 1, 9, 2}

	maxes := RollingMax(values, 3)
	if IsDefined(maxes[1]) {
		t.Error("index 1 should be undefined for a 3-bar window")
	}
	for i, want := range map[int]float64{2: 8, 3: 8, 4: 9, 5: 9} {
		closeTo(t, maxes[i], want, "RollingMax at "+itoa(i))
	}

	mins := RollingMin(values, 3)
	for i, want := range map[int]float64{2: 3, 3: 1, 4: 1, 5: 1} {
		closeTo(t, mins[i], want, "RollingMin at "+itoa(i))
	}
}

func TestRollingExtremesSkipHoles(t *testing.T) {
	// A hole must be ignored, not treated as zero — a zero would become a
	// spurious 52-week low.
	values := []float64{5, NaN, 8, 6}
	mins := RollingMin(values, 3)
	closeTo(t, mins[2], 5, "min ignores the hole")
	closeTo(t, mins[3], 6, "min of 8 and 6")
}

func TestSeriesAccessors(t *testing.T) {
	s := Series{1, 2, 3}
	closeTo(t, s.At(0), 1, "At(0)")
	closeTo(t, s.Last(), 3, "Last")
	if IsDefined(s.At(-1)) || IsDefined(s.At(99)) {
		t.Error("out-of-range access must yield an undefined value")
	}
	if IsDefined(Series{}.Last()) {
		t.Error("Last on an empty series must be undefined")
	}
}

func TestIsDefined(t *testing.T) {
	tests := map[string]struct {
		v    float64
		want bool
	}{
		"finite":            {1.5, true},
		"zero":              {0, true},
		"negative":          {-1, true},
		"NaN":               {math.NaN(), false},
		"positive infinity": {math.Inf(1), false},
		"negative infinity": {math.Inf(-1), false},
	}
	for name, tc := range tests {
		if got := IsDefined(tc.v); got != tc.want {
			t.Errorf("%s: IsDefined = %v, want %v", name, got, tc.want)
		}
	}
}

func TestEmptyInput(t *testing.T) {
	// Nothing may panic on an empty series.
	if len(SMA(nil, 5)) != 0 {
		t.Error("SMA(nil)")
	}
	if len(EMA(nil, 5)) != 0 {
		t.Error("EMA(nil)")
	}
	if len(RSI(nil, 14)) != 0 {
		t.Error("RSI(nil)")
	}
	if len(ATR(nil, 14)) != 0 {
		t.Error("ATR(nil)")
	}
	if len(VolAvg(nil, 5)) != 0 {
		t.Error("VolAvg(nil)")
	}
	if len(SessionVWAP(nil)) != 0 {
		t.Error("SessionVWAP(nil)")
	}
	res := MACD(nil, 12, 26, 9)
	if len(res.MACD) != 0 {
		t.Error("MACD(nil)")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
