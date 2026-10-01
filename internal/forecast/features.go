package forecast

import (
	"math"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Factor is one input the return model reads.
type Factor struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Why is the evidence the factor rests on, in a sentence.
	Why string `json:"why"`
}

// Factors are the stock-level inputs, ranked across the universe each day.
// Every one is published, replicated evidence; none was found by searching
// this data, which is the surest way to find a pattern that is not there.
var Factors = []Factor{
	{"mom_12_1", "12-month momentum, skipping the last month", "Past-year winners keep outperforming for months (Jegadeesh and Titman, 1993)."},
	{"mom_6_1", "6-month momentum, skipping the last month", "The same effect at the horizon most studies use."},
	{"ret_1m", "1-month return", "Last month's winners give some back (Jegadeesh, 1990)."},
	{"ret_5d", "5-day return", "Week-long moves partly reverse, most in liquid stocks (Lehmann, 1990)."},
	{"ret_1d", "1-day return", "Yesterday's move partly reverses as liquidity providers are paid."},
	{"vol_20", "20-day volatility", "Calmer stocks have earned more per unit of risk (Frazzini and Pedersen, 2014)."},
	{"idio_vol", "Idiosyncratic volatility, 60 days", "Stocks with high firm-specific volatility have earned less (Ang, Hodrick, Xing and Zhang, 2006)."},
	{"beta", "Beta to the S&P 500", "High-beta stocks have earned less than the market model says (betting against beta)."},
	{"max_21", "Largest daily gain in a month", "Lottery-like stocks are overpriced and underperform (Bali, Cakici and Whitelaw, 2011)."},
	{"skew_60", "Return skewness, 60 days", "Investors overpay for positive skew (Boyer, Mitton and Vorkink, 2010)."},
	{"illiquidity", "Illiquidity (Amihud)", "Price impact per dollar traded; illiquid stocks carry a premium (Amihud, 2002)."},
	{"volume_trend", "Volume, 5 days against 60", "A volume surge signals news the price may not have absorbed (Gervais, Kaniel and Mingelgrin, 2001)."},
	{"dollar_volume", "Dollar volume (size)", "A size proxy: small, thinly traded stocks behave differently."},
	{"high_52w", "Distance from the 52-week high", "Stocks near their high break through rather than stall (George and Hwang, 2004)."},
	{"ma_50", "Distance from the 50-day average", "The trend as traders watch it."},
	{"ma_200", "Distance from the 200-day average", "The long trend; time-series momentum (Moskowitz, Ooi and Pedersen, 2012)."},
	{"rsi_14", "RSI(14)", "The most-watched oscillator."},
	{"surprise", "Latest earnings surprise", "Prices drift toward an earnings surprise for weeks (Bernard and Thomas, 1989)."},
	{"since_earnings", "Sessions since the last report", "Drift is strongest soon after the report."},
	{"to_earnings", "Sessions to the next report", "Stocks earn a premium into scheduled announcements (Frazzini and Lamont, 2007)."},
	{"sector_mom", "Sector momentum, 6 months", "Industries trend; much of momentum is industry momentum (Moskowitz and Grinblatt, 1999)."},
	{"sector_rel", "Momentum against its sector", "What the stock did beyond its industry."},
	{"seasonal", "Same-month return in past years", "Stocks repeat their relative performance in the same calendar month (Heston and Sadka, 2008)."},
	{"insider_buy", "Insider open-market buying, 90 days", "Opportunistic insider purchases predict returns (Cohen, Malloy and Pomorski, 2012)."},
	{"insider_sell", "Discretionary insider selling, 90 days", "Heavy discretionary selling is a weaker, opposite signal."},
	{"overnight", "Overnight returns, 5 days", "Overnight and intraday returns carry different investors' flows (Lou, Polk and Skouras, 2019)."},
	{"vol_ratio", "Volatility now against its year", "A stock whose volatility is rising is pricing something new."},
}

// RegimeFactors describe the market rather than the stock. They are the
// same for every stock on a day, so a linear model on ranks cannot use them;
// trees can, as the conditions under which the stock factors work.
var RegimeFactors = []Factor{
	{"market_vol", "S&P 500 volatility, 22 days", "Momentum crashes and reversal pays in turbulent markets (Daniel and Moskowitz, 2016)."},
	{"vix", "VIX", "The market's own forecast of its volatility."},
	{"market_1m", "S&P 500 return, 1 month", "Factor returns depend on the market's recent path (Cooper, Gutierrez and Hameed, 2004)."},
}

