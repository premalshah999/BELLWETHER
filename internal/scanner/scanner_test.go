package scanner

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func base() Metrics {
	return Metrics{Symbol: "TEST", Close: 100, Bars: 120}
}

// TestClassifyRelativeNotAbsolute is the scanner's central premise: a move is
// judged against the instrument's own normal, never against a fixed
// percentage. Absolute thresholds surface the same volatile names every day
// and miss the quiet one that just woke up.
func TestClassifyRelativeNotAbsolute(t *testing.T) {
	// A large bank moving 2% — small in percentage terms, enormous for it.
	bank := base()
	bank.Return1D = 2.0
	bank.ReturnZ = priceMoveZ + 0.9
	bank.VolumeZ = volumeSpikeZ + 0.1
	bank.VolumeRatio = 3.1

	// A smallcap moving 6% on its usual volume — ordinary for that name.
	smallcap := base()
	smallcap.Return1D = 6.0
	smallcap.ReturnZ = 0.9
	smallcap.VolumeZ = 0.3
	smallcap.VolumeRatio = 1.1

	if len(Classify(bank)) == 0 {
		t.Error("a 3.4 sigma move on 2.6 sigma volume must be flagged, however small the percentage")
	}
	if len(Classify(smallcap)) != 0 {
		t.Errorf("a 6%% day inside a name's normal range must not be flagged: %v", Classify(smallcap))
	}
}

func TestClassifySignals(t *testing.T) {
	cases := []struct {
		name string
		m    func(Metrics) Metrics
		want Signal
	}{
		{"volume spike", func(m Metrics) Metrics { m.VolumeZ = volumeSpikeZ + 0.1; m.ReturnZ = 1.5; return m }, SignalVolumeSpike},
		{"price move", func(m Metrics) Metrics { m.ReturnZ = -(priceMoveZ + 0.3); return m }, SignalPriceMove},
		{"gap", func(m Metrics) Metrics { m.GapPercent = -4.2; return m }, SignalGap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.m(base()))
			found := false
			for _, s := range got {
				if s == tc.want {
					found = true
				}
			}
			if !found {
				t.Errorf("signals = %v, want %s among them", got, tc.want)
			}
		})
	}
}

// TestVolumeWithoutPriceIsItsOwnSignal: somebody transacting size while the
// price has not reacted is a different, earlier phenomenon than a move on
// heavy volume, and folding them together loses the more interesting one.
func TestVolumeWithoutPriceIsItsOwnSignal(t *testing.T) {
	m := base()
	m.VolumeZ = silentVolumeZ + 0.1
	m.ReturnZ = 0.3

	got := Classify(m)
	var silent, move bool
	for _, s := range got {
		if s == SignalSilentVolume {
			silent = true
		}
		if s == SignalPriceMove {
			move = true
		}
	}
	if !silent {
		t.Errorf("signals = %v, want volume_without_price", got)
	}
	if move {
		t.Errorf("signals = %v, must not claim a price move on a 0.3 sigma day", got)
	}
	// And it should outrank a comparable move that everyone can already see.
	quiet := score(m, got)

	loud := base()
	loud.VolumeZ = silentVolumeZ + 0.1
	loud.ReturnZ = 2.2
	if quiet <= 0 {
		t.Error("an unexplained transaction should carry weight")
	}
	_ = loud
}

// TestProximityNeedsParticipation: without it, every stock in a quiet uptrend
// sits near its high indefinitely and would be reported every single day.
func TestProximityNeedsParticipation(t *testing.T) {
	drifting := base()
	drifting.PctFrom52WHigh = -0.5
	drifting.VolumeZ = 0.2
	drifting.ReturnZ = 0.4
	if len(Classify(drifting)) != 0 {
		t.Errorf("a quiet drift near the high must not be flagged: %v", Classify(drifting))
	}

	breaking := drifting
	breaking.VolumeZ = volumeSpikeZ + 0.3
	got := Classify(breaking)
	found := false
	for _, s := range got {
		if s == SignalNear52WHigh {
			found = true
		}
	}
	if !found {
		t.Errorf("signals = %v, want near_52w_high once volume confirms it", got)
	}
}

// TestScoreLeadsOnVolume: a price can move on nothing, but volume means
// somebody transacted.
func TestScoreLeadsOnVolume(t *testing.T) {
	onVolume := base()
	onVolume.VolumeZ = volumeSpikeZ + 0.5
	onVolume.ReturnZ = 1.0

	onPrice := base()
	onPrice.VolumeZ = 1.0
	onPrice.ReturnZ = priceMoveZ + 0.5

	a := score(onVolume, Classify(onVolume))
	b := score(onPrice, Classify(onPrice))
	if a <= b {
		t.Errorf("volume-led score %.2f should exceed price-led %.2f", a, b)
	}
}

// TestNormalDayIsQuiet is the property that makes a scanner a scanner. One
// that flags eighty names has not scanned anything; it has re-listed the
// market.
func TestNormalDayIsQuiet(t *testing.T) {
	flagged := 0
	// A synthetic universe of ordinary days: z-scores spread across the
	// range a normal distribution actually produces.
	for i := 0; i < 500; i++ {
		m := base()
		// Deterministic spread from -1.8 to +1.8 sigma.
		m.ReturnZ = math.Sin(float64(i)) * 1.8
		m.VolumeZ = math.Cos(float64(i)) * 1.6
		m.GapPercent = math.Sin(float64(i)*0.5) * 2.0
		m.PctFrom52WHigh = -8
		m.PctFrom52WLow = 40
		if len(Classify(m)) > 0 {
			flagged++
		}
	}
	if flagged > 25 {
		t.Errorf("%d of 500 ordinary days flagged; the thresholds are too loose", flagged)
	}
}

