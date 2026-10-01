package forecast

import (
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// QuantileLevels are the levels every forecast distribution reports.
var QuantileLevels = []float64{0.05, 0.1, 0.15, 0.2, 0.25, 0.3, 0.35, 0.4, 0.45, 0.5, 0.55, 0.6, 0.65, 0.7, 0.75, 0.8, 0.85, 0.9, 0.95}

// Report is one run: how every part of the engine did out of sample, the
// calibration it carries forward, and the market's state today.
type Report struct {
	Version    int       `json:"version"`
	At         time.Time `json:"at"`
	AsOf       time.Time `json:"as_of"`
	Horizon    int       `json:"horizon"`
	Horizons   []int     `json:"horizons"`
	Symbols    int       `json:"symbols"`
	TrainRows  int       `json:"train_rows"`
	Paths      int       `json:"paths"`
	EvalPaths  int       `json:"eval_paths"`
	EvalStocks int       `json:"eval_stocks"`

	Years []YearRecord `json:"years"`
	// Alpha, Vol and Dist pool every tested year.
	Alpha map[int]AlphaYear `json:"alpha"`
	Vol   map[int]VolYear   `json:"vol"`
	Dist  map[int]DistYear  `json:"dist"`
	// Turnover is the share of the top tenth replaced each week; for
	// momentum alone, the comparison.
	Turnover         float64 `json:"turnover_pct"`
	MomentumTurnover float64 `json:"momentum_turnover_pct"`

	Calibration CalibInfo       `json:"calibration"`
	Regime      RegimeNow       `json:"regime"`
	Market      []HorizonDist   `json:"market"`
	Factors     []FactorInfo    `json:"factors"`
	HAR         map[int]HAR     `json:"har"`
	Reliability map[int]RelInfo `json:"reliability"`
}

// YearRecord is one calendar year tested on models fit only before it.
type YearRecord struct {
	Year             int                `json:"year"`
	TrainRows        int                `json:"train_rows"`
	Alpha            map[int]*AlphaYear `json:"alpha"`
	Vol              map[int]*VolYear   `json:"vol"`
	Dist             map[int]*DistYear  `json:"dist"`
	Turnover         float64            `json:"turnover_pct"`
	MomentumTurnover float64            `json:"momentum_turnover_pct"`
}

// AlphaYear is the return model's ranking skill.
type AlphaYear struct {
	Days int `json:"days"`
	// IC is the mean weekly rank correlation between the score and the
	// abnormal return that followed; T its t statistic with Newey-West
	// errors. RidgeIC, TreeIC and MomentumIC are the parts and the naive
	// alternative.
	IC         float64 `json:"ic"`
	T          float64 `json:"t"`
	RidgeIC    float64 `json:"ridge_ic"`
	TreeIC     float64 `json:"tree_ic"`
	CompIC     float64 `json:"composite_ic"`
	MomentumIC float64 `json:"momentum_ic"`
	SpreadPct  float64 `json:"spread_pct"`

	ens, ridge, tree, comp, mom, spread []float64
	h                                   int
}

func (a *AlphaYear) close() {
	a.Days = len(a.ens)
	a.IC = round4(meanOf0(a.ens))
	a.T = round(neweyWestT(a.ens, max(1, a.h/step)))
	a.RidgeIC = round4(meanOf0(a.ridge))
	a.TreeIC = round4(meanOf0(a.tree))
	a.CompIC = round4(meanOf0(a.comp))
	a.MomentumIC = round4(meanOf0(a.mom))
	a.SpreadPct = round(meanOf0(a.spread))
}

// VolYear is the volatility models' QLIKE loss (lower is better) against
// the realised variance that followed.
type VolYear struct {
	N     int     `json:"n"`
	HAR   float64 `json:"har"`
	GARCH float64 `json:"garch"`
	Blend float64 `json:"blend"`
	Naive float64 `json:"naive"`
	// Weight is the share on HAR the blend used, chosen on earlier years.
	Weight float64 `json:"weight"`
	// Skill is the blend's loss reduction against the naive forecast.
	Skill float64 `json:"skill_pct"`
	// Bias is each forecast's mean log ratio to the variance that followed:
	// above zero it over-predicts, below it under-predicts.
	BiasHAR   float64 `json:"bias_har"`
	BiasGARCH float64 `json:"bias_garch"`
	BiasNaive float64 `json:"bias_naive"`

	// Running sums of the losses (HAR, GARCH, blend, naive) and the log
	// biases (HAR, GARCH, naive), so a year costs seven numbers, not seven
	// per stock and week.
	sums [7]float64
	cnt  int
}

func (v *VolYear) add(x [7]float64) {
	for i := range x {
		v.sums[i] += x[i]
	}
	v.cnt++
}

func (v *VolYear) merge(o *VolYear) {
	for i := range o.sums {
		v.sums[i] += o.sums[i]
	}
	v.cnt += o.cnt
}

func (v *VolYear) close(w float64) {
	v.N = v.cnt
	m := func(i int) float64 {
		if v.cnt == 0 {
			return 0
		}
		return round4(v.sums[i] / float64(v.cnt))
	}
	v.HAR, v.GARCH, v.Blend, v.Naive = m(0), m(1), m(2), m(3)
	v.BiasHAR, v.BiasGARCH, v.BiasNaive = m(4), m(5), m(6)
	v.Weight = w
	if v.Naive > 0 {
		v.Skill = round((1 - v.Blend/v.Naive) * 100)
	}
}

// DistYear is how the full forecast distribution scored.
type DistYear struct {
	N int `json:"n"`
	// CRPS is in log-return units; the baselines are a normal curve with
	// the last 60 days' volatility, and the stock's own past year of
	// returns over the same horizon.
	CRPS       float64 `json:"crps"`
	CRPSRaw    float64 `json:"crps_raw"`
	CRPSNormal float64 `json:"crps_normal"`
	CRPSBoot   float64 `json:"crps_history"`
	// Skill is the percentage reduction in CRPS; DMT is the Diebold-Mariano
	// t statistic of the weekly difference.
	SkillNormal float64 `json:"skill_normal_pct"`
	SkillBoot   float64 `json:"skill_history_pct"`
	DMNormalT   float64 `json:"dm_normal_t"`
	DMBootT     float64 `json:"dm_history_t"`
	// Coverage is the share of outcomes inside the central 50, 80 and 90%
	// intervals as forecast, then after recalibration on earlier years.
	Coverage    [3]float64 `json:"coverage"`
	CalCoverage [3]float64 `json:"calibrated_coverage"`
	Calibrated  bool       `json:"calibrated"`
	PIT         []float64  `json:"pit"`
	// ZVar is the mean square of each outcome divided by its forecast's
	// standard deviation: 1 when the forecast widths are right, above 1
	// when they are too narrow, below when too wide.
	ZVar float64 `json:"z_var"`
	// Brier scores: forecast, recalibrated, and the base rate's.
	BrierUp       float64 `json:"brier_up"`
	BrierUpCal    float64 `json:"brier_up_calibrated"`
	BrierUpClim   float64 `json:"brier_up_base_rate"`
	BrierBeat     float64 `json:"brier_beat"`
	BrierBeatCal  float64 `json:"brier_beat_calibrated"`
	BrierBeatClim float64 `json:"brier_beat_base_rate"`

	// Per-week mean CRPS of the forecast as served, raw, and the two
	// baselines, for the Diebold-Mariano tests; everything else is kept
	// as counts and sums.
	crps, raw, nrm, boot     []float64
	pitBins                  [10]float64
	z2                       float64
	up, beat, upCal, beatCal probStats
	in, cin                  [3]int
	h                        int
}

// probStats accumulates probability forecasts against what happened.
type probStats struct {
	sq, o, n float64
	bins     [10][3]float64 // per tenth: sum of forecasts, of outcomes, count
}

func (s *probStats) add(p, o float64) {
	s.sq += (p - o) * (p - o)
	s.o += o
	s.n++
	b := min(9, max(0, int(p*10)))
	s.bins[b][0] += p
	s.bins[b][1] += o
	s.bins[b][2]++
}

func (s *probStats) merge(x probStats) {
	s.sq, s.o, s.n = s.sq+x.sq, s.o+x.o, s.n+x.n
	for b := range s.bins {
		for k := 0; k < 3; k++ {
			s.bins[b][k] += x.bins[b][k]
		}
	}
}

// brier is the Brier score of the forecasts and of always forecasting
// their base rate.
func (s probStats) brier() (float64, float64) {
	if s.n == 0 {
		return 0, 0
	}
	base := s.o / s.n
	return round6(s.sq / s.n), round6(base * (1 - base))
}

func (s probStats) reliability() []Bin {
	var out []Bin
	for _, b := range s.bins {
		if b[2] == 0 {
			continue
		}
		out = append(out, Bin{Forecast: round4(b[0] / b[2]), Observed: round4(b[1] / b[2]), N: int(b[2])})
	}
	return out
}

func (d *DistYear) fill(crps, nrm, boot, pits, z2 []float64, n int, in, cin [3]int, calibrated bool, up, beat, upCal, beatCal []probRec, h int) {
	d.crps, d.nrm, d.boot = crps, nrm, boot
	for _, u := range pits {
		d.pitBins[min(9, max(0, int(u*10)))]++
	}
	for _, z := range z2 {
		d.z2 += z
	}
	for i := range up {
		d.up.add(up[i].p, up[i].outcome)
		d.beat.add(beat[i].p, beat[i].outcome)
		d.upCal.add(upCal[i].p, upCal[i].outcome)
		d.beatCal.add(beatCal[i].p, beatCal[i].outcome)
	}
	d.in, d.cin, d.N, d.Calibrated, d.h = in, cin, n, calibrated, h
	d.summarise()
}

func (d *DistYear) merge(o *DistYear) {
	d.crps, d.raw, d.nrm, d.boot = append(d.crps, o.crps...), append(d.raw, o.raw...), append(d.nrm, o.nrm...), append(d.boot, o.boot...)
	for i := range d.pitBins {
		d.pitBins[i] += o.pitBins[i]
	}
	d.z2 += o.z2
	d.up.merge(o.up)
	d.beat.merge(o.beat)
	d.upCal.merge(o.upCal)
	d.beatCal.merge(o.beatCal)
	for k := 0; k < 3; k++ {
		d.in[k] += o.in[k]
		if o.Calibrated {
			d.cin[k] += o.cin[k]
		}
	}
	d.N += o.N
	d.Calibrated = d.Calibrated || o.Calibrated
}

func (d *DistYear) summarise() {
	if d.N == 0 {
		return
	}
	d.CRPS, d.CRPSRaw, d.CRPSNormal, d.CRPSBoot = round6(meanOf0(d.crps)), round6(meanOf0(d.raw)), round6(meanOf0(d.nrm)), round6(meanOf0(d.boot))
	if d.CRPSNormal > 0 {
		d.SkillNormal = round((1 - d.CRPS/d.CRPSNormal) * 100)
	}
	if d.CRPSBoot > 0 {
		d.SkillBoot = round((1 - d.CRPS/d.CRPSBoot) * 100)
	}
	lags := max(1, d.h/step)
	dn := make([]float64, len(d.crps))
	db := make([]float64, len(d.crps))
	for i := range d.crps {
		dn[i] = d.nrm[i] - d.crps[i]
		db[i] = d.boot[i] - d.crps[i]
	}
	d.DMNormalT, d.DMBootT = round(neweyWestT(dn, lags)), round(neweyWestT(db, lags))
	for k := 0; k < 3; k++ {
		d.Coverage[k] = round(float64(d.in[k]) / float64(d.N) * 100)
		d.CalCoverage[k] = round(float64(d.cin[k]) / float64(d.N) * 100)
	}
	d.PIT = make([]float64, 10)
	for i, c := range d.pitBins {
		d.PIT[i] = round4(c / float64(d.N))
	}
	d.ZVar = round4(d.z2 / float64(d.N))
	d.BrierUp, d.BrierUpClim = d.up.brier()
	d.BrierUpCal, _ = d.upCal.brier()
	d.BrierBeat, d.BrierBeatClim = d.beat.brier()
	d.BrierBeatCal, _ = d.beatCal.brier()
}

func round6(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*1e6) / 1e6
}