// Horizons are the forecast lengths in sessions. One simulation of the
// longest path yields all three.
var Horizons = []int{5, 10, 20}

const longest = 20

// Row is one stock on one session.
type Row struct {
	Stock int32
	T     int32
	X     []float32 // Factors then RegimeFactors
	Beta  float32
	// Y5 and Y20 are the abnormal log returns that followed, NaN until known.
	Y5, Y20 float32
}

// stockView caches what features need per stock.
type stockView struct {
	s     *Series
	beta  []float32 // shrunk 252-day beta at each session
	idio  []float32 // residual against beta times the market
	cv    *cleanVar
	month map[int]float64 // year*12+month -> log return of that month
	// Running sums of squared residuals and their count, earnings days
	// left out.
	pid2 []float32
	pidn []int16
}

// idioVar is the mean squared idiosyncratic return over the n sessions to
// t, earnings days left out.
func (v *stockView) idioVar(t, n int) float64 {
	lo := max(0, t+1-n)
	c := int(v.pidn[t+1]) - int(v.pidn[lo])
	if c < 1 {
		return 0
	}
	return (float64(v.pid2[t+1]) - float64(v.pid2[lo])) / float64(c)
}

// marketView is the market's derived series.
type marketView struct {
	r   []float64
	cv  *cleanVar
	vix []float64
}

func (p *Panel) market() *marketView {
	m := p.Market
	m.derive()
	mv := &marketView{r: make([]float64, p.T()), cv: m.clean(), vix: make([]float64, p.T())}
	for i := range mv.r {
		mv.r[i] = float64(m.r[i])
		mv.vix[i] = float64(p.VIX[i])
		if math.IsNaN(mv.vix[i]) && i > 0 {
			mv.vix[i] = mv.vix[i-1]
		}
	}
	return mv
}

const betaWindow = 252

func (p *Panel) view(s *Series, mv *marketView) *stockView {
	s.derive()
	T := p.T()
	v := &stockView{s: s, beta: make([]float32, T), idio: make([]float32, T), cv: s.clean(), month: map[int]float64{}}
	// Rolling covariance with the market, earnings days excluded so one
	// report does not set a year's beta.
	var sx, sy, sxx, sxy float64
	n := 0
	push := func(i int, sign float64) {
		if i <= s.First || s.earnDay[i] {
			return
		}
		x, y := mv.r[i], float64(s.r[i])
		sx += sign * x
		sy += sign * y
		sxx += sign * x * x
		sxy += sign * x * y
		n += int(sign)
	}
	for t := 0; t < T; t++ {
		push(t, 1)
		if t-betaWindow >= 0 {
			push(t-betaWindow, -1)
		}
		b := 1.0
		if n > 60 {
			den := sxx - sx*sx/float64(n)
			if den > 0 {
				b = (sxy - sx*sy/float64(n)) / den
			}
		}
		// Shrink toward one, as Blume (1971) and Vasicek (1973) found betas
		// regress there.
		v.beta[t] = float32(0.67*b + 0.33)
	}
	for t := s.First + 1; t < T; t++ {
		x := s.r[t] - v.beta[t-1]*float32(mv.r[t])
		// Outside earnings, capped at a 25% move for the same reason as the
		// variance proxies; a report's own move is kept whole.
		if !s.earnDay[t] {
			x = max(-0.223, min(0.223, x))
		}
		v.idio[t] = x
	}
	v.pid2, v.pidn = make([]float32, T+1), make([]int16, T+1)
	var acc float64
	for t := 0; t < T; t++ {
		v.pidn[t+1] = v.pidn[t]
		if t > s.First && !s.earnDay[t] {
			x := float64(v.idio[t])
			acc += x * x
			v.pidn[t+1]++
		}
		v.pid2[t+1] = float32(acc)
	}
	// Calendar-month returns for the seasonality factor.
	last := -1
	for t := s.First; t < T; t++ {
		d := p.Days[t]
		key := d.Year()*12 + int(d.Month()) - 1
		if t+1 < T && sameMonth(d, p.Days[t+1]) {
			continue
		}
		if last >= 0 {
			v.month[key] = math.Log(float64(s.C[t]) / float64(s.C[last]))
		}
		last = t
	}
	return v
}

