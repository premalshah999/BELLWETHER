package paper

import (
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Performance answers "did it make money, and was it skill?".
type Performance struct {
	Since          *time.Time `json:"since,omitempty"`
	ReturnPct      float64    `json:"return_pct"`
	BenchmarkPct   *float64   `json:"benchmark_pct,omitempty"`
	ExcessPct      *float64   `json:"excess_pct,omitempty"`
	MaxDrawdownPct float64    `json:"max_drawdown_pct"`
	// Sharpe is annualised from daily returns with no risk-free rate.
	Sharpe        *float64     `json:"sharpe,omitempty"`
	VolatilityPct *float64     `json:"volatility_pct,omitempty"`
	Weeks         []WeekResult `json:"weeks"`
	WeeklyGoalPct float64      `json:"weekly_goal_pct"`
	GoalHitWeeks  int          `json:"goal_hit_weeks"`

	Trades        int      `json:"trades"`
	WinRate       float64  `json:"win_rate"`
	AvgWinPct     float64  `json:"avg_win_pct"`
	AvgLossPct    float64  `json:"avg_loss_pct"`
	ProfitFactor  *float64 `json:"profit_factor,omitempty"`
	RealizedCents Cents    `json:"realized_cents"`
	FeesCents     Cents    `json:"fees_cents"`

	Luck *LuckTest `json:"luck,omitempty"`
	// Verdict is the plain-English answer, with the evidence it rests on.
	Verdict string `json:"verdict"`
}

// WeekResult is one week's return.
type WeekResult struct {
	Week      string   `json:"week"` // Monday's date
	ReturnPct float64  `json:"return_pct"`
	Benchmark *float64 `json:"benchmark_pct,omitempty"`
}

// LuckTest compares the account with random traders who made the same number
// of trades, held them as long, in the same stocks and period.
type LuckTest struct {
	Simulations int     `json:"simulations"`
	Trades      int     `json:"trades"`
	ActualPct   float64 `json:"actual_pct"`
	MedianPct   float64 `json:"median_pct"`
	// BeatPct is the share of random traders the account did better than.
	BeatPct float64 `json:"beat_pct"`
	P90Pct  float64 `json:"p90_pct"`
}

// RoundTrip is one closed trade: what the random traders must match.
type RoundTrip struct {
	Symbol    string
	Opened    time.Time
	Closed    time.Time
	ReturnPct float64
}

// Analyze computes performance from equity marks, fills and round trips.
// pool supplies daily bars for the luck test; nil skips it.
func Analyze(marks []EquityPoint, fills []Fill, trips []RoundTrip, s Settings, pool map[string][]marketdata.Candle, seed int64) Performance {
	p := Performance{WeeklyGoalPct: s.WeeklyGoalPct, Weeks: []WeekResult{}}
	for _, f := range fills {
		p.FeesCents += f.FeeCents
		if f.RealizedCents != nil {
			p.RealizedCents += *f.RealizedCents
		}
	}
	summariseTrips(&p, trips)
	if pool != nil {
		p.Luck = LuckAgainst(trips, pool, seed, 1000)
	}
	if len(marks) < 2 {
		p.Verdict = verdict(p)
		if p.Luck == nil {
			p.Verdict = "Not enough history yet: the account needs to be marked over at least a few sessions."
		}
		return p
	}
	first, last := marks[0], marks[len(marks)-1]
	p.Since = &first.At
	p.ReturnPct = pctChange(float64(first.EquityCents), float64(last.EquityCents))
	if first.Benchmark != nil && last.Benchmark != nil && *first.Benchmark > 0 {
		b := pctChange(*first.Benchmark, *last.Benchmark)
		ex := round2(p.ReturnPct - b)
		p.BenchmarkPct, p.ExcessPct = &b, &ex
	}

	// Drawdown over every mark; daily returns from each session's last mark.
	peak := float64(first.EquityCents)
	daily := dailyCloses(marks)
	for _, m := range marks {
		e := float64(m.EquityCents)
		peak = math.Max(peak, e)
		if peak > 0 {
			p.MaxDrawdownPct = math.Max(p.MaxDrawdownPct, round2((peak-e)/peak*100))
		}
	}
	if len(daily) >= 5 {
		var rets []float64
		for i := 1; i < len(daily); i++ {
			if daily[i-1].EquityCents > 0 {
				rets = append(rets, float64(daily[i].EquityCents)/float64(daily[i-1].EquityCents)-1)
			}
		}
		mean, sd := meanSD(rets)
		if sd > 0 {
			sh := round2(mean / sd * math.Sqrt(252))
			vol := round2(sd * math.Sqrt(252) * 100)
			p.Sharpe, p.VolatilityPct = &sh, &vol
		}
	}
	p.Weeks = weekly(daily)
	for _, w := range p.Weeks {
		if w.ReturnPct >= s.WeeklyGoalPct {
			p.GoalHitWeeks++
		}
	}
	p.Verdict = verdict(p)
	return p
}

func summariseTrips(p *Performance, trips []RoundTrip) {
	p.Trades = len(trips)
	var wins, losses []float64
	var gain, loss float64
	for _, t := range trips {
		if t.ReturnPct > 0 {
			wins = append(wins, t.ReturnPct)
			gain += t.ReturnPct
		} else {
			losses = append(losses, t.ReturnPct)
			loss -= t.ReturnPct
		}
	}
	if p.Trades > 0 {
		p.WinRate = round2(float64(len(wins)) / float64(p.Trades) * 100)
	}
	if len(wins) > 0 {
		p.AvgWinPct = round2(gain / float64(len(wins)))
	}
	if len(losses) > 0 {
		p.AvgLossPct = round2(-loss / float64(len(losses)))
	}
	if loss > 0 {
		pf := round2(gain / loss)
		p.ProfitFactor = &pf
	}
}

// dailyCloses keeps each session's last mark.
func dailyCloses(marks []EquityPoint) []EquityPoint {
	var out []EquityPoint
	for _, m := range marks {
		day := m.At.In(marketdata.Market).Format("2006-01-02")
		if n := len(out); n > 0 && out[n-1].At.In(marketdata.Market).Format("2006-01-02") == day {
			out[n-1] = m
			continue
		}
		out = append(out, m)
	}
	return out
}

// weekly is each week's return, from the last close of the prior week.
func weekly(daily []EquityPoint) []WeekResult {
	var out []WeekResult
	var prev *EquityPoint
	var weekEnd EquityPoint
	current := ""
	flush := func() {
		if current == "" || prev == nil || prev.EquityCents <= 0 {
			return
		}
		w := WeekResult{Week: current, ReturnPct: pctChange(float64(prev.EquityCents), float64(weekEnd.EquityCents))}
		if prev.Benchmark != nil && weekEnd.Benchmark != nil && *prev.Benchmark > 0 {
			b := pctChange(*prev.Benchmark, *weekEnd.Benchmark)
			w.Benchmark = &b
		}
		out = append(out, w)
	}
	for i := range daily {
		d := daily[i]
		wk := monday(d.At)
		if wk != current {
			flush()
			if current != "" {
				last := weekEnd
				prev = &last
			} else {
				first := d
				prev = &first
			}
			current = wk
		}
		weekEnd = d
	}
	flush()
	return out
}

func monday(t time.Time) string {
	et := t.In(marketdata.Market)
	back := (int(et.Weekday()) + 6) % 7
	return et.AddDate(0, 0, -back).Format("2006-01-02")
}

// LuckAgainst runs the luck test with the bars it needs: for each simulation,
// every round trip is replaced by one in a random stock from the pool, entered
// on a random session in the account's period and held the same number of
// sessions; the simulated account compounds them the way the real one did.
func LuckAgainst(trips []RoundTrip, pool map[string][]marketdata.Candle, seed int64, sims int) *LuckTest {
	if len(trips) < 10 || len(pool) < 10 {
		return nil
	}
	symbols := make([]string, 0, len(pool))
	for s := range pool {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	from, to := trips[0].Opened, trips[0].Closed
	actual := 1.0
	for _, t := range trips {
		if t.Opened.Before(from) {
			from = t.Opened
		}
		if t.Closed.After(to) {
			to = t.Closed
		}
		actual *= 1 + t.ReturnPct/100
	}
	rng := rand.New(rand.NewSource(seed))
	results := make([]float64, 0, sims)
	for i := 0; i < sims; i++ {
		growth := 1.0
		for _, t := range trips {
			hold := max(1, sessionsBetween(t.Opened, t.Closed))
			bars := pool[symbols[rng.Intn(len(symbols))]]
			// Entries inside the account's own period, with room to hold.
			var window []int
			for j := range bars {
				if !bars[j].Time.Before(from.AddDate(0, 0, -1)) && !bars[j].Time.After(to) && j+hold < len(bars) {
					window = append(window, j)
				}
			}
			if len(window) == 0 {
				continue
			}
			j := window[rng.Intn(len(window))]
			if bars[j].Close > 0 {
				growth *= bars[j+hold].Close / bars[j].Close
			}
		}
		results = append(results, (growth-1)*100)
	}
	sort.Float64s(results)
	act := (actual - 1) * 100
	beat := sort.SearchFloat64s(results, act)
	return &LuckTest{
		Simulations: sims, Trades: len(trips), ActualPct: round2(act),
		MedianPct: round2(results[len(results)/2]), P90Pct: round2(results[len(results)*9/10]),
		BeatPct: round2(float64(beat) / float64(len(results)) * 100),
	}
}

func sessionsBetween(a, b time.Time) int {
	n := 0
	for d := a.In(marketdata.Market); d.Before(b); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}

func verdict(p Performance) string {
	switch {
	case p.Luck != nil && p.Luck.BeatPct >= 95:
		return "The account beat 95% or more of random traders making the same trades in the same stocks: evidence of real skill, if it holds up over more trades."
	case p.Luck != nil && p.Luck.BeatPct <= 50:
		return "The account did no better than half of random traders with the same activity. On this evidence the results are luck, not an edge."
	case p.Luck != nil:
		return "Better than most random traders, but not by enough to rule out luck. Keep trading it on paper before trusting it."
	case p.Trades < 10:
		return "Too few closed trades to tell skill from luck: the test needs at least ten."
	case p.ExcessPct != nil && *p.ExcessPct > 0:
		return "Ahead of the S&P 500 so far. The skill-or-luck test runs once enough price history is loaded."
	default:
		return "Behind the S&P 500 so far."
	}
}

func pctChange(from, to float64) float64 {
	if from == 0 {
		return 0
	}
	return round2((to/from - 1) * 100)
}

func meanSD(xs []float64) (float64, float64) {
	if len(xs) < 2 {
		return 0, 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var ss float64
	for _, x := range xs {
		ss += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(ss / float64(len(xs)-1))
}

func round2(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*100) / 100
}