// CalibInfo is what earlier years taught the engine about its own outputs.
type CalibInfo struct {
	// SlopeBps is the expected abnormal return, in basis points, between a
	// middling stock and the top-ranked one; SlopeT its Fama-MacBeth t.
	SlopeBps map[int]float64 `json:"slope_bps"`
	SlopeT   map[int]float64 `json:"slope_t"`
	// Ensemble is the shares of the ridge, the trees and the IC-weighted
	// composite in the ranking.
	Ensemble map[int][3]float64 `json:"ensemble_weight"`
	Blend    map[int]float64    `json:"har_weight"`
	PITN     map[int]int        `json:"pit_n"`
}

// RegimeNow is the market's state on the last session.
type RegimeNow struct {
	StressProb   float64 `json:"stress_prob"`
	CalmVolPct   float64 `json:"calm_vol_pct"`
	StressVolPct float64 `json:"stress_vol_pct"`
	// StayCalm and StayStressed are the daily odds of remaining.
	StayCalm     float64 `json:"stay_calm"`
	StayStressed float64 `json:"stay_stressed"`
	VIX          float64 `json:"vix"`
	// VolForecastPct is the market's annualised volatility forecast over
	// each horizon.
	VolForecastPct map[int]float64 `json:"vol_forecast_pct"`
	RealisedVolPct float64         `json:"realised_vol_pct"`
}