func sameMonth(a, b time.Time) bool { return a.Year() == b.Year() && a.Month() == b.Month() }

// filedKnown is when a Form 4 filed on a date is public: that evening.
func filedKnown(d time.Time) time.Time {
	y, m, day := d.In(marketdata.Market).Date()
	return time.Date(y, m, day, 20, 0, 0, 0, marketdata.Market)
}

// features computes one stock's raw factors at session t, or nil when the
// history is too short. Everything read is from sessions at or before t.
func (p *Panel) features(v *stockView, mv *marketView, t int) []float32 {
	s := v.s
	if !s.ok(t, 260) || stale(s, t) {
		return nil
	}
	c := func(i int) float64 { return float64(s.C[i]) }
	x := make([]float32, len(Factors)+len(RegimeFactors))
	set := func(i int, f float64) {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			f = 0
		}
		x[i] = float32(f)
	}
	set(0, c(t-21)/c(t-252)-1)
	set(1, c(t-21)/c(t-126)-1)
	set(2, c(t)/c(t-21)-1)
	set(3, c(t)/c(t-5)-1)
	set(4, c(t)/c(t-1)-1)
	rs := make([]float64, 0, 60)
	for i := t - 19; i <= t; i++ {
		rs = append(rs, float64(s.r[i]))
	}
	_, sd20 := meanSD(rs)
	set(5, sd20)
	var id []float64
	for i := t - 59; i <= t; i++ {
		if !s.earnDay[i] {
			id = append(id, float64(v.idio[i]))
		}
	}
	_, sdi := meanSD(id)
	set(6, sdi)
	set(7, float64(v.beta[t]))
	mx := math.Inf(-1)
	for i := t - 20; i <= t; i++ {
		mx = math.Max(mx, float64(s.r[i]))
	}
	set(8, mx)
	rs = rs[:0]
	for i := t - 59; i <= t; i++ {
		rs = append(rs, float64(s.r[i]))
	}
	set(9, skewness(rs))
	var illiq, dv5, dv60, v5, v60 float64
	nIl := 0
	for i := t - 59; i <= t; i++ {
		dollar := float64(s.V[i]) * c(i)
		dv60 += dollar
		v60 += float64(s.V[i])
		if i > t-5 {
			v5 += float64(s.V[i])
			dv5 += dollar
		}
		if i > t-21 && dollar > 0 {
			illiq += math.Abs(float64(s.r[i])) / dollar
			nIl++
		}
	}
	if nIl > 0 {
		set(10, math.Log1p(illiq/float64(nIl)*1e9))
	}
	set(11, math.Log((v5/5+1)/(v60/60+1)))
	set(12, math.Log1p(dv60/60))
	hi := 0.0
	for i := t - 251; i <= t; i++ {
		hi = math.Max(hi, float64(s.H[i]))
	}
	if hi > 0 {
		set(13, c(t)/hi-1)
	}
	var m50, m200 float64
	for i := t - 199; i <= t; i++ {
		m200 += c(i)
		if i > t-50 {
			m50 += c(i)
		}
	}
	set(14, c(t)/(m50/50)-1)
	set(15, c(t)/(m200/200)-1)
	var up, down float64
	for i := t - 13; i <= t; i++ {
		d := c(i) - c(i-1)
		if d > 0 {
			up += d
		} else {
			down -= d
		}
	}
	if up+down > 0 {
		set(16, 100*up/(up+down))
	} else {
		set(16, 50)
	}
	// Earnings: the latest report whose reaction session has traded, and
	// the next scheduled one. Companies publish their report dates weeks
	// ahead, so the next date is known at t.
	since, to := 63.0, 63.0
	for _, e := range s.Earnings {
		if e.Session <= t {
			if t-e.Session < 63 {
				since = float64(t - e.Session)
				if !math.IsNaN(e.SurprisePct) {
					set(17, math.Max(-50, math.Min(50, e.SurprisePct)))
				} else {
					set(17, 0)
				}
			}
		} else {
			to = math.Min(to, float64(e.Session-t))
			break
		}
	}
	set(18, since)
	set(19, to)
	// 20 and 21 are filled across the sector afterwards.
	d := p.Days[t]
	var seas float64
	ns := 0
	for k := 1; k <= 4; k++ {
		if r, ok := v.month[(d.Year()-k)*12+int(d.Month())-1]; ok {
			seas += r
			ns++
		}
	}
	if ns > 0 {
		set(22, seas/float64(ns))
	}
	at := p.closes[t]
	var buy, sell float64
	for _, f := range s.Insiders {
		known := filedKnown(f.Filed)
		if known.Before(at) && at.Sub(known) <= 90*24*time.Hour {
			if f.Buy {
				buy += f.Value
			} else {
				sell += f.Value
			}
		}
	}
	set(23, math.Log1p(buy))
	set(24, math.Log1p(sell))
	var on float64
	for i := t - 4; i <= t; i++ {
		on += math.Log(float64(s.O[i]) / c(i-1))
	}
	set(25, on)
	set(26, math.Sqrt(v.cv.meanRV(t, 20)/math.Max(v.cv.meanRV(t, 252), varFloor)))
	base := len(Factors)
	mvol := math.Sqrt(mv.cv.meanR2(t, 22) * 252)
	set(base, mvol)
	vix := mv.vix[t]
	if math.IsNaN(vix) || vix <= 0 {
		vix = mvol * 100
	}
	set(base+1, vix)
	var m1 float64
	for i := t - 20; i <= t; i++ {
		m1 += mv.r[i]
	}
	set(base+2, m1)
	return x
}

