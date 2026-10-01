package forecast

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// GARCH must recover a known process's persistence and asymmetry.
func TestGARCHRecoversParameters(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	omega, alpha, gamma, beta := 2e-6, 0.03, 0.08, 0.90
	h := omega / (1 - alpha - gamma/2 - beta)
	var xs []float64
	for i := 0; i < 3000; i++ {
		z := rng.NormFloat64()
		x := math.Sqrt(h) * z
		xs = append(xs, x)
		neg := 0.0
		if x < 0 {
			neg = 1
		}
		h = omega + (alpha+gamma*neg)*x*x + beta*h
	}
	g := fitGARCH(xs)
	if !g.ok {
		t.Fatal("did not fit")
	}
	if p := g.persistence(); math.Abs(p-0.97) > 0.03 {
		t.Errorf("persistence = %.3f, want about 0.97", p)
	}
	if g.Gamma <= g.Alpha {
		t.Errorf("asymmetry lost: alpha %.3f gamma %.3f", g.Alpha, g.Gamma)
	}
}

// The regime model must separate a calm and a stressed state.
func TestRegimeSeparatesStates(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	var xs []float64
	var truth []int
	s := 0
	for i := 0; i < 2500; i++ {
		if s == 0 && rng.Float64() < 0.01 {
			s = 1
		} else if s == 1 && rng.Float64() < 0.04 {
			s = 0
		}
		sd := 0.007
		if s == 1 {
			sd = 0.022
		}
		xs = append(xs, 0.0004+sd*rng.NormFloat64())
		truth = append(truth, s)
	}
	g, ok := fitRegime(xs)
	if !ok {
		t.Fatal("did not fit")
	}
	if math.Abs(g.Sigma[0]-0.007) > 0.002 || math.Abs(g.Sigma[1]-0.022) > 0.005 {
		t.Errorf("sigmas = %v", g.Sigma)
	}
	probs := g.Filter(xs)
	right := 0
	for i, p := range probs {
		if (p > 0.5) == (truth[i] == 1) {
			right++
		}
	}
	if acc := float64(right) / float64(len(xs)); acc < 0.85 {
		t.Errorf("state accuracy %.2f", acc)
	}
}

// Trees must find an interaction a linear model cannot.
func TestGBDTFindsInteraction(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	n := 40000
	x := make([][]float32, n)
	y := make([]float64, n)
	for i := range x {
		row := make([]float32, 5)
		for f := range row {
			row[f] = float32(rng.Float64()*2 - 1)
		}
		x[i] = row
		sign := 1.0
		if row[0] < 0 {
			sign = -1
		}
		y[i] = 0.3*sign*float64(row[1]) + rng.NormFloat64()*0.5
	}
	cfg := defaultGB
	cfg.MinLeaf = 200
	m := fitGBDT(x, y, n*4/5, cfg)
	var se, se0 float64
	for i := n * 4 / 5; i < n; i++ {
		d := y[i] - m.Predict(x[i])
		se += d * d
		se0 += y[i] * y[i]
	}
	if se >= se0*0.95 {
		t.Errorf("held-out error %.1f, no better than predicting zero (%.1f)", se, se0)
	}
	if m.Gain[0] < m.Gain[2] || m.Gain[1] < m.Gain[3] {
		t.Errorf("gain on noise features: %v", m.Gain)
	}
	c := m.contributions(x[0])
	var sum float64
	for _, v := range c {
		sum += v
	}
	// Each tree's root value is the bias the path contributions start from.
	bias := m.Base
	for _, tr := range m.Trees {
		bias += m.LR * tr.value[0]
	}
	if math.Abs(bias+sum-m.Predict(x[0])) > 1e-9 {
		t.Errorf("contributions do not add up to the prediction")
	}
}