// RelInfo are reliability diagrams pooled over every tested year.
type RelInfo struct {
	Up   []Bin `json:"up"`
	Beat []Bin `json:"beat"`
}

// FactorInfo is one factor's weight in the live models.
type FactorInfo struct {
	Factor
	Regime bool `json:"regime,omitempty"`
	// RidgeWeight is the linear model's coefficient on the factor's rank;
	// TreeShare its share of the trees' split gain.
	RidgeWeight float64 `json:"ridge_weight"`
	TreeShare   float64 `json:"tree_share_pct"`
	// IC and T are the factor's own rank correlation with the next 20
	// sessions' abnormal return, out of sample, and its Newey-West t.
	IC float64 `json:"ic"`
	T  float64 `json:"t"`
}

// Driver is one factor's pull on a stock's score.
type Driver struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	Contribution float64 `json:"contribution"`
}

// Prediction is one stock's forecast on the latest session.
type Prediction struct {
	Symbol string  `json:"symbol"`
	Score  float64 `json:"score"`
	// Percentile is where the five-session score ranks: 100 is the most
	// likely to outperform.
	Percentile float64       `json:"percentile"`
	Drivers    []Driver      `json:"drivers"`
	Dist       *Distribution `json:"dist,omitempty"`
}

// Distribution is a stock's simulated return distribution.
type Distribution struct {
	Horizons []HorizonDist `json:"horizons"`
	// Fan holds, for each of the next 20 sessions, the cumulative return at
	// FanLevels.
	Fan       [][]float64 `json:"fan"`
	FanLevels []float64   `json:"fan_levels"`
	Beta      float64     `json:"beta"`
	// VolPct is the annualised volatility forecast for the next month,
	// earnings excluded; NormalVolPct the stock's past year.
	VolPct       float64        `json:"vol_pct"`
	NormalVolPct float64        `json:"normal_vol_pct"`
	Earnings     *EarningsAhead `json:"earnings,omitempty"`
	Percentile20 float64        `json:"percentile_20"`
}