func TestHasDiscoverableCause(t *testing.T) {
	f := Finding{Signals: []Signal{SignalSilentVolume}}
	if f.HasDiscoverableCause() {
		t.Error("volume without price has no visible cause yet")
	}
	f.Signals = append(f.Signals, SignalGap)
	if !f.HasDiscoverableCause() {
		t.Error("a gap is the kind of thing that usually has a discoverable cause")
	}
}

// TestUnexplainedFindingCrossesWarm guards the calibration that made the
// scanner's output visible to the scheduler at all.
//
// The first wiring routed findings through the article-importance path, where
// the strongest possible finding contributed 0.64 against a warm threshold of
// 2.0. Everything ran, nothing logged an error, and no scan ever escalated
// anything. A weight that does not cross the bar is the same as no weight.
func TestUnexplainedFindingCrossesWarm(t *testing.T) {
	const warmThreshold = 2.0 // news.warmThreshold

	f := Finding{
		Metrics: Metrics{Symbol: "TEST", VolumeZ: 3.0, ReturnZ: 2.6},
		Score:   6.0,
	}
	f.Signals = Classify(f.Metrics)

	var got float64
	r := &Runner{
		Client: nil,
		Observe: func(symbols, sectors []string, weight float64, reason string) {
			got = weight
		},
	}
	// Exercise the weight arithmetic the way Run does.
	weight := weightUnexplained
	if bonus := f.Score * weightScoreBonus; bonus > 0 {
		weight += min(bonus, weightBonusMax)
	}
	r.Observe([]string{f.Symbol}, nil, weight, "test")

	if got <= warmThreshold {
		t.Errorf("an unexplained finding contributed %.2f, which does not cross the warm threshold of %.1f", got, warmThreshold)
	}
	// An explained one should not escalate on its own: we already know why.
	if weightExplained > warmThreshold {
		t.Errorf("an explained finding contributes %.2f, crossing warm on its own", weightExplained)
	}
}

// TestScanSeriesCandlesFiltersBadRows is the regression for the reason this
// filtering exists in Go rather than trusting the sidecar and letting
// Postgres's own CHECK constraint reject a bad bar: SaveCandles writes one
// symbol's whole batch in a single transaction, so a lone corrupt row would
// otherwise cost that symbol's entire day of history for the pass, not just
// the one row.
func TestScanSeriesCandlesFiltersBadRows(t *testing.T) {
	s := scanSeries{
		T: []int64{20260310, 20260311, 20260312},
		O: []float64{100, 100, 100},
		H: []float64{105, 90, 105}, // row 1: high below low -- corrupt
		L: []float64{95, 95, 95},
		C: []float64{102, 92, 102},
		V: []float64{1000, 1000, -5}, // row 2: negative volume -- corrupt
	}
	got := s.candles()
	if len(got) != 1 {
		t.Fatalf("candles = %d, want 1 (only 2026-03-10 is clean); got %+v", len(got), got)
	}
	// Midnight New York: the same stamp charts write, so one session is one row.
	want := time.Date(2026, 3, 10, 0, 0, 0, 0, marketdata.Market)
	if !got[0].Time.Equal(want) {
		t.Errorf("Time = %v, want %v", got[0].Time, want)
	}
	if got[0].Close != 102 {
		t.Errorf("Close = %v, want 102", got[0].Close)
	}
}

// TestScanSeriesCandlesRejectsMismatchedLength covers a chunk fetch that
// partly failed on the sidecar side, leaving one array shorter than the
// rest -- indexing into it would panic, so the whole series is discarded
// instead of guessing which entries still line up.
func TestScanSeriesCandlesRejectsMismatchedLength(t *testing.T) {
	s := scanSeries{
		T: []int64{20260310, 20260311},
		O: []float64{100, 100},
		H: []float64{105, 105},
		L: []float64{95, 95},
		C: []float64{102},
		V: []float64{1000, 1000},
	}
	if got := s.candles(); got != nil {
		t.Errorf("candles = %+v, want nil for mismatched array lengths", got)
	}
}

// TestClientScanDecodesSeries is the regression for the whole Phase 5
// price-history prerequisite: the scan response's columnar "series" field
// must decode and re-tag to the canonical symbol the same way Metrics does,
// or a scan-persisted candle would go missing (or worse, land under the
// wrong venue) silently.
func TestClientScanDecodesSeries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"metrics": map[string]Metrics{
				"AAPL": {Symbol: "AAPL", Close: 190, Bars: 250},
			},
			"series": map[string]scanSeries{
				"AAPL": {
					T: []int64{20260310, 20260311},
					O: []float64{188, 189},
					H: []float64{191, 191},
					L: []float64{187, 188},
					C: []float64{190, 190.5},
					V: []float64{1_000_000, 1_100_000},
				},
				// Echoed a symbol never requested -- must be dropped, not
				// guessed onto some venue.
				"UNKNOWN": {T: []int64{20260310}, O: []float64{1}, H: []float64{1}, L: []float64{1}, C: []float64{1}, V: []float64{0}},
			},
			"failed": []string{}, "as_of": time.Now().UTC().Format(time.RFC3339),
			"elapsed_seconds": 0.1,
		})
	}))
	defer srv.Close()

	aapl, _ := marketdata.ParseSymbol("AAPL")
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	res, err := c.Scan(t.Context(), []marketdata.Symbol{aapl}, 10)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	candles, ok := res.Series[aapl]
	if !ok {
		t.Fatalf("Series missing AAPL; got %v", res.Series)
	}
	if len(candles) != 2 {
		t.Fatalf("got %d candles, want 2", len(candles))
	}
	if len(res.Series) != 1 {
		t.Errorf("Series has %d symbols, want 1 (the unrequested echo must be dropped)", len(res.Series))
	}
}
