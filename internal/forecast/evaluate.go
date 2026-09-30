package forecast

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// lambda is the ridge penalty, per row: enough to keep correlated factors
// (momentum and the 52-week high) from trading off wild weights, small
// enough to leave real signal alone.
const lambda = 0.01

// trainYears is how much history each fit uses. Markets change; a model fit
// on a decade learns a market that no longer exists.
const trainYears = 3

// YearResult is the model's out-of-sample record in one calendar year: it
// was fit only on earlier years.
type YearResult struct {
	Year int `json:"year"`
	Days int `json:"days"`
	// RankIC is the mean daily rank correlation between the model's scores
	// and what the stocks then did. Zero is no skill; 0.05 is good for a
	// daily cross-sectional model.
	RankIC float64 `json:"rank_ic"`
	T      float64 `json:"t"`
	// SpreadPct is how much the top tenth beat the bottom tenth over the
	// horizon, on average.
	SpreadPct float64 `json:"spread_pct"`
	// TopHitPct is how often the top tenth beat the average stock.
	TopHitPct float64 `json:"top_hit_pct"`
}

// FactorIC is one factor's own record and the weight the model gives it.
type FactorIC struct {
	Factor
	RankIC float64 `json:"rank_ic"`
	T      float64 `json:"t"`
	Weight float64 `json:"weight"`
}

// Report is a run: how the model did out of sample, and its current weights.
type Report struct {
	At        time.Time    `json:"at"`
	AsOf      time.Time    `json:"as_of"`
	Horizon   int          `json:"horizon"`
	Symbols   int          `json:"symbols"`
	TrainRows int          `json:"train_rows"`
	Years     []YearResult `json:"years"`
	Overall   YearResult   `json:"overall"`
	Factors   []FactorIC   `json:"factors"`
	Model     Model        `json:"model"`
}

// Driver is one factor's pull on a stock's score.
type Driver struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	Contribution float64 `json:"contribution"`
}

// Prediction is one stock's score on the latest session.
type Prediction struct {
	Symbol string  `json:"symbol"`
	Score  float64 `json:"score"`
	// Percentile is where it ranks: 100 is the most likely to outperform.
	Percentile float64  `json:"percentile"`
	Drivers    []Driver `json:"drivers"`
}

