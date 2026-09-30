package research

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Benchmark is the index every stock is measured against.
const Benchmark = "GSPC.INDEX"

// EventRef is one archived news event about a company, as analysis needs it.
type EventRef struct {
	ID           int64     `json:"id"`
	Headline     string    `json:"headline"`
	Type         string    `json:"type"`
	Importance   int       `json:"importance"`
	Source       string    `json:"source,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// SmartMoneyRef summarises insider and fund activity in one stock.
type SmartMoneyRef struct {
	InsiderBuyValue  float64  `json:"insider_buy_value"`
	InsiderSellValue float64  `json:"insider_sell_value"`
	InsiderBuyers    []string `json:"insider_buyers,omitempty"`
	InsiderSellers   int      `json:"insider_sellers"`
	FundMoves        []string `json:"fund_moves,omitempty"`
	CongressFilings  int      `json:"congress_filings"`
}

// CatalystRef is the next scheduled company event.
type CatalystRef struct {
	Kind    string   `json:"kind"`
	Date    string   `json:"date"`
	InDays  int      `json:"in_days"`
	EPSMean *float64 `json:"eps_mean,omitempty"`
}

// AnalysisDeps are the archive lookups analysis draws on. Any may be nil.
type AnalysisDeps struct {
	Events     func(ctx context.Context, symbol string, since time.Time) ([]EventRef, error)
	SmartMoney func(ctx context.Context, symbol string) (*SmartMoneyRef, error)
	Catalyst   func(ctx context.Context, symbol string) (*CatalystRef, error)
}

// WithAnalysis enables price-and-event analysis for the question's subjects.
func WithAnalysis(d AnalysisDeps) Option { return func(e *Engine) { e.analysisDeps = &d } }

// SeriesPoint is one trading day on the analysis chart: both lines rebased
// to 100 at the start of the window.
type SeriesPoint struct {
	Date  string  `json:"d"`
	Close float64 `json:"c"`
	Stock float64 `json:"s"`
	Bench float64 `json:"b"`
}

// BigMove is one of the stock's largest market-adjusted days.
type BigMove struct {
	Date        string     `json:"date"`
	ReturnPct   float64    `json:"return_pct"`
	AbnormalPct float64    `json:"abnormal_pct"`
	VolumeRatio float64    `json:"volume_ratio"`
	Events      []EventRef `json:"events,omitempty"`
}

// Reaction is how the stock has moved after one kind of news.
type Reaction struct {
	Type     string  `json:"type"`
	Count    int     `json:"count"`
	Day1Mean float64 `json:"day1_mean_pct"`
	Day5Mean float64 `json:"day5_mean_pct"`
	HitRate  float64 `json:"hit_rate"`
}

// EventMove is one notable event and what the stock did after it.
type EventMove struct {
	Event   EventRef `json:"event"`
	Session string   `json:"session"`
	Day1Pct float64  `json:"day1_abnormal_pct"`
	Day5Pct float64  `json:"day5_abnormal_pct"`
}

// Analysis is what the price history says about one company, and how that
// history lines up with the news.
type Analysis struct {
	Symbol      string         `json:"symbol"`
	Benchmark   string         `json:"benchmark"`
	AsOf        string         `json:"as_of"`
	Bars        int            `json:"bars"`
	Series      []SeriesPoint  `json:"series"`
	Beta        float64        `json:"beta"`
	Correlation float64        `json:"correlation"`
	Excess      []Return       `json:"excess"`
	UpDays      int            `json:"up_days"`
	DownDays    int            `json:"down_days"`
	BigMoves    []BigMove      `json:"big_moves"`
	Reactions   []Reaction     `json:"reactions,omitempty"`
	Notable     []EventMove    `json:"notable,omitempty"`
	SmartMoney  *SmartMoneyRef `json:"smart_money,omitempty"`
	Catalyst    *CatalystRef   `json:"catalyst,omitempty"`
	Notes       []string       `json:"notes"`
}

func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// analyse builds the analysis for up to three subject symbols.
func (e *Engine) analyse(ctx context.Context, symbols []string) []Analysis {
	if e.prices == nil || e.analysisDeps == nil || len(symbols) == 0 {
		return nil
	}
	if len(symbols) > 3 {
		symbols = symbols[:3]
	}
	bench, err := e.prices.DailyBars(ctx, Benchmark, 300)
	if err != nil || len(bench) < 60 {
		e.log.Warn("research: benchmark unavailable, analysis skipped", "err", err)
		return nil
	}
	var out []Analysis
	for _, sym := range symbols {
		bars, err := e.prices.DailyBars(ctx, sym, 300)
		if err != nil || len(bars) < 60 {
			continue
		}
		a := e.analyseOne(ctx, sym, bars, bench)
		if a != nil {
			out = append(out, *a)
		}
	}
	return out
}

func (e *Engine) analyseOne(ctx context.Context, sym string, bars, bench []Bar) *Analysis {
	// Align both series on shared trading days, last 252 at most.
	bByDay := map[string]Bar{}
	for _, b := range bench {
		bByDay[dayKey(b.Time)] = b
	}
	type row struct {
		day       string
		s, b, vol float64
		t         time.Time
	}
	var rows []row
	for _, b := range bars {
		if m, ok := bByDay[dayKey(b.Time)]; ok && b.Close > 0 && m.Close > 0 {
			rows = append(rows, row{dayKey(b.Time), b.Close, m.Close, b.Volume, b.Time})
		}
	}
	if len(rows) > 253 {
		rows = rows[len(rows)-253:]
	}
	if len(rows) < 60 {
		return nil
	}
	n := len(rows)
	a := &Analysis{Symbol: sym, Benchmark: "S&P 500", AsOf: rows[n-1].day, Bars: n}

	// Daily returns.
	rs := make([]float64, n)
	rb := make([]float64, n)
	for i := 1; i < n; i++ {
		rs[i] = rows[i].s/rows[i-1].s - 1
		rb[i] = rows[i].b/rows[i-1].b - 1
	}
	ms, mb := mean(rs[1:]), mean(rb[1:])
	var cov, vs, vb float64
	for i := 1; i < n; i++ {
		cov += (rs[i] - ms) * (rb[i] - mb)
		vs += (rs[i] - ms) * (rs[i] - ms)
		vb += (rb[i] - mb) * (rb[i] - mb)
	}
	if vb > 0 {
		a.Beta = cov / vb
	}
	if vs > 0 && vb > 0 {
		a.Correlation = cov / math.Sqrt(vs*vb)
	}
	// Market-model abnormal return: what the stock did beyond what its beta
	// to the market implied.
	ab := make([]float64, n)
	for i := 1; i < n; i++ {
		ab[i] = rs[i] - a.Beta*rb[i]
		if rs[i] > 0 {
			a.UpDays++
		} else if rs[i] < 0 {
			a.DownDays++
		}
	}

	// Chart series, rebased to 100.
	for _, r := range rows {
		a.Series = append(a.Series, SeriesPoint{
			Date: r.day, Close: round2(r.s),
			Stock: round2(r.s / rows[0].s * 100), Bench: round2(r.b / rows[0].b * 100),
		})
	}

	// Excess return by horizon.
	for _, h := range horizons {
		if h.Days >= n {
			continue
		}
		from := n - 1 - h.Days
		sr := (rows[n-1].s/rows[from].s - 1) * 100
		br := (rows[n-1].b/rows[from].b - 1) * 100
		a.Excess = append(a.Excess, Return{Horizon: h.Label, Percent: round2(sr - br), From: round2(sr), FromDate: rows[from].t})
	}

	// Volume baseline for the volume ratio on big days.
	vols := make([]float64, 0, n)
	for _, r := range rows {
		if r.vol > 0 {
			vols = append(vols, r.vol)
		}
	}
	sort.Float64s(vols)
	medVol := 0.0
	if len(vols) > 0 {
		medVol = vols[len(vols)/2]
	}

	// News around it.
	var events []EventRef
	if e.analysisDeps.Events != nil {
		evs, err := e.analysisDeps.Events(ctx, sym, rows[0].t.AddDate(0, 0, -2))
		if err == nil {
			events = evs
		}
	}
	eventsByDay := map[string][]EventRef{}
	for _, ev := range events {
		eventsByDay[dayKey(ev.DiscoveredAt)] = append(eventsByDay[dayKey(ev.DiscoveredAt)], ev)
	}

	// Biggest market-adjusted days, each with the news that coincided: the
	// day before, the day itself (news found overnight or pre-market lands
	// on the day's session).
	idx := make([]int, 0, n-1)
	for i := 1; i < n; i++ {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(x, y int) bool { return math.Abs(ab[idx[x]]) > math.Abs(ab[idx[y]]) })
	for _, i := range idx[:min(8, len(idx))] {
		m := BigMove{Date: rows[i].day, ReturnPct: round2(rs[i] * 100), AbnormalPct: round2(ab[i] * 100)}
		if medVol > 0 {
			m.VolumeRatio = round2(rows[i].vol / medVol)
		}
		for _, d := range []string{rows[i-1].day, rows[i].day} {
			for _, ev := range eventsByDay[d] {
				if len(m.Events) < 3 {
					m.Events = append(m.Events, ev)
				}
			}
		}
		// Also news discovered in the calendar gap before this session.
		for t := rows[i-1].t.AddDate(0, 0, 1); dayKey(t) < rows[i].day; t = t.AddDate(0, 0, 1) {
			for _, ev := range eventsByDay[dayKey(t)] {
				if len(m.Events) < 3 {
					m.Events = append(m.Events, ev)
				}
			}
		}
		a.BigMoves = append(a.BigMoves, m)
	}
	sort.Slice(a.BigMoves, func(x, y int) bool { return a.BigMoves[x].Date > a.BigMoves[y].Date })

	// Reactions. Each event is measured from the first session that closes
	// after it was discovered, so nothing here uses a price the reader could
	// not have seen after reading the news.
	firstAfter := func(t time.Time) int {
		k := dayKey(t)
		for i := 1; i < n; i++ {
			if rows[i].day > k {
				return i
			}
		}
		return -1
	}
	type acc struct {
		n      int
		d1, d5 float64
		hits   int
	}
	byType := map[string]*acc{}
	var moves []EventMove
	for _, ev := range events {
		i := firstAfter(ev.DiscoveredAt)
		if i < 0 || i+4 >= n {
			continue
		}
		d1 := ab[i] * 100
		d5 := 0.0
		for j := i; j < i+5; j++ {
			d5 += ab[j]
		}
		d5 *= 100
		if ev.Type != "" && ev.Type != "UNCLASSIFIED" {
			x := byType[ev.Type]
			if x == nil {
				x = &acc{}
				byType[ev.Type] = x
			}
			x.n++
			x.d1 += d1
			x.d5 += d5
			if d5 > 0 {
				x.hits++
			}
		}
		if ev.Importance >= 6 {
			moves = append(moves, EventMove{Event: ev, Session: rows[i].day, Day1Pct: round2(d1), Day5Pct: round2(d5)})
		}
	}
	for t, x := range byType {
		a.Reactions = append(a.Reactions, Reaction{
			Type: t, Count: x.n, Day1Mean: round2(x.d1 / float64(x.n)),
			Day5Mean: round2(x.d5 / float64(x.n)), HitRate: round2(float64(x.hits) / float64(x.n) * 100),
		})
	}
	sort.Slice(a.Reactions, func(x, y int) bool { return a.Reactions[x].Count > a.Reactions[y].Count })
	if len(a.Reactions) > 8 {
		a.Reactions = a.Reactions[:8]
	}
	sort.Slice(moves, func(x, y int) bool { return math.Abs(moves[x].Day1Pct) > math.Abs(moves[y].Day1Pct) })
	if len(moves) > 8 {
		moves = moves[:8]
	}
	a.Notable = moves

	if e.analysisDeps.SmartMoney != nil {
		if sm, err := e.analysisDeps.SmartMoney(ctx, sym); err == nil {
			a.SmartMoney = sm
		}
	}
	if e.analysisDeps.Catalyst != nil {
		if c, err := e.analysisDeps.Catalyst(ctx, sym); err == nil {
			a.Catalyst = c
		}
	}
	a.Notes = notes(a, len(events))
	return a
}

// notes states the analysis in sentences, so neither the reader nor the
// model has to do arithmetic to use it.
func notes(a *Analysis, eventCount int) []string {
	var out []string
	for _, x := range a.Excess {
		if x.Horizon == "1y" || (x.Horizon == "6m" && len(a.Excess) < 5) {
			mkt := x.From - x.Percent
			verb := "beat"
			if x.Percent < 0 {
				verb = "trailed"
			}
			out = append(out, fmt.Sprintf("Over %s %s returned %+.1f%% against %+.1f%% for the S&P 500: it %s the market by %.1f points.",
				horizonWords(x.Horizon), a.Symbol, x.From, mkt, verb, math.Abs(x.Percent)))
		}
	}
	out = append(out, fmt.Sprintf("Beta %.2f and correlation %.2f to the S&P 500: a 1%% market move has come with a %.2f%% move in %s on average, and the market explains about %.0f%% of its daily variation.",
		a.Beta, a.Correlation, a.Beta, a.Symbol, a.Correlation*a.Correlation*100))
	explained := 0
	for _, m := range a.BigMoves {
		if len(m.Events) > 0 {
			explained++
		}
	}
	if len(a.BigMoves) > 0 {
		out = append(out, fmt.Sprintf("Of its %d largest market-adjusted days, %d coincided with news in the archive and %d had none that Bellwether collected.",
			len(a.BigMoves), explained, len(a.BigMoves)-explained))
	}
	for _, r := range a.Reactions {
		if r.Count >= 3 {
			out = append(out, fmt.Sprintf("After %s news (%d cases) the stock moved %+.2f%% against the market the next session and %+.2f%% over five, beating it %.0f%% of the time.",
				strings.ToLower(strings.ReplaceAll(r.Type, "_", " ")), r.Count, r.Day1Mean, r.Day5Mean, r.HitRate))
		}
		if len(out) >= 6 {
			break
		}
	}
	if eventCount == 0 {
		out = append(out, "No archived news events name this company in the window, so reactions to news could not be measured.")
	}
	if sm := a.SmartMoney; sm != nil && (sm.InsiderBuyValue > 0 || sm.InsiderSellValue > 0) {
		out = append(out, fmt.Sprintf("Insiders bought $%s and sold $%s on the open market over the last six months.",
			compactMoney(sm.InsiderBuyValue), compactMoney(sm.InsiderSellValue)))
	}
	return out
}

func horizonWords(h string) string {
	switch h {
	case "1y":
		return "the past year"
	case "6m":
		return "six months"
	case "3m":
		return "three months"
	case "1m":
		return "a month"
	}
	return h
}

func compactMoney(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.1fB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.0fK", v/1e3)
	}
	return fmt.Sprintf("%.0f", v)
}

func mean(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}