// stale reports whether a stock's last quarter has runs of unchanged,
// untraded prices: a feed carrying a quote forward, not a market. Its
// variance reads as zero, and the move when trading resumes as a shock.
func stale(s *Series, t int) bool {
	flat, dead := 0, 0
	for i := t - 59; i <= t; i++ {
		if s.r[i] == 0 {
			flat++
			if s.V[i] == 0 {
				dead++
			}
		}
	}
	return dead > 3 || flat > 15
}

func skewness(xs []float64) float64 {
	m, sd := meanSD(xs)
	if sd == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		z := (x - m) / sd
		s += z * z * z
	}
	return s / float64(len(xs))
}

// abnormal is the stock's log return over (t, t+h] less beta times the
// market's, NaN when the window has not closed.
func (p *Panel) abnormal(v *stockView, mv *marketView, t, h int) float32 {
	if t+h >= p.T() {
		return float32(math.NaN())
	}
	var rs, rm float64
	for i := t + 1; i <= t+h; i++ {
		rs += float64(v.s.r[i])
		rm += mv.r[i]
	}
	return float32(rs - float64(v.beta[t])*rm)
}

// sectorFill sets the sector factors on one session's rows: the sector's
// mean 6-month momentum and each stock's momentum beyond it.
func sectorFill(rows []Row, sectors []string) {
	sum := map[string]float64{}
	n := map[string]int{}
	for _, r := range rows {
		k := sectors[r.Stock]
		sum[k] += float64(r.X[1])
		n[k]++
	}
	for i := range rows {
		k := sectors[rows[i].Stock]
		m := sum[k] / float64(n[k])
		if k == "" || n[k] < 5 {
			m = 0
		}
		rows[i].X[20] = float32(m)
		rows[i].X[21] = rows[i].X[1] - float32(m)
	}
}

// rankDay replaces one session's stock factors with their cross-sectional
// ranks in [-1, 1]. Regime factors are left as they are.
func rankDay(rows []Row) {
	n := len(rows)
	vals := make([]float64, n)
	for f := range Factors {
		for i, r := range rows {
			vals[i] = float64(r.X[f])
		}
		for i, rk := range rankScale(vals) {
			rows[i].X[f] = float32(rk)
		}
	}
}

// rankScale maps values to evenly spaced ranks in [-1, 1], ties averaged.
func rankScale(v []float64) []float64 {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	out := make([]float64, len(v))
	n := float64(len(v) - 1)
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && v[idx[j+1]] == v[idx[i]] {
			j++
		}
		r := (float64(i+j)/2)/math.Max(n, 1)*2 - 1
		for k := i; k <= j; k++ {
			out[idx[k]] = r
		}
		i = j + 1
	}
	return out
}