// The sample CRPS must agree with the closed form, and PITs of a correct
// forecast must be uniform.
func TestScoringRules(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	xs := make([]float64, 20000)
	for i := range xs {
		xs[i] = rng.NormFloat64() * 0.02
	}
	sort.Float64s(xs)
	for _, y := range []float64{-0.03, 0, 0.01} {
		a, b := crpsSorted(xs, y), crpsNormal(0, 0.02, y)
		if math.Abs(a-b) > 0.0003 {
			t.Errorf("CRPS at %.2f: sample %.5f, closed form %.5f", y, a, b)
		}
	}
	var pits []float64
	for i := 0; i < 5000; i++ {
		pits = append(pits, pitSorted(xs, rng.NormFloat64()*0.02))
	}
	for i, f := range pitHistogram(pits) {
		if math.Abs(f-0.1) > 0.025 {
			t.Errorf("PIT bin %d holds %.3f", i, f)
		}
	}
	// A forecaster that says 70% when the truth is 50% is pulled back.
	var p, o []float64
	for i := 0; i < 4000; i++ {
		p = append(p, 0.7)
		o = append(o, b2f(rng.Float64() < 0.5))
	}
	if v := fitIsotonic(p, o).Apply(0.7); math.Abs(v-0.5) > 0.03 {
		t.Errorf("isotonic maps 0.7 to %.2f", v)
	}
	// Recalibration finds the level that truly holds 10%.
	var skewed []float64
	for i := 0; i < 5000; i++ {
		skewed = append(skewed, math.Pow(rng.Float64(), 1.5)) // too many low PITs
	}
	if l := newPITMap(skewed).Level(0.1); l >= 0.1 {
		t.Errorf("level %.3f, want below 0.1 for a forecast that is too high", l)
	}
}

// synthetic builds a market: a regime-switching index, and stocks that are
// beta times it plus GARCH shocks, a planted one-week reversal, and an
// earnings jump every quarter.
func synthetic(nStocks, nDays int, seed int64) *Panel {
	rng := rand.New(rand.NewSource(seed))
	start := time.Date(2019, 1, 2, 0, 0, 0, 0, marketdata.Market)
	var days []time.Time
	for d := start; len(days) < nDays; d = d.AddDate(0, 0, 1) {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			days = append(days, d)
		}
	}
	mret := make([]float64, nDays)
	s := 0
	mc := make([]marketdata.Candle, nDays)
	vix := make([]marketdata.Candle, nDays)
	price := 3000.0
	for i := range days {
		if s == 0 && rng.Float64() < 0.01 {
			s = 1
		} else if s == 1 && rng.Float64() < 0.05 {
			s = 0
		}
		sd := 0.008
		if s == 1 {
			sd = 0.02
		}
		if i > 0 {
			mret[i] = 0.0003 + sd*rng.NormFloat64()
		}
		o := price
		price *= math.Exp(mret[i])
		hi, lo := math.Max(o, price)*(1+sd/3), math.Min(o, price)*(1-sd/3)
		mc[i] = marketdata.Candle{Time: days[i], Open: o, High: hi, Low: lo, Close: price, Volume: 1e9}
		vix[i] = marketdata.Candle{Time: days[i], Close: sd * math.Sqrt(252) * 100}
	}
	p := NewPanel(mc, vix)
	sectors := []string{"Tech", "Energy", "Health", "Banks"}
	for k := 0; k < nStocks; k++ {
		beta := 0.6 + rng.Float64()
		h := 0.00015 + rng.Float64()*0.0003
		omega := h * 0.04
		var bars []marketdata.Candle
		var earns []Earning
		price := 50 + rng.Float64()*100
		idio := make([]float64, nDays)
		offset := rng.Intn(63)
		for i := range days {
			// A planted reversal: half of last week's idiosyncratic move
			// comes back over the next week.
			var past float64
			for j := max(0, i-5); j < i; j++ {
				past += idio[j]
			}
			z := rng.NormFloat64()
			e := -0.1*past + math.Sqrt(h)*z
			if i%63 == offset && i > 0 {
				e += rng.NormFloat64() * 0.06
				at := days[i-1].Add(16*time.Hour + 30*time.Minute)
				earns = append(earns, Earning{At: at, SurprisePct: rng.NormFloat64() * 5})
			}
			idio[i] = e
			r := beta*mret[i] + e
			if i == 0 {
				r = 0
			}
			o := price * math.Exp(0.2*r)
			price *= math.Exp(r)
			hi := math.Max(o, price) * (1 + math.Sqrt(h)/2)
			lo := math.Min(o, price) * (1 - math.Sqrt(h)/2)
			bars = append(bars, marketdata.Candle{Time: days[i], Open: o, High: hi, Low: lo, Close: price, Volume: 1e6 * (1 + rng.Float64())})
			neg := 0.0
			if z < 0 {
				neg = 1
			}
			h = omega + (0.03+0.06*neg)*h*z*z + 0.91*h
		}
		p.AddStock(fmt.Sprintf("S%03d", k), sectors[k%4], bars, earns, nil)
	}
	return p
}