// HorizonDist is the forecast over one horizon, as simple returns.
type HorizonDist struct {
	Horizon   int       `json:"h"`
	Quantiles []float64 `json:"q"`
	Expected  float64   `json:"expected"`
	// Alpha is the expected return against beta times the market.
	Alpha    float64 `json:"alpha"`
	PUp      float64 `json:"p_up"`
	PBeat    float64 `json:"p_beat"`
	PUpRaw   float64 `json:"p_up_raw"`
	PBeatRaw float64 `json:"p_beat_raw"`
	// ES5 is the average of the worst 5% of outcomes.
	ES5   float64 `json:"es5"`
	Sigma float64 `json:"sigma"`
}

// EarningsAhead is a report inside the window.
type EarningsAhead struct {
	Session int `json:"session"`
	// TypicalMovePct is the standard deviation of the simulated jump.
	TypicalMovePct float64 `json:"typical_move_pct"`
	FromCalendar   bool    `json:"from_calendar"`
}

// live fits on the latest three years, carries every year's calibration
// forward, and forecasts each stock on the last session.
func (e *engine) live() (Report, []Prediction, error) {
	T := e.p.T()
	last := T - 1
	rep := Report{Version: 2, At: e.cfg.Now, AsOf: e.p.Days[last], Horizon: 5, Horizons: Horizons,
		Paths: e.cfg.Paths, EvalPaths: e.cfg.EvalPaths, EvalStocks: len(e.evalSet)}
	e.ensureRows(max(0, last-trainYears*252), T)
	m, err := e.fit(max(0, last-trainYears*252), T)
	if err != nil {
		return rep, nil, err
	}
	c := e.calibration()
	day := e.rows[last]
	if len(day) == 0 {
		return rep, nil, errErr("forecast: no stock has factors on the last session")
	}
	rep.TrainRows = m.trained
	rep.Symbols = len(day)
	e.summarise(&rep, c)
	rep.HAR = m.har

	sc5, sc20 := e.score(m, 5, day, c.ensW[5]), e.score(m, 20, day, c.ensW[20])
	mvar := e.marketVar(m, last)
	pool := e.marketPool(last)
	rng := rand.New(rand.NewSource(int64(last)))
	mkt := simMarket(rng, m.regime, m.stress[last], mvar[:], pool, e.cfg.Paths)
	rep.Regime = e.regimeNow(m, last, mvar)
	rep.Market = marketDist(mkt)
	rep.Factors = factorInfo(m, e.factorIC)

	sj := e.sectorJumps()
	// Drivers: the ridge's and the composite's linear contributions and the
	// trees' path contributions, each in units of its own cross-sectional
	// spread and weighted as in the ensemble.
	rp := make([]float64, len(day))
	gp := make([]float64, len(day))
	cp := make([]float64, len(day))
	for i, r := range day {
		rp[i] = m.ridge[5].scoreF32(r.X[:len(Factors)])
		gp[i] = m.gbdt[5].Predict(r.X)
		for f, cw := range m.comp[5] {
			cp[i] += cw * float64(r.X[f])
		}
	}
	_, rsd := meanSD(rp)
	_, gsd := meanSD(gp)
	_, csd := meanSD(cp)

	preds := make([]Prediction, len(day))
	parallel(len(day), e.cfg.Workers, func(k int) {
		r := day[k]
		v := e.views[r.Stock]
		sym := v.s.Symbol
		p := Prediction{Symbol: sym, Score: round4(sc5.rank[r.Stock]), Percentile: round((sc5.rank[r.Stock] + 1) * 50)}
		p.Drivers = drivers(m, r.X, [3]float64{rsd, gsd, csd}, c.ensW[5])
		earnAt, fromCal := v.s.nextEarnings(last), false
		if earnAt < 0 {
			if d, ok := e.cfg.Upcoming[sym]; ok {
				earnAt, fromCal = last+sessionsAhead(e.p.Days[last], d, afterClose(v.s)), true
			}
		}
		in, svar, ok := e.inputs(m, c, sc5, sc20, r, last, mvar, sj, earnAt)
		if !ok {
			preds[k] = p
			return
		}
		lr := rand.New(rand.NewSource(int64(last)*31 + int64(r.Stock)))
		sim := simStock(lr, in, mkt, true)
		dist := &Distribution{Beta: round(in.beta), FanLevels: fanLevels, Percentile20: round((sc20.rank[r.Stock] + 1) * 50)}
		var sv float64
		for _, x := range svar {
			sv += x
		}
		dist.VolPct = round(math.Sqrt(sv/longest*252) * 100)
		dist.NormalVolPct = round(math.Sqrt(v.cv.meanR2(last, 252)*252) * 100)
		if in.earnDay > 0 && len(in.jumps) > 0 {
			_, jsd := meanSD(in.jumps)
			dist.Earnings = &EarningsAhead{Session: in.earnDay, TypicalMovePct: round(jsd * 100), FromCalendar: fromCal}
		}
		for _, h := range Horizons {
			xs := sim.stock[h]
			pm := c.pit[h]
			hd := HorizonDist{Horizon: h, Expected: round6(sim.mean[h]),
				PUpRaw: round4(sim.pUp[h]), PBeatRaw: round4(sim.pBeat[h]),
				PUp: round4(c.up[h].Apply(sim.pUp[h])), PBeat: round4(c.beat[h].Apply(sim.pBeat[h]))}
			for _, q := range QuantileLevels {
				hd.Quantiles = append(hd.Quantiles, round6(math.Exp(quantileSorted(xs, pm.Level(q)))-1))
			}
			var a float64
			for d := 0; d < h; d++ {
				a += in.alpha[d]
			}
			hd.Alpha = round6(a)
			worst := xs[:max(1, len(xs)/20)]
			var es float64
			for _, x := range worst {
				es += math.Exp(x) - 1
			}
			hd.ES5 = round6(es / float64(len(worst)))
			_, sd := meanSD(xs)
			hd.Sigma = round6(sd)
			dist.Horizons = append(dist.Horizons, hd)
		}
		for d, row := range sim.fan {
			out := make([]float64, len(row))
			for i, x := range row {
				out[i] = round6(math.Exp(x) - 1)
			}
			_ = d
			dist.Fan = append(dist.Fan, out)
		}
		p.Dist = dist
		preds[k] = p
	})
	sort.Slice(preds, func(a, b int) bool { return preds[a].Score > preds[b].Score })
	return rep, preds, nil
}

