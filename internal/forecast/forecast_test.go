package forecast

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// A planted signal must be found out of sample, weighted above noise, and
// reflected in the ranking of the last session.
func TestRunFindsAPlantedSignal(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	start := time.Date(2021, 1, 4, 0, 0, 0, 0, marketdata.Market)
	var rows []Row
	for d := 0; d < 1100; d++ {
		day := start.AddDate(0, 0, d)
		for s := 0; s < 80; s++ {
			x := make([]float64, len(Factors))
			for i := range x {
				x[i] = rng.NormFloat64()
			}
			y := 0.02*x[0] + 0.02*rng.NormFloat64() // factor 0 predicts; the rest is noise
			if d >= 1095 {
				y = math.NaN() // the last sessions' futures are not known yet
			}
			rows = append(rows, Row{Symbol: fmt.Sprintf("S%02d", s), Date: day, X: x, Y: y})
		}
	}
	rep, preds, err := Run(rows, start.AddDate(0, 0, 1100))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Overall.RankIC < 0.3 || rep.Overall.T < 10 {
		t.Errorf("out-of-sample rank IC = %.3f (t %.1f), want the planted signal found", rep.Overall.RankIC, rep.Overall.T)
	}
	if len(rep.Years) < 2 {
		t.Errorf("years = %+v, want a walk-forward record per test year", rep.Years)
	}
	for i, f := range rep.Factors {
		if i > 0 && math.Abs(f.Weight) >= rep.Factors[0].Weight {
			t.Errorf("noise factor %s weighted %.3f, not below the signal's %.3f", f.Key, f.Weight, rep.Factors[0].Weight)
		}
	}
	if len(preds) != 80 || preds[0].Percentile != 100 || preds[0].Drivers[0].Key != "mom_12_1" {
		t.Errorf("top prediction = %+v", preds[0])
	}
}

// Pure noise must look like noise.
func TestRunOnNoiseReportsNoSkill(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	start := time.Date(2021, 1, 4, 0, 0, 0, 0, marketdata.Market)
	var rows []Row
	for d := 0; d < 900; d++ {
		for s := 0; s < 60; s++ {
			x := make([]float64, len(Factors))
			for i := range x {
				x[i] = rng.NormFloat64()
			}
			rows = append(rows, Row{Symbol: fmt.Sprintf("S%02d", s), Date: start.AddDate(0, 0, d), X: x, Y: rng.NormFloat64()})
		}
	}
	rep, _, err := Run(rows, start.AddDate(0, 0, 900))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(rep.Overall.RankIC) > 0.03 {
		t.Errorf("rank IC on noise = %.3f", rep.Overall.RankIC)
	}
}

func TestFeaturesArePointInTime(t *testing.T) {
	var bars []marketdata.Candle
	start := time.Date(2025, 1, 2, 0, 0, 0, 0, marketdata.Market)
	for i := 0; i < 300; i++ {
		p := 100 + float64(i)
		bars = append(bars, marketdata.Candle{Time: start.AddDate(0, 0, i), Open: p, High: p + 1, Low: p - 1, Close: p, Volume: 1000})
	}
	// An announcement after the last close must not reach the last row.
	late := []Earning{{At: bars[299].Time.Add(20 * time.Hour), SurprisePct: 30}}
	rows := Features("X", bars, late, nil, 1)
	last := rows[len(rows)-1]
	if last.X[7] != 0 {
		t.Errorf("surprise = %v, want 0: it was announced after the session", last.X[7])
	}
	if !math.IsNaN(last.Y) {
		t.Error("the last session's forward return cannot be known")
	}
	early := []Earning{{At: bars[290].Time, SurprisePct: 12}}
	if got := Features("X", bars, early, nil, 1); got[len(got)-1].X[7] != 12 {
		t.Errorf("surprise = %v, want 12", got[len(got)-1].X[7])
	}
}