// End to end: the planted reversal must be found out of sample, the
// intervals must hold about what they claim, the forecast must beat a
// normal curve, and every live forecast must be a coherent distribution.
func TestEngineEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates a market")
	}
	p := synthetic(160, 1100, 11)
	// Ten stocks report a week after the last session, from the calendar.
	upcoming := map[string]time.Time{}
	for k := 0; k < 10; k++ {
		upcoming[fmt.Sprintf("S%03d", k)] = p.Days[p.T()-1].AddDate(0, 0, 7)
	}
	rep, preds, err := Run(p, Config{Paths: 1000, EvalPaths: 300, EvalStocks: 80, Now: time.Now(), Upcoming: upcoming})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []int{5, 20} {
		a, v := rep.Alpha[h], rep.Vol[h]
		t.Logf("h%d alpha: IC %.3f (t %.1f) ridge %.3f tree %.3f momentum %.3f; vol QLIKE har %.3f garch %.3f blend %.3f naive %.3f",
			h, a.IC, a.T, a.RidgeIC, a.TreeIC, a.MomentumIC, v.HAR, v.GARCH, v.Blend, v.Naive)
	}
	for _, h := range Horizons {
		d := rep.Dist[h]
		t.Logf("h%d dist: n %d CRPS skill vs normal %.1f%% (DM t %.1f), vs history %.1f%% (t %.1f); coverage %v calibrated %v; Brier up %.4f vs %.4f",
			h, d.N, d.SkillNormal, d.DMNormalT, d.SkillBoot, d.DMBootT, d.Coverage, d.CalCoverage, d.BrierUp, d.BrierUpClim)
	}
	if a := rep.Alpha[5]; a.IC < 0.03 || a.T < 2 {
		t.Errorf("planted reversal: IC %.3f t %.1f", a.IC, a.T)
	}
	if a := rep.Alpha[5]; a.IC <= a.MomentumIC {
		t.Errorf("model IC %.3f not above momentum's %.3f", a.IC, a.MomentumIC)
	}
	d := rep.Dist[5]
	if d.Coverage[1] < 70 || d.Coverage[1] > 90 {
		t.Errorf("80%% interval held %.1f%%", d.Coverage[1])
	}
	if d.SkillNormal <= 0 {
		t.Errorf("CRPS skill against a normal curve %.1f%%", d.SkillNormal)
	}
	if v := rep.Vol[5]; v.Blend >= v.Naive {
		t.Errorf("volatility blend %.4f no better than naive %.4f", v.Blend, v.Naive)
	}
	if len(preds) < 100 {
		t.Fatalf("%d predictions", len(preds))
	}
	withEarn := 0
	for _, pr := range preds {
		if pr.Dist == nil {
			continue
		}
		if len(pr.Dist.Horizons) != 3 || len(pr.Dist.Fan) != longest {
			t.Fatalf("%s: incomplete distribution", pr.Symbol)
		}
		for _, hd := range pr.Dist.Horizons {
			if !sort.Float64sAreSorted(hd.Quantiles) || hd.PUp <= 0 || hd.PUp >= 1 {
				t.Fatalf("%s h%d: incoherent %+v", pr.Symbol, hd.Horizon, hd)
			}
		}
		if pr.Dist.Earnings != nil {
			withEarn++
			// A report inside the window must widen the 20-day spread
			// beyond the diffusive volatility alone.
			h20 := pr.Dist.Horizons[2]
			diffusive := pr.Dist.VolPct / 100 * math.Sqrt(20.0/252)
			if h20.Sigma <= diffusive {
				t.Errorf("%s: earnings ahead but sigma %.4f <= diffusive %.4f", pr.Symbol, h20.Sigma, diffusive)
			}
		}
	}
	if withEarn == 0 {
		t.Error("no stock had a report inside its window")
	}
}