type errErr string

func (e errErr) Error() string { return string(e) }

// drivers are a stock's three largest pulls on its five-session score.
func drivers(m *models, x []float32, sd [3]float64, w ensembleWeights) []Driver {
	n := len(Factors) + len(RegimeFactors)
	contrib := make([]float64, n)
	if sd[0] > 0 {
		for f, cf := range m.ridge[5].Coef {
			contrib[f] += w[0] * cf * float64(x[f]) / sd[0]
		}
	}
	if sd[1] > 0 {
		for f, v := range m.gbdt[5].contributions(x) {
			contrib[f] += w[1] * v / sd[1]
		}
	}
	if sd[2] > 0 {
		for f, cw := range m.comp[5] {
			contrib[f] += w[2] * cw * float64(x[f]) / sd[2]
		}
	}
	out := make([]Driver, 0, n)
	for f, v := range contrib {
		fac := factorAt(f)
		out = append(out, Driver{Key: fac.Key, Label: fac.Label, Contribution: round4(v)})
	}
	sort.Slice(out, func(a, b int) bool { return math.Abs(out[a].Contribution) > math.Abs(out[b].Contribution) })
	return out[:3]
}

func factorAt(f int) Factor {
	if f < len(Factors) {
		return Factors[f]
	}
	return RegimeFactors[f-len(Factors)]
}

