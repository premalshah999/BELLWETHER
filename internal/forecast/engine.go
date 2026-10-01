package forecast

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Config sets the engine's effort.
type Config struct {
	Now time.Time
	// Paths is how many simulated paths each live forecast draws.
	Paths int
	// EvalPaths and EvalStocks bound the walk-forward test of the full
	// distribution, which simulates every sampled stock every week.
	EvalPaths  int
	EvalStocks int
	Workers    int
	// Upcoming are scheduled report dates from the calendar, for stocks
	// whose next report the history does not yet hold.
	Upcoming map[string]time.Time
	Log      func(msg string, args ...any)
}

func (c *Config) defaults() {
	if c.Paths <= 0 {
		c.Paths = 4000
	}
	if c.EvalPaths <= 0 {
		c.EvalPaths = 400
	}
	if c.EvalStocks <= 0 {
		c.EvalStocks = 400
	}
	if c.Workers <= 0 {
		c.Workers = 2
	}
	if c.Log == nil {
		c.Log = func(string, ...any) {}
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
}

const (
	warmup     = 260 // sessions of history a row needs
	step       = 5   // a row every week: neighbouring days' labels overlap
	trainYears = 3
	// minTrainDates is how many weekly dates a model needs before its
	// year can be tested.
	minTrainDates = 60
)

type engine struct {
	p       *Panel
	cfg     Config
	mv      *marketView
	views   []*stockView
	sectors []string
	dates   []int
	rows    map[int][]Row
	evalSet map[int32]bool

	// Out-of-sample records, accumulated year by year, from which each
	// later year's calibration is fitted.
	slopeOOS map[int][]float64 // weekly cross-sectional slopes of return on score
	factorIC [][]float64       // per stock factor, its weekly 20-session rank IC
	// volLoss sums each candidate blend weight's QLIKE, per horizon.
	volLoss map[int]*[5]float64
	volN    map[int]int
	// Per tested year, oldest first: recalibration reads only the latest
	// calYears of them, because a forecast's misses change with the market
	// it is made in, and years after a crash miss differently from years
	// after a calm.
	pitOOS  map[int][][]float64
	upOOS   map[int][][]probRec
	beatOOS map[int][][]probRec

	years []YearRecord
}

// blendWeights are the shares on HAR the volatility blend chooses among.
var blendWeights = [5]float64{0, 0.25, 0.5, 0.75, 1}

type probRec struct{ p, outcome float64 }

// Run validates the engine walk-forward and forecasts the latest session.
func Run(p *Panel, cfg Config) (Report, []Prediction, error) {
	cfg.defaults()
	if p.T() < 600 || len(p.Stocks) < 50 {
		return Report{}, nil, fmt.Errorf("forecast: %d sessions and %d stocks, too little history", p.T(), len(p.Stocks))
	}
	e := &engine{p: p, cfg: cfg, rows: map[int][]Row{},
		slopeOOS: map[int][]float64{}, factorIC: make([][]float64, len(Factors)), volLoss: map[int]*[5]float64{5: {}, 20: {}}, volN: map[int]int{}, pitOOS: map[int][][]float64{},
		upOOS: map[int][][]probRec{}, beatOOS: map[int][][]probRec{}}
	started := time.Now()
	e.mv = p.market()
	e.views = make([]*stockView, len(p.Stocks))
	e.sectors = make([]string, len(p.Stocks))
	parallel(len(p.Stocks), cfg.Workers, func(i int) {
		e.views[i] = p.view(p.Stocks[i], e.mv)
	})
	for i, s := range p.Stocks {
		e.sectors[i] = s.Sector
	}
	e.planDates()
	e.pickEvalSet()
	cfg.Log("forecast: panel ready", "dates", len(e.dates), "elapsed", time.Since(started).Round(time.Second))

	for _, y := range e.testYears() {
		if err := e.walk(y); err != nil {
			cfg.Log("forecast: year skipped", "year", y, "err", err)
			continue
		}
		cfg.Log("forecast: year tested", "year", y, "elapsed", time.Since(started).Round(time.Second))
	}
	if len(e.years) == 0 {
		return Report{}, nil, errors.New("forecast: no year could be tested")
	}
	rep, preds, err := e.live()
	if err != nil {
		return rep, nil, err
	}
	cfg.Log("forecast: done", "stocks", len(preds), "elapsed", time.Since(started).Round(time.Second))
	return rep, preds, nil
}

func parallel(n, workers int, f func(i int)) {
	var wg sync.WaitGroup
	ch := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				f(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
}

// planDates lists the weekly sessions, back from the last so today is
// always included, that have enough stocks with a year of history.
func (e *engine) planDates() {
	T := e.p.T()
	firsts := make([]int, 0, len(e.views))
	for _, v := range e.views {
		firsts = append(firsts, v.s.First)
	}
	sort.Ints(firsts)
	for t := T - 1; t >= warmup; t -= step {
		// Stocks whose history began at least a warm-up before t.
		if sort.SearchInts(firsts, t-warmup+1) >= 30 {
			e.dates = append(e.dates, t)
		}
	}
	sort.Ints(e.dates)
}

// ensureRows computes the factors for every planned session in [lo, hi)
// not yet built, and frees those before lo: the walk only ever needs its
// training window and its test year, so ten years of history never sit in
// memory at once.
func (e *engine) ensureRows(lo, hi int) {
	for t := range e.rows {
		if t < lo {
			delete(e.rows, t)
		}
	}
	var todo []int
	for _, t := range e.dates {
		if t >= lo && t < hi && e.rows[t] == nil {
			todo = append(todo, t)
		}
	}
	var mu sync.Mutex
	parallel(len(todo), e.cfg.Workers, func(k int) {
		t := todo[k]
		var day []Row
		for i, v := range e.views {
			x := e.p.features(v, e.mv, t)
			if x == nil {
				continue
			}
			day = append(day, Row{Stock: int32(i), T: int32(t), X: x, Beta: v.beta[t],
				Y5: e.p.abnormal(v, e.mv, t, 5), Y20: e.p.abnormal(v, e.mv, t, 20)})
		}
		if len(day) < 30 {
			day = []Row{}
		} else {
			sectorFill(day, e.sectors)
			rankDay(day)
		}
		mu.Lock()
		e.rows[t] = day
		mu.Unlock()
	})
}

// pickEvalSet chooses the stocks whose full distribution is tested each
// week: an even spread through the universe in symbol order, so it is not
// biased toward any sector or size.
func (e *engine) pickEvalSet() {
	idx := make([]int, len(e.p.Stocks))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return e.p.Stocks[idx[a]].Symbol < e.p.Stocks[idx[b]].Symbol })
	e.evalSet = map[int32]bool{}
	every := max(1, len(idx)/e.cfg.EvalStocks)
	for k := 0; k < len(idx); k += every {
		e.evalSet[int32(idx[k])] = true
	}
}

