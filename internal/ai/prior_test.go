package ai

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/forecast"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// normalDist is an engine distribution for a normal log return.
func normalDist(sigma float64, earnings bool) *forecast.Distribution {
	inv := map[float64]float64{0.05: -1.6449, 0.1: -1.2816, 0.15: -1.0364, 0.2: -0.8416, 0.25: -0.6745, 0.3: -0.5244,
		0.35: -0.3853, 0.4: -0.2533, 0.45: -0.1257, 0.5: 0}
	var q []float64
	for _, l := range forecast.QuantileLevels {
		z, ok := inv[l]
		if !ok {
			z = -inv[math.Round((1-l)*100)/100]
		}
		q = append(q, math.Expm1(z*sigma))
	}
	d := &forecast.Distribution{NormalVolPct: 0.04 / math.Sqrt(10.0/252) * 100, VolPct: 30,
		Horizons: []forecast.HorizonDist{{Horizon: 10, Quantiles: q, PUp: 0.5, PBeat: 0.5}}}
	if earnings {
		d.Earnings = &forecast.EarningsAhead{Session: 3, TypicalMovePct: 5}
	}
	return d
}

// One normal-sized move either way: a calm stock's distribution puts about
// 16% in each tail, a stock with a report inside the window much more.
func TestPriorScenariosFollowTheDistribution(t *testing.T) {
	calm := priorFrom(normalDist(0.04, false), time.Now(), 10)
	if calm == nil {
		t.Fatal("no prior")
	}
	if math.Abs(calm.ThresholdPct-4) > 0.01 {
		t.Fatalf("threshold %.2f, want 4", calm.ThresholdPct)
	}
	if math.Abs(calm.Bull-0.16) > 0.03 || math.Abs(calm.Bear-0.16) > 0.03 {
		t.Errorf("calm: bull %.2f bear %.2f, want about 0.16 each", calm.Bull, calm.Bear)
	}
	wide := priorFrom(normalDist(0.07, true), time.Now(), 10)
	if wide.Bull < 0.25 || wide.Bear < 0.25 || wide.EarningsSession != 3 {
		t.Errorf("report ahead: bull %.2f bear %.2f session %d", wide.Bull, wide.Bear, wide.EarningsSession)
	}
	if !(wide.BullMovePct > wide.ThresholdPct && wide.BearMovePct < -wide.ThresholdPct) {
		t.Errorf("scenario moves %.1f / %.1f outside their thresholds", wide.BullMovePct, wide.BearMovePct)
	}
}

// A writer may lean and widen, within bounds, and nothing more.
func TestAdjustmentIsBounded(t *testing.T) {
	p := priorFrom(normalDist(0.04, false), time.Now(), 10)
	up := p.adjusted(&Adjustment{ShiftSigma: 0.3, VolScale: 1})
	if up.Bull <= p.Bull || up.Bear >= p.Bear || up.PUp <= p.PUp {
		t.Errorf("a positive shift did not lean bullish: %+v", up)
	}
	a := &Adjustment{ShiftSigma: 5, VolScale: 9}
	wild := p.adjusted(a)
	if a.ShiftSigma != maxShift || a.VolScale != maxVolScale {
		t.Errorf("adjustment not clamped: %+v", a)
	}
	if wild.Bull > 0.75 {
		t.Errorf("a clamped adjustment still produced bull %.2f", wild.Bull)
	}
	same := p.adjusted(&Adjustment{})
	if math.Abs(same.Bull-p.Bull) > 1e-9 || math.Abs(same.Bear-p.Bear) > 1e-9 {
		t.Errorf("no adjustment changed the prior: %+v vs %+v", same, p)
	}
}

// A stock the engine does not cover gets a prior from its own history.
func TestPriorFromHistory(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	price := 100.0
	var candles []marketdata.Candle
	for i := 0; i < 520; i++ {
		price *= math.Exp(rng.NormFloat64() * 0.02)
		candles = append(candles, marketdata.Candle{Close: price})
	}
	p := priorFromHistory(candles, 10)
	if p == nil {
		t.Fatal("no prior")
	}
	if want := 0.02 * math.Sqrt(10) * 100; math.Abs(p.ThresholdPct-want) > 1 {
		t.Errorf("threshold %.2f, want about %.2f", p.ThresholdPct, want)
	}
	if s := p.Bull + p.Base + p.Bear; math.Abs(s-1) > 1e-6 {
		t.Errorf("scenarios sum to %.3f", s)
	}
}