func factorInfo(m *models, ics [][]float64) []FactorInfo {
	g := m.gbdt[5].Gain
	var total float64
	for _, v := range g {
		total += v
	}
	var out []FactorInfo
	for f := 0; f < len(Factors)+len(RegimeFactors); f++ {
		fi := FactorInfo{Factor: factorAt(f), Regime: f >= len(Factors)}
		if f < len(Factors) {
			fi.RidgeWeight = round4(m.ridge[5].Coef[f])
			if f < len(ics) && len(ics[f]) > 0 {
				fi.IC, fi.T = round4(meanOf(ics[f])), round(neweyWestT(ics[f], 20/step))
			}
		}
		if total > 0 && f < len(g) {
			fi.TreeShare = round(g[f] / total * 100)
		}
		out = append(out, fi)
	}
	return out
}

func (e *engine) regimeNow(m *models, t int, mvar [longest]float64) RegimeNow {
	g := m.regime
	r := RegimeNow{StressProb: round4(m.stress[t]),
		CalmVolPct: round(g.Sigma[0] * math.Sqrt(252) * 100), StressVolPct: round(g.Sigma[1] * math.Sqrt(252) * 100),
		StayCalm: round4(g.A[0][0]), StayStressed: round4(g.A[1][1]),
		VIX: round(e.mv.vix[t]), VolForecastPct: map[int]float64{},
		RealisedVolPct: round(math.Sqrt(e.mv.cv.meanR2(t, 22)*252) * 100)}
	for _, h := range Horizons {
		r.VolForecastPct[h] = round(math.Sqrt(meanOf(mvar[:h])*252) * 100)
	}
	return r
}