// testYears are the calendar years with enough earlier history to fit on.
func (e *engine) testYears() []int {
	var out []int
	seen := map[int]bool{}
	for _, t := range e.dates {
		y := e.p.Days[t].Year()
		if seen[y] {
			continue
		}
		seen[y] = true
		start := e.yearStart(y)
		n := 0
		for _, d := range e.dates {
			if d < start-longest {
				n++
			}
		}
		if n >= minTrainDates {
			out = append(out, y)
		}
	}
	return out
}

func (e *engine) yearStart(y int) int {
	for t, d := range e.p.Days {
		if d.Year() >= y {
			return t
		}
	}
	return e.p.T()
}

// models is everything fitted on one training window.
type models struct {
	ridge   map[int]Model
	comp    map[int][]float64 // IC-weighted composite: a weight per factor
	gbdt    map[int]GBDT
	har     map[int]HAR
	mhar    map[int]HAR
	garch   []GARCH
	gH1     [][]float32 // per stock: one-step GARCH variance at each session
	regime  Regime
	stress  []float64 // filtered stress probability at each session
	lo, hi  int       // training sessions [lo, hi)
	trained int
}

// fit trains every model on sessions [lo, hi), using only outcomes that
// were known by hi.
func (e *engine) fit(lo, hi int) (*models, error) {
	m := &models{ridge: map[int]Model{}, comp: map[int][]float64{}, gbdt: map[int]GBDT{}, har: map[int]HAR{}, mhar: map[int]HAR{}, lo: lo, hi: hi}
	var train []int
	for _, t := range e.dates {
		if t >= lo && t < hi {
			train = append(train, t)
		}
	}
	if len(train) < minTrainDates/2 {
		return nil, fmt.Errorf("forecast: %d training dates", len(train))
	}
	// Return models, per horizon, on rank targets.
	for _, h := range []int{5, 20} {
		var x [][]float32
		var y []float64
		var rr []Row
		dayIC := make([][]float64, len(Factors))
		split := 0
		cut := train[len(train)*4/5]
		for _, t := range train {
			if t+h >= hi {
				continue
			}
			day := e.rows[t]
			ys := make([]float64, 0, len(day))
			var at []int
			for i, r := range day {
				v := r.Y5
				if h == 20 {
					v = r.Y20
				}
				if !isNaN32(v) {
					ys = append(ys, float64(v))
					at = append(at, i)
				}
			}
			if len(ys) < 30 {
				continue
			}
			yr := rankScale(ys)
			for j, rk := range yr {
				r := day[at[j]]
				x = append(x, r.X)
				y = append(y, rk)
				rr = append(rr, Row{X: r.X[:len(Factors)], Y5: float32(rk)})
				if t < cut {
					split = len(x)
				}
			}
			fx := make([]float64, len(at))
			for f := range Factors {
				for j, i := range at {
					fx[j] = float64(day[i].X[f])
				}
				dayIC[f] = append(dayIC[f], pearson(fx, yr))
			}
		}
		m.comp[h] = icWeights(dayIC, h)
		if len(x) < 5000 || split < len(x)/2 {
			return nil, fmt.Errorf("forecast: %d rows for horizon %d", len(x), h)
		}
		ridge, err := fitRidge(rr, 0.01)
		if err != nil {
			return nil, err
		}
		m.ridge[h] = ridge
		m.gbdt[h] = fitGBDT(x, y, split, defaultGB)
		m.trained = len(x)
	}
	// Volatility: pooled HAR for stocks, its own for the market.
	for _, h := range []int{5, 20} {
		var rows []harSample
		for _, t := range train {
			if t+h >= hi {
				continue
			}
			for _, r := range e.rows[t] {
				v := e.views[r.Stock]
				rows = append(rows, harSample{harX(v.cv, e.mv.cv, e.mv.vix[t], t), lg(v.cv.future(t, h))})
			}
		}
		har, ok := fitHAR(h, rows)
		if !ok {
			return nil, fmt.Errorf("forecast: HAR for horizon %d did not fit", h)
		}
		m.har[h] = har
		var mrows []harSample
		for t := max(lo, 260); t+h < hi; t++ {
			mrows = append(mrows, harSample{marketX(e.mv, t), lg(e.mv.cv.future(t, h))})
		}
		mh, ok := fitHAR(h, mrows)
		if !ok {
			return nil, fmt.Errorf("forecast: market HAR for horizon %d did not fit", h)
		}
		m.mhar[h] = mh
	}
	// GARCH per stock on its last three years of clean returns, then run
	// forward through every later session with the parameters fixed.
	m.garch = make([]GARCH, len(e.views))
	m.gH1 = make([][]float32, len(e.views))
	parallel(len(e.views), e.cfg.Workers, func(i int) {
		v := e.views[i]
		s := v.s
		from := max(lo, s.First+1)
		var xs []float64
		for t := from; t < hi; t++ {
			if !s.earnDay[t] {
				xs = append(xs, capMove(float64(s.r[t])))
			}
		}
		g := fitGARCH(xs)
		if !g.ok {
			return
		}
		m.garch[i] = g
		path := make([]float32, e.p.T())
		uncond := g.Omega / math.Max(1-g.persistence(), 1e-6)
		h := uncond
		for t := from; t < e.p.T(); t++ {
			if !s.earnDay[t] {
				x := capMove(float64(s.r[t])) - g.Mean
				neg := 0.0
				if x < 0 {
					neg = 1
				}
				h = math.Max(g.Omega+(g.Alpha+g.Gamma*neg)*x*x+g.Beta*h, varFloor)
			}
			path[t] = float32(h) // the variance expected for session t+1
		}
		m.gH1[i] = path
	})
	// The market's regimes.
	var mr []float64
	for t := max(lo, 1); t < hi; t++ {
		mr = append(mr, e.mv.r[t])
	}
	g, ok := fitRegime(mr)
	if !ok {
		return nil, errors.New("forecast: the regime model did not fit")
	}
	m.regime = g
	from := max(lo, 1)
	probs := g.Filter(e.mv.r[from:])
	m.stress = make([]float64, e.p.T())
	copy(m.stress[from:], probs)
	return m, nil
}