// Run validates the model walk-forward and scores the latest session.
func Run(rows []Row, now time.Time) (Report, []Prediction, error) {
	raw := map[string]map[string]float64{}
	for _, r := range rows {
		k := r.Date.Format("2006-01-02")
		if raw[k] == nil {
			raw[k] = map[string]float64{}
		}
		raw[k][r.Symbol] = r.Y
	}
	byDay := normalise(rows)
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	if len(days) < 100 {
		return Report{}, nil, fmt.Errorf("forecast: %d usable sessions, too few", len(days))
	}
	rep := Report{At: now, Horizon: Horizon}
	symbols := map[string]bool{}
	for _, r := range rows {
		symbols[r.Symbol] = true
	}
	rep.Symbols = len(symbols)

	first, _ := time.Parse("2006-01-02", days[0])
	last, _ := time.Parse("2006-01-02", days[len(days)-1])
	rep.AsOf = last
	embargo := time.Duration(Horizon*2) * 24 * time.Hour

	var allIC, allSpread []float64
	var allHits, allDays int
	factorICs := make([][]float64, len(Factors))
	for year := first.Year() + 2; year <= last.Year(); year++ {
		start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		train := collect(byDay, days, start.AddDate(-trainYears, 0, 0), start.Add(-embargo))
		m, err := fit(train, lambda)
		if err != nil {
			continue
		}
		yr := YearResult{Year: year}
		var ics, spreads []float64
		hits := 0
		for _, d := range days {
			dt, _ := time.Parse("2006-01-02", d)
			if dt.Year() != year {
				continue
			}
			var day []Row
			for _, r := range byDay[d] {
				if !math.IsNaN(r.Y) {
					day = append(day, r)
				}
			}
			if len(day) < 30 {
				continue
			}
			scores := make([]float64, len(day))
			ys := make([]float64, len(day))
			for i, r := range day {
				scores[i], ys[i] = m.Score(r.X), r.Y
			}
			ic := pearson(rankScale(scores), ys)
			ics = append(ics, ic)
			for f := range Factors {
				xs := make([]float64, len(day))
				for i, r := range day {
					xs[i] = r.X[f]
				}
				factorICs[f] = append(factorICs[f], pearson(xs, ys))
			}
			top, bottom, avg := deciles(day, scores, raw[d])
			spreads = append(spreads, (top-bottom)*100)
			if top > avg {
				hits++
			}
		}
		if len(ics) == 0 {
			continue
		}
		yr.Days = len(ics)
		yr.RankIC, yr.T = summarise(ics)
		yr.SpreadPct, _ = summarise(spreads)
		yr.TopHitPct = round(float64(hits) / float64(len(ics)) * 100)
		rep.Years = append(rep.Years, yr)
		allIC, allSpread = append(allIC, ics...), append(allSpread, spreads...)
		allHits += hits
		allDays += len(ics)
	}
	if allDays > 0 {
		rep.Overall = YearResult{Days: allDays, TopHitPct: round(float64(allHits) / float64(allDays) * 100)}
		rep.Overall.RankIC, rep.Overall.T = summarise(allIC)
		rep.Overall.SpreadPct, _ = summarise(allSpread)
	}

	// The live model: the latest three years, up to the last label that is
	// fully known.
	train := collect(byDay, days, last.AddDate(-trainYears, 0, 0), last.Add(-embargo))
	m, err := fit(train, lambda)
	if err != nil {
		return rep, nil, err
	}
	rep.Model, rep.TrainRows = m, countKnown(train)
	for f, fac := range Factors {
		ic, t := summarise(factorICs[f])
		rep.Factors = append(rep.Factors, FactorIC{Factor: fac, RankIC: ic, T: t, Weight: round4(m.Coef[f])})
	}

	today := byDay[days[len(days)-1]]
	scores := make([]float64, len(today))
	for i, r := range today {
		scores[i] = m.Score(r.X)
	}
	pct := rankScale(scores)
	preds := make([]Prediction, len(today))
	for i, r := range today {
		p := Prediction{Symbol: r.Symbol, Score: round4(scores[i]), Percentile: round((pct[i] + 1) * 50)}
		for f, fac := range Factors {
			p.Drivers = append(p.Drivers, Driver{Key: fac.Key, Label: fac.Label, Contribution: round4(m.Coef[f] * r.X[f])})
		}
		sort.Slice(p.Drivers, func(a, b int) bool { return math.Abs(p.Drivers[a].Contribution) > math.Abs(p.Drivers[b].Contribution) })
		p.Drivers = p.Drivers[:3]
		preds[i] = p
	}
	sort.Slice(preds, func(a, b int) bool { return preds[a].Score > preds[b].Score })
	return rep, preds, nil
}

func collect(byDay map[string][]Row, days []string, from, to time.Time) []Row {
	var out []Row
	for _, d := range days {
		dt, _ := time.Parse("2006-01-02", d)
		if dt.Before(from) || !dt.Before(to) {
			continue
		}
		out = append(out, byDay[d]...)
	}
	return out
}

func countKnown(rows []Row) int {
	n := 0
	for _, r := range rows {
		if !math.IsNaN(r.Y) {
			n++
		}
	}
	return n
}

// deciles is the mean raw forward return of the top and bottom tenth by
// score, and of every stock that day.
func deciles(day []Row, scores []float64, raw map[string]float64) (top, bottom, avg float64) {
	idx := make([]int, len(day))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	n := max(1, len(idx)/10)
	mean := func(ix []int) float64 {
		var s float64
		for _, i := range ix {
			s += raw[day[i].Symbol]
		}
		return s / float64(len(ix))
	}
	return mean(idx[:n]), mean(idx[len(idx)-n:]), mean(idx)
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

// summarise is a series' mean and its t statistic.
func summarise(xs []float64) (float64, float64) {
	m, sd := meanSD(xs)
	t := 0.0
	if sd > 0 {
		t = m / (sd / math.Sqrt(float64(len(xs))))
	}
	return round4(m), round(t)
}

func round(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*100) / 100
}

func round4(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*10000) / 10000
}
