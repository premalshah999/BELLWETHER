package paper

import (
	"fmt"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func mark(day int, equity float64, bench float64) EquityPoint {
	b := bench
	return EquityPoint{At: time.Date(2026, 9, day, 20, 0, 0, 0, time.UTC), EquityCents: CentsOf(equity), Benchmark: &b}
}

func TestAnalyzeReturnsDrawdownAndWeeks(t *testing.T) {
	marks := []EquityPoint{
		mark(7, 10_000, 100), mark(8, 10_500, 101), mark(9, 9_800, 100), mark(10, 10_200, 102), mark(11, 10_400, 102),
		mark(14, 11_000, 103), mark(15, 11_200, 104),
	}
	p := Analyze(marks, nil, nil, DefaultSettings(), nil, 1)
	if p.ReturnPct != 12 {
		t.Errorf("return = %v, want 12", p.ReturnPct)
	}
	if p.ExcessPct == nil || *p.ExcessPct != 8 {
		t.Errorf("excess = %v, want 8 (12 vs 4)", p.ExcessPct)
	}
	if p.MaxDrawdownPct != 6.67 {
		t.Errorf("drawdown = %v, want 6.67 (10,500 to 9,800)", p.MaxDrawdownPct)
	}
	if len(p.Weeks) != 2 || p.Weeks[1].ReturnPct != 7.69 {
		t.Errorf("weeks = %+v, want week 2 at +7.69%% (10,400 to 11,200)", p.Weeks)
	}
	if p.Sharpe == nil {
		t.Error("seven sessions of marks should give a Sharpe ratio")
	}
	if p.Beta == nil || p.AlphaPct == nil {
		t.Error("seven sessions beside the S&P 500 should give a beta and an alpha")
	}
}

// A trader who only ever buys before a rise must beat nearly every random
// trader; one whose trades return what the market did must not.
func TestLuckSeparatesSkillFromChance(t *testing.T) {
	pool := map[string][]marketdata.Candle{}
	start := time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		var bars []marketdata.Candle
		price := 100.0
		for d := 0; d < 120; d++ {
			// Noise that nets out: up, down, up, down.
			if d%2 == 0 {
				price *= 1.01
			} else {
				price /= 1.01
			}
			bars = append(bars, marketdata.Candle{Time: start.AddDate(0, 0, d), Close: price})
		}
		pool[fmt.Sprintf("S%02d", i)] = bars
	}
	trip := func(ret float64, day int) RoundTrip {
		o := start.AddDate(0, 0, day)
		return RoundTrip{Symbol: "S00", Opened: o, Closed: o.AddDate(0, 0, 3), ReturnPct: ret}
	}
	var skilled, lucky []RoundTrip
	for i := 0; i < 12; i++ {
		skilled = append(skilled, trip(3, i*7))
		lucky = append(lucky, trip(0, i*7))
	}
	if l := LuckAgainst(skilled, pool, 7, 500); l == nil || l.BeatPct < 95 {
		t.Errorf("a trader making 3%% every time beat only %+v", l)
	}
	if l := LuckAgainst(lucky, pool, 7, 500); l == nil || l.BeatPct > 60 {
		t.Errorf("a trader making nothing beat %+v of random traders", l)
	}
	if LuckAgainst(skilled[:5], pool, 7, 500) != nil {
		t.Error("five trades are too few for the test")
	}
}