// icWeights is the IC-weighted composite (Grinold and Kahn): each factor
// weighted by its mean rank IC over the training window, kept in full when
// its Newey-West t is 2 or more and shrunk by the square of t/2 below
// that, so a factor whose record is noise contributes almost nothing.
func icWeights(dayIC [][]float64, h int) []float64 {
	w := make([]float64, len(dayIC))
	for f, ics := range dayIC {
		if len(ics) < 20 {
			continue
		}
		m := meanOf(ics)
		t := math.Abs(neweyWestT(ics, max(1, h/step)))
		w[f] = m * math.Min(1, (t/2)*(t/2))
	}
	return w
}

// capMove caps a non-earnings day at a 25% move, as the variance proxies
// do: a spin-off or a bad print must not set GARCH's next variance.
func capMove(x float64) float64 { return math.Max(-0.223, math.Min(0.223, x)) }

// marketX is the market HAR's regressors at t.
func marketX(mv *marketView, t int) []float64 {
	c := mv.cv
	implied := c.meanRV(t, 22)
	if v := mv.vix[t]; v > 0 {
		implied = (v / 100) * (v / 100) / 252
	}
	return []float64{1, lg(c.rv(t)), lg(c.meanRV(t, 5)), lg(c.meanRV(t, 22)), lg(c.meanR2(t, 252)), lg(implied)}
}

// fitRidge solves (XᵀX + λnI)β = Xᵀy on rows whose Y5 holds the target.
func fitRidge(rows []Row, lambda float64) (Model, error) {
	k := len(Factors) + 1
	a := make([][]float64, k)
	for i := range a {
		a[i] = make([]float64, k+1)
	}
	x := make([]float64, k)
	for _, r := range rows {
		x[0] = 1
		for j, v := range r.X {
			x[j+1] = float64(v)
		}
		y := float64(r.Y5)
		for i := 0; i < k; i++ {
			for j := 0; j < k; j++ {
				a[i][j] += x[i] * x[j]
			}
			a[i][k] += x[i] * y
		}
	}
	for i := 1; i < k; i++ {
		a[i][i] += lambda * float64(len(rows))
	}
	beta, err := solve(a)
	if err != nil {
		return Model{}, err
	}
	return Model{Intercept: beta[0], Coef: beta[1:]}, nil
}