func marketDist(mkt marketPaths) []HorizonDist {
	var out []HorizonDist
	for _, h := range Horizons {
		xs := make([]float64, len(mkt))
		up := 0
		for p, path := range mkt {
			var s float64
			for d := 0; d < h; d++ {
				s += path[d]
			}
			xs[p] = s
			if s > 0 {
				up++
			}
		}
		sort.Float64s(xs)
		hd := HorizonDist{Horizon: h, PUp: round4(float64(up) / float64(len(xs))), PUpRaw: round4(float64(up) / float64(len(xs)))}
		var mean float64
		for _, x := range xs {
			mean += math.Exp(x) - 1
		}
		hd.Expected = round6(mean / float64(len(xs)))
		for _, q := range QuantileLevels {
			hd.Quantiles = append(hd.Quantiles, round6(math.Exp(quantileSorted(xs, q))-1))
		}
		_, sd := meanSD(xs)
		hd.Sigma = round6(sd)
		out = append(out, hd)
	}
	return out
}

// summarise pools every tested year into the report's headline record.
func (e *engine) summarise(rep *Report, c calib) {
	rep.Years = e.years
	rep.Alpha, rep.Vol, rep.Dist = map[int]AlphaYear{}, map[int]VolYear{}, map[int]DistYear{}
	rep.Reliability = map[int]RelInfo{}
	var turn, mturn []float64
	for _, h := range []int{5, 20} {
		a := AlphaYear{h: h}
		v := VolYear{}
		for _, y := range e.years {
			ay := y.Alpha[h]
			a.ens, a.ridge, a.tree, a.mom, a.spread = append(a.ens, ay.ens...), append(a.ridge, ay.ridge...), append(a.tree, ay.tree...), append(a.mom, ay.mom...), append(a.spread, ay.spread...)
			a.comp = append(a.comp, ay.comp...)
			v.merge(y.Vol[h])
		}
		a.close()
		v.close(c.blend[h])
		rep.Alpha[h], rep.Vol[h] = a, v
	}
	for _, y := range e.years {
		turn = append(turn, y.Turnover)
		mturn = append(mturn, y.MomentumTurnover)
	}
	rep.Turnover, rep.MomentumTurnover = round(meanOf0(turn)), round(meanOf0(mturn))
	for _, h := range Horizons {
		d := DistYear{h: h}
		calN := 0
		for _, y := range e.years {
			d.merge(y.Dist[h])
			if y.Dist[h].Calibrated {
				calN += y.Dist[h].N
			}
		}
		d.summarise()
		// Calibrated coverage counts only the years that had an earlier
		// year to calibrate on.
		for k := 0; k < 3; k++ {
			if calN > 0 {
				d.CalCoverage[k] = round(float64(d.cin[k]) / float64(calN) * 100)
			}
		}
		rep.Dist[h] = d
		rep.Reliability[h] = RelInfo{Up: d.up.reliability(), Beat: d.beat.reliability()}
	}
	rep.Calibration = CalibInfo{SlopeBps: map[int]float64{}, SlopeT: map[int]float64{}, Blend: map[int]float64{}, PITN: map[int]int{}, Ensemble: map[int][3]float64{}}
	for _, h := range []int{5, 20} {
		rep.Calibration.SlopeBps[h] = round(c.slope[h] * 1e4)
		rep.Calibration.SlopeT[h] = round(c.slopeT[h])
		rep.Calibration.Blend[h] = c.blend[h]
		w := c.ensW[h]
		rep.Calibration.Ensemble[h] = [3]float64{round(w[0]), round(w[1]), round(w[2])}
	}
	for _, h := range Horizons {
		rep.Calibration.PITN[h] = c.pit[h].N
	}
}

// sessionsAhead counts weekday sessions from last to a report date, plus
// one when the company reports after the close, since the first session to
// trade on it is then the next day.
func sessionsAhead(last, report time.Time, after bool) int {
	d := last.In(marketdata.Market)
	target := report.In(marketdata.Market)
	n := 0
	for d.Before(target) && n < 400 {
		d = d.AddDate(0, 0, 1)
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			n++
		}
	}
	if after {
		n++
	}
	return max(1, n)
}

// afterClose reports whether a company usually reports after the close.
func afterClose(s *Series) bool {
	var after, all int
	for _, e := range s.Earnings {
		h := e.At.In(marketdata.Market).Hour()
		if h == 0 && e.At.In(marketdata.Market).Minute() == 0 {
			continue
		}
		all++
		if h >= 16 {
			after++
		}
	}
	return all == 0 || after*2 >= all
}