// calib is what earlier out-of-sample years say about this model's outputs.
type calib struct {
	slope  map[int]float64 // expected abnormal return per unit of score rank
	slopeT map[int]float64
	blend  map[int]float64 // weight on HAR against GARCH
	ensW   map[int]ensembleWeights
	pit    map[int]PITMap
	up     map[int]Isotonic
	beat   map[int]Isotonic
}

func (e *engine) calibration() calib {
	c := calib{slope: map[int]float64{}, slopeT: map[int]float64{}, blend: map[int]float64{}, pit: map[int]PITMap{}, up: map[int]Isotonic{}, beat: map[int]Isotonic{}, ensW: map[int]ensembleWeights{}}
	// The ensemble leans toward whichever part ranked better in earlier
	// years, halfway: a year or two of ICs is too noisy to trust fully.
	for _, h := range []int{5, 20} {
		var rid, tre, cmp []float64
		for _, y := range e.years {
			rid, tre, cmp = append(rid, y.Alpha[h].ridge...), append(tre, y.Alpha[h].tree...), append(cmp, y.Alpha[h].comp...)
		}
		c.ensW[h] = equalWeights
		if len(rid) >= 20 {
			ic := [3]float64{math.Max(0, meanOf(rid)), math.Max(0, meanOf(tre)), math.Max(0, meanOf(cmp))}
			if sum := ic[0] + ic[1] + ic[2]; sum > 0 {
				var w ensembleWeights
				for k := range ic {
					w[k] = 0.5*ic[k]/sum + 0.5/3
				}
				c.ensW[h] = w
			}
		}
	}
	for _, h := range []int{5, 20} {
		// Fama-MacBeth: each week's cross-sectional slope of the realised
		// abnormal return on the score's rank, averaged, with Newey-West
		// errors because a 20-day outcome spans four weekly forecasts.
		if sl := e.slopeOOS[h]; len(sl) >= 20 {
			b, _ := meanSD(sl)
			t := neweyWestT(sl, h/step)
			c.slopeT[h] = t
			// Shrunk toward zero unless the evidence is strong: full weight
			// at t of 3, none at or below zero.
			c.slope[h] = b * math.Max(0, math.Min(1, t/3))
		}
		c.blend[h] = 0.5
		if e.volN[h] > 2000 {
			best := math.Inf(1)
			for k, w := range blendWeights {
				if l := e.volLoss[h][k]; l < best {
					best, c.blend[h] = l, w
				}
			}
		}
	}
	c.blend[10] = (c.blend[5] + c.blend[20]) / 2
	for _, h := range Horizons {
		c.pit[h] = newPITMap(latest(e.pitOOS[h]))
		if recs := latest(e.upOOS[h]); len(recs) > 500 {
			c.up[h] = fitIsotonic(probCols(recs))
		}
		if recs := latest(e.beatOOS[h]); len(recs) > 500 {
			c.beat[h] = fitIsotonic(probCols(recs))
		}
	}
	return c
}

// calYears is how many of the latest tested years recalibration reads.
const calYears = 2

// keepLast drops all but the last calYears years.
func keepLast[T any](years [][]T) [][]T {
	if len(years) > calYears {
		return append([][]T(nil), years[len(years)-calYears:]...)
	}
	return years
}

// latest joins the last calYears years' records.
func latest[T any](years [][]T) []T {
	var out []T
	for _, y := range years[max(0, len(years)-calYears):] {
		out = append(out, y...)
	}
	return out
}

func probCols(r []probRec) ([]float64, []float64) {
	p := make([]float64, len(r))
	o := make([]float64, len(r))
	for i, x := range r {
		p[i], o[i] = x.p, x.outcome
	}
	return p, o
}

func percentile(xs []float64, q float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return quantileSorted(s, q)
}

// scores is the ensemble's view of one session: each stock's score rank and
// the components behind it.
type scores struct {
	rank              map[int32]float64 // ensemble score as a rank in [-1, 1]
	ridge, tree, comp map[int32]float64
}

// ensembleWeights are the shares of the ridge, the trees and the IC-weighted
// composite.
type ensembleWeights [3]float64

var equalWeights = ensembleWeights{1.0 / 3, 1.0 / 3, 1.0 / 3}

func (e *engine) score(m *models, h int, day []Row, w ensembleWeights) scores {
	n := len(day)
	rp := make([]float64, n)
	gp := make([]float64, n)
	cp := make([]float64, n)
	for i, r := range day {
		rp[i] = m.ridge[h].scoreF32(r.X[:len(Factors)])
		gp[i] = m.gbdt[h].Predict(r.X)
		for f, cw := range m.comp[h] {
			cp[i] += cw * float64(r.X[f])
		}
	}
	rz, gz, cz := zscores(rp), zscores(gp), zscores(cp)
	ens := make([]float64, n)
	for i := range ens {
		ens[i] = w[0]*rz[i] + w[1]*gz[i] + w[2]*cz[i]
	}
	rk := rankScale(ens)
	s := scores{rank: map[int32]float64{}, ridge: map[int32]float64{}, tree: map[int32]float64{}, comp: map[int32]float64{}}
	rr, gr, cr := rankScale(rp), rankScale(gp), rankScale(cp)
	for i, r := range day {
		s.rank[r.Stock] = rk[i]
		s.ridge[r.Stock] = rr[i]
		s.tree[r.Stock] = gr[i]
		s.comp[r.Stock] = cr[i]
	}
	return s
}

func (m Model) scoreF32(x []float32) float64 {
	s := m.Intercept
	for i, c := range m.Coef {
		s += c * float64(x[i])
	}
	return s
}

func zscores(xs []float64) []float64 {
	m, sd := meanSD(xs)
	out := make([]float64, len(xs))
	for i, x := range xs {
		if sd > 0 {
			out[i] = (x - m) / sd
		}
	}
	return out
}

// stockVar is a stock's forecast daily diffusive variance for each day
// ahead: HAR's term structure and GARCH's decay, blended.
func (e *engine) stockVar(m *models, i int32, t int, w float64) (out [longest]float64, har5, har20, garch5, garch20 float64) {
	v := e.views[i]
	x := harX(v.cv, e.mv.cv, e.mv.vix[t], t)
	har5, har20 = m.har[5].Daily(x), m.har[20].Daily(x)
	hp := termStructure(har5, har20)
	gp := hp
	if g := m.garch[i]; g.ok && m.gH1[i] != nil && m.gH1[i][t] > 0 {
		path := g.path(float64(m.gH1[i][t]), longest)
		copy(gp[:], path)
	}
	for d := 0; d < longest; d++ {
		out[d] = w*hp[d] + (1-w)*gp[d]
	}
	garch5, garch20 = meanOf(gp[:5]), meanOf(gp[:])
	return
}

func termStructure(v5, v20 float64) [longest]float64 {
	var out [longest]float64
	later := math.Max((20*v20-5*v5)/15, 0.25*v5)
	for d := range out {
		if d < 5 {
			out[d] = v5
		} else {
			out[d] = later
		}
	}
	return out
}

func meanOf(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// marketVar is the market's forecast daily variance for each day ahead.
func (e *engine) marketVar(m *models, t int) [longest]float64 {
	x := marketX(e.mv, t)
	return termStructure(m.mhar[5].Daily(x), m.mhar[20].Daily(x))
}

// marketPool is the market's standardised shocks over the three years to t.
func (e *engine) marketPool(t int) []float64 {
	var out []float64
	for j := max(23, t-756); j <= t; j++ {
		sd := math.Sqrt(e.mv.cv.meanR2(j-1, 22))
		if sd > 0 {
			out = append(out, e.mv.r[j]/sd)
		}
	}
	return standardise(out)
}

// idioPool is a stock's standardised idiosyncratic shocks over two years to
// t, earnings days left out: those are drawn separately.
func (e *engine) idioPool(v *stockView, t int) []float64 {
	var out []float64
	for j := max(v.s.First+23, t-503); j <= t; j++ {
		if v.s.earnDay[j] {
			continue
		}
		sd := math.Sqrt(v.idioVar(j-1, 22))
		if sd > 0 {
			out = append(out, float64(v.idio[j])/sd)
		}
	}
	if len(out) < 100 {
		return nil
	}
	return standardise(out)
}

// jumps are a stock's past earnings-day abnormal moves, each divided by the
// idiosyncratic volatility before it, rescaled to today's, and demeaned:
// the jump carries the size and shape of a report, not a drift. A stock
// with fewer than eight reports on record borrows its sector's.
func (e *engine) jumps(i int32, t int, sectorPool map[string][]sectorJump) []float64 {
	v := e.views[i]
	var own []float64
	for _, ev := range v.s.Earnings {
		if ev.Session > t {
			break
		}
		if ev.Session > v.s.First+1 {
			own = append(own, float64(v.idio[ev.Session]))
		}
	}
	var out []float64
	if len(own) >= 8 {
		// The company's own reports as they moved it, the last six years of
		// them: how hard results hit a stock is a trait of the company more
		// than of this month's volatility, and rescaling by a jumpy month
		// turned a 6% typical move into 20%.
		out = append(out, own[max(0, len(own)-24):]...)
	} else {
		// Too few of its own: its sector's, each divided by the reporting
		// company's idiosyncratic volatility over the year before, and
		// rescaled to this stock's over the past year.
		now := math.Sqrt(v.idioVar(t, 252))
		if now <= 0 {
			return nil
		}
		for _, sj := range sectorPool[e.sectors[i]] {
			if sj.session > t {
				break
			}
			out = append(out, sj.std*now)
		}
		if len(out) > 200 {
			out = out[len(out)-200:]
		}
	}
	if len(out) < 4 {
		return nil
	}
	m, _ := meanSD(out)
	for k := range out {
		out[k] -= m
	}
	return out
}

type sectorJump struct {
	session int
	std     float64
}

func (e *engine) sectorJumps() map[string][]sectorJump {
	out := map[string][]sectorJump{}
	for i, v := range e.views {
		for _, ev := range v.s.Earnings {
			if ev.Session >= e.p.T() || ev.Session-61 <= v.s.First {
				continue
			}
			before := math.Sqrt(v.idioVar(ev.Session-1, 252))
			if before > 0 {
				k := e.sectors[i]
				out[k] = append(out[k], sectorJump{ev.Session, float64(v.idio[ev.Session]) / before})
			}
		}
	}
	for k := range out {
		sort.Slice(out[k], func(a, b int) bool { return out[k][a].session < out[k][b].session })
	}
	return out
}

// inputs assembles one stock's simulation on session t.
func (e *engine) inputs(m *models, c calib, sc5, sc20 scores, r Row, t int, mvar [longest]float64, sj map[string][]sectorJump, earnAt int) (stockInputs, [longest]float64, bool) {
	var in stockInputs
	v := e.views[r.Stock]
	in.pool = e.idioPool(v, t)
	if in.pool == nil {
		return in, [longest]float64{}, false
	}
	in.beta = float64(r.Beta)
	a5 := c.slope[5] * sc5.rank[r.Stock]
	a20 := c.slope[20] * sc20.rank[r.Stock]
	for d := 0; d < longest; d++ {
		if d < 5 {
			in.alpha[d] = a5 / 5
		} else {
			in.alpha[d] = (a20 - a5) / 15
		}
	}
	svar, _, _, _, _ := e.stockVar(m, r.Stock, t, c.blend[5])
	for d := 0; d < longest; d++ {
		in.idioVar[d] = math.Max(svar[d]-in.beta*in.beta*mvar[d], 0.15*svar[d])
	}
	if earnAt > t && earnAt <= t+longest {
		in.earnDay = earnAt - t
		in.jumps = e.jumps(r.Stock, t, sj)
	}
	return in, svar, true
}

// walk tests one calendar year: fit on the three years before it, forecast
// every week of it, and score each forecast against what happened.
func (e *engine) walk(year int) error {
	start := e.yearStart(year)
	end := e.yearStart(year + 1)
	e.ensureRows(max(0, start-trainYears*252), end)
	m, err := e.fit(max(0, start-trainYears*252), start-longest)
	if err != nil {
		return err
	}
	c := e.calibration()
	rec := YearRecord{Year: year, Alpha: map[int]*AlphaYear{}, Vol: map[int]*VolYear{}, Dist: map[int]*DistYear{}}
	for _, h := range []int{5, 20} {
		rec.Alpha[h] = &AlphaYear{h: h}
		rec.Vol[h] = &VolYear{}
	}
	for _, h := range Horizons {
		rec.Dist[h] = &DistYear{}
	}
	sj := e.sectorJumps()
	var prevTop, prevMomTop map[int32]bool
	var turnover, momTurnover []float64
	type distAcc struct {
		crps, raw, nrm, boot []float64 // per-date means
		z2                   []float64
		pits                 []float64
		up, beat             []probRec
		upCal, beatCal       []probRec
		in50, in80, in90     int
		cin50, cin80, cin90  int
		n                    int
	}
	acc := map[int]*distAcc{}
	for _, h := range Horizons {
		acc[h] = &distAcc{}
	}
	for _, t := range e.dates {
		if t < start || t >= end {
			continue
		}
		day := e.rows[t]
		sc := map[int]scores{5: e.score(m, 5, day, c.ensW[5]), 20: e.score(m, 20, day, c.ensW[20])}
		// Return model: rank IC of the ensemble and each part, and of
		// momentum alone, the factor a naive model collapses to.
		for _, h := range []int{5, 20} {
			var ens, rid, tre, cmp, mom, ys []float64
			for _, r := range day {
				y := r.Y5
				if h == 20 {
					y = r.Y20
				}
				if isNaN32(y) {
					continue
				}
				ens = append(ens, sc[h].rank[r.Stock])
				rid = append(rid, sc[h].ridge[r.Stock])
				tre = append(tre, sc[h].tree[r.Stock])
				cmp = append(cmp, sc[h].comp[r.Stock])
				mom = append(mom, float64(r.X[0]))
				ys = append(ys, float64(y))
			}
			if len(ys) < 30 {
				continue
			}
			e.slopeOOS[h] = append(e.slopeOOS[h], xsSlope(ens, ys))
			yr := rankScale(ys)
			a := rec.Alpha[h]
			a.ens = append(a.ens, pearson(ens, yr))
			a.ridge = append(a.ridge, pearson(rid, yr))
			a.tree = append(a.tree, pearson(tre, yr))
			a.comp = append(a.comp, pearson(cmp, yr))
			a.mom = append(a.mom, pearson(mom, yr))
			// Each stock factor's own rank IC over 20 sessions: which of
			// the published effects held up out of sample.
			if h == 20 {
				xs := make([]float64, 0, len(ys))
				for f := range Factors {
					xs = xs[:0]
					for _, r := range day {
						if !isNaN32(r.Y20) {
							xs = append(xs, float64(r.X[f]))
						}
					}
					e.factorIC[f] = append(e.factorIC[f], pearson(xs, yr))
				}
			}
			top, bot := decileMeans(ens, ys)
			a.spread = append(a.spread, (top-bot)*100)
		}
		// How much the top tenth changes from one week to the next.
		top, momTop := topDecile(day, sc[5].rank, func(r Row) float64 { return float64(r.X[0]) })
		if prevTop != nil {
			turnover = append(turnover, changed(prevTop, top))
			momTurnover = append(momTurnover, changed(prevMomTop, momTop))
		}
		prevTop, prevMomTop = top, momTop

		// Volatility, for every stock: HAR, GARCH, the blend and the naive
		// forecast that next month looks like last month.
		for _, r := range day {
			v := e.views[r.Stock]
			_, h5, h20, g5, g20 := e.stockVar(m, r.Stock, t, 0.5)
			naive := v.cv.meanR2(t, 22)
			for _, hz := range []struct {
				h      int
				har, g float64
			}{{5, h5, g5}, {20, h20, g20}} {
				if t+hz.h >= e.p.T() {
					continue
				}
				real := v.cv.future(t, hz.h)
				vy := rec.Vol[hz.h]
				w := c.blend[hz.h]
				lr := math.Log(math.Max(real, varFloor))
				vy.add([7]float64{qlike(hz.har, real), qlike(hz.g, real), qlike(w*hz.har+(1-w)*hz.g, real), qlike(naive, real),
					math.Log(math.Max(hz.har, varFloor)) - lr, math.Log(math.Max(hz.g, varFloor)) - lr, math.Log(math.Max(naive, varFloor)) - lr})
				for k, bw := range blendWeights {
					e.volLoss[hz.h][k] += qlike(bw*hz.har+(1-bw)*hz.g, real)
				}
				e.volN[hz.h]++
			}
		}

		// The full distribution, for the evaluation sample.
		mvar := e.marketVar(m, t)
		pool := e.marketPool(t)
		if len(pool) < 200 {
			continue
		}
		rng := rand.New(rand.NewSource(int64(t)*7919 + int64(year)))
		mkt := simMarket(rng, m.regime, m.stress[t], mvar[:], pool, e.cfg.EvalPaths)
		type out struct {
			crps, raw, nrm, boot map[int]float64
			z2                   map[int]float64
			pit                  map[int]float64
			up, beat             map[int]probRec
		}
		var sample []Row
		for _, r := range day {
			if e.evalSet[r.Stock] {
				sample = append(sample, r)
			}
		}
		results := make([]*out, len(sample))
		parallel(len(sample), e.cfg.Workers, func(k int) {
			r := sample[k]
			v := e.views[r.Stock]
			in, _, ok := e.inputs(m, c, sc[5], sc[20], r, t, mvar, sj, v.s.nextEarnings(t))
			if !ok {
				return
			}
			lr := rand.New(rand.NewSource(int64(t)*31 + int64(r.Stock)))
			sim := simStock(lr, in, mkt, false)
			o := &out{crps: map[int]float64{}, raw: map[int]float64{}, z2: map[int]float64{}, nrm: map[int]float64{}, boot: map[int]float64{}, pit: map[int]float64{}, up: map[int]probRec{}, beat: map[int]probRec{}}
			var raw []float64
			for j := max(v.s.First+1, t-59); j <= t; j++ {
				raw = append(raw, float64(v.s.r[j]))
			}
			_, sd60 := meanSD(raw)
			for _, h := range Horizons {
				if t+h >= e.p.T() {
					continue
				}
				var y, ym float64
				for j := t + 1; j <= t+h; j++ {
					y += float64(v.s.r[j])
					ym += e.mv.r[j]
				}
				xs := sim.stock[h]
				// Scored as served: recalibrated on earlier years when
				// there are any, raw otherwise.
				o.crps[h] = crpsSorted(served(xs, c.pit[h]), y)
				o.raw[h] = crpsSorted(xs, y)
				if mu, sd := meanSD(xs); sd > 0 {
					z := (y - mu) / sd
					o.z2[h] = z * z
				}
				o.pit[h] = pitSorted(xs, y)
				o.nrm[h] = crpsNormal(0, sd60*math.Sqrt(float64(h)), y)
				var hist []float64
				for j := max(v.s.First+h, t-252+h); j <= t; j++ {
					var s float64
					for q := j - h + 1; q <= j; q++ {
						s += float64(v.s.r[q])
					}
					hist = append(hist, s)
				}
				sort.Float64s(hist)
				if len(hist) > 50 {
					o.boot[h] = crpsSorted(hist, y)
				}
				o.up[h] = probRec{sim.pUp[h], b2f(y > 0)}
				o.beat[h] = probRec{sim.pBeat[h], b2f(y > ym)}
			}
			results[k] = o
		})
		for _, h := range Horizons {
			a := acc[h]
			var cs, rs, ns, bs []float64
			for _, o := range results {
				if o == nil {
					continue
				}
				if _, ok := o.crps[h]; !ok {
					continue
				}
				cs = append(cs, o.crps[h])
				rs = append(rs, o.raw[h])
				ns = append(ns, o.nrm[h])
				if b, ok := o.boot[h]; ok {
					bs = append(bs, b)
				} else {
					bs = append(bs, o.nrm[h])
				}
				u := o.pit[h]
				a.pits = append(a.pits, u)
				a.z2 = append(a.z2, o.z2[h])
				a.n++
				if u >= 0.25 && u <= 0.75 {
					a.in50++
				}
				if u >= 0.1 && u <= 0.9 {
					a.in80++
				}
				if u >= 0.05 && u <= 0.95 {
					a.in90++
				}
				pm := c.pit[h]
				if u >= pm.Level(0.25) && u <= pm.Level(0.75) {
					a.cin50++
				}
				if u >= pm.Level(0.1) && u <= pm.Level(0.9) {
					a.cin80++
				}
				if u >= pm.Level(0.05) && u <= pm.Level(0.95) {
					a.cin90++
				}
				a.up = append(a.up, o.up[h])
				a.beat = append(a.beat, o.beat[h])
				a.upCal = append(a.upCal, probRec{c.up[h].Apply(o.up[h].p), o.up[h].outcome})
				a.beatCal = append(a.beatCal, probRec{c.beat[h].Apply(o.beat[h].p), o.beat[h].outcome})
			}
			if len(cs) > 0 {
				a.crps = append(a.crps, meanOf(cs))
				a.raw = append(a.raw, meanOf(rs))
				a.nrm = append(a.nrm, meanOf(ns))
				a.boot = append(a.boot, meanOf(bs))
			}
		}
	}
	// Close the year.
	for _, h := range []int{5, 20} {
		rec.Alpha[h].close()
		rec.Vol[h].close(c.blend[h])
	}
	rec.Turnover = round(meanOf0(turnover) * 100)
	rec.MomentumTurnover = round(meanOf0(momTurnover) * 100)
	for _, h := range Horizons {
		a := acc[h]
		d := rec.Dist[h]
		d.raw = a.raw
		d.fill(a.crps, a.nrm, a.boot, a.pits, a.z2, a.n, [3]int{a.in50, a.in80, a.in90}, [3]int{a.cin50, a.cin80, a.cin90}, c.pit[h].N > 0, a.up, a.beat, a.upCal, a.beatCal, h)
		// Only the years recalibration reads are kept.
		e.pitOOS[h] = keepLast(append(e.pitOOS[h], a.pits))
		e.upOOS[h] = keepLast(append(e.upOOS[h], a.up))
		e.beatOOS[h] = keepLast(append(e.beatOOS[h], a.beat))
	}
	rec.TrainRows = m.trained
	e.years = append(e.years, rec)
	return nil
}

// served is the ensemble a forecast is published as: each member moved to
// the quantile level that earlier years showed truly holds its share of
// outcomes.
func served(xs []float64, pm PITMap) []float64 {
	if len(pm.PITs) < 200 {
		return xs
	}
	n := len(xs)
	out := make([]float64, n)
	for k := range out {
		out[k] = quantileSorted(xs, pm.Level((float64(k)+0.5)/float64(n)))
	}
	return out
}

// xsSlope is one day's least-squares slope of returns (winsorised at the
// 1st and 99th percentiles) on score ranks, which have mean zero.
func xsSlope(rank, y []float64) float64 {
	lo, hi := percentile(y, 0.01), percentile(y, 0.99)
	var sxy, sxx float64
	for i := range rank {
		v := math.Max(lo, math.Min(hi, y[i]))
		sxy += rank[i] * v
		sxx += rank[i] * rank[i]
	}
	if sxx == 0 {
		return 0
	}
	return sxy / sxx
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func meanOf0(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	return meanOf(xs)
}

func decileMeans(score, y []float64) (top, bottom float64) {
	idx := make([]int, len(score))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return score[idx[a]] > score[idx[b]] })
	n := max(1, len(idx)/10)
	for _, i := range idx[:n] {
		top += y[i]
	}
	for _, i := range idx[len(idx)-n:] {
		bottom += y[i]
	}
	return top / float64(n), bottom / float64(n)
}

func topDecile(day []Row, rank map[int32]float64, mom func(Row) float64) (map[int32]bool, map[int32]bool) {
	n := max(1, len(day)/10)
	byScore := append([]Row(nil), day...)
	sort.Slice(byScore, func(a, b int) bool { return rank[byScore[a].Stock] > rank[byScore[b].Stock] })
	top := map[int32]bool{}
	for _, r := range byScore[:n] {
		top[r.Stock] = true
	}
	sort.Slice(byScore, func(a, b int) bool { return mom(byScore[a]) > mom(byScore[b]) })
	mt := map[int32]bool{}
	for _, r := range byScore[:n] {
		mt[r.Stock] = true
	}
	return top, mt
}

func changed(prev, cur map[int32]bool) float64 {
	if len(cur) == 0 {
		return 0
	}
	n := 0
	for k := range cur {
		if !prev[k] {
			n++
		}
	}
	return float64(n) / float64(len(cur))
}

func pearson(x, y []float64) float64 {
	mx, _ := meanSD(x)
	my, _ := meanSD(y)
	var sxy, sxx, syy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0
	}
	return sxy / math.Sqrt(sxx*syy)
}
