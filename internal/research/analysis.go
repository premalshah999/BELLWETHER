package research

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
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

// EarningsRef is one past earnings announcement and its EPS surprise.
type EarningsRef struct {
	AnnouncedAt time.Time `json:"announced_at"`
	SurprisePct *float64  `json:"surprise_pct,omitempty"`
}

// InsiderRef is one Form 4 filing's open-market trades in one direction.
type InsiderRef struct {
	FiledAt time.Time `json:"filed_at"`
	Buy     bool      `json:"buy"`
	Value   float64   `json:"value"`
	// Planned is set when every trade in the filing was made under a
	// pre-arranged 10b5-1 plan, which says nothing about a view.
	Planned bool   `json:"planned"`
	Who     string `json:"who,omitempty"`
}

// AnalysisDeps are the archive lookups analysis draws on. Any may be nil.
type AnalysisDeps struct {
	Events     func(ctx context.Context, symbol string, since time.Time) ([]EventRef, error)
	Earnings   func(ctx context.Context, symbol string) ([]EarningsRef, error)
	Insiders   func(ctx context.Context, symbol string, since time.Time) ([]InsiderRef, error)
	SmartMoney func(ctx context.Context, symbol string) (*SmartMoneyRef, error)
	Catalyst   func(ctx context.Context, symbol string) (*CatalystRef, error)
	// Name is the company's name as headlines write it, for the dated news
	// search that explains big days the archive does not reach.
	Name func(symbol string) string
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

// BigMove is one of the stock's largest market-adjusted days, with what is
// on record about why.
type BigMove struct {
	Date        string     `json:"date"`
	ReturnPct   float64    `json:"return_pct"`
	AbnormalPct float64    `json:"abnormal_pct"`
	VolumeRatio float64    `json:"volume_ratio"`
	Events      []EventRef `json:"events,omitempty"`
	// Earnings is set when the session was the first to trade on a results
	// announcement.
	Earnings string `json:"earnings,omitempty"`
	// Insider is set when a notable Form 4 became public just before it.
	Insider string `json:"insider,omitempty"`
	// Covered is whether the news archive already covered the company on
	// that day. A day before coverage with no other record is unknown, not
	// unexplained.
	Covered bool `json:"news_covered"`
	// Web is coverage published that day, found by a dated news search when
	// nothing in the archive explains the move.
	Web []WebRef `json:"web,omitempty"`
}

// Explained reports whether anything on record accounts for the move.
func (m BigMove) Explained() bool {
	return m.Earnings != "" || m.Insider != "" || len(m.Events) > 0 || len(m.Web) > 0
}

// Reaction is how the stock has moved after one kind of event.
type Reaction struct {
	Type   string `json:"type"`
	Label  string `json:"label"`
	Source string `json:"source"` // earnings, insiders or news
	Count  int    `json:"count"`
	// Since is the first session measured, so a reader can see how much
	// history the row rests on.
	Since    string  `json:"since,omitempty"`
	Day1Mean float64 `json:"day1_mean_pct"`
	Day5Mean float64 `json:"day5_mean_pct"`
	// AbsMean is the average size of the first-session move either way: how
	// much this kind of event moves the stock, whatever the direction.
	AbsMean float64 `json:"abs_mean_pct"`
	HitRate float64 `json:"hit_rate"`
}

// EventMove is one notable event and what the stock did after it.
type EventMove struct {
	Event   EventRef `json:"event"`
	Session string   `json:"session"`
	Day1Pct float64  `json:"day1_abnormal_pct"`
	Day5Pct float64  `json:"day5_abnormal_pct"`
}

// Analysis is what the price history says about one company, and how that
// history lines up with earnings, insider filings and the news.
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
	// NewsSince is the first day the news archive holds an event about the
	// company; empty when it holds none.
	NewsSince string   `json:"news_since,omitempty"`
	Notes     []string `json:"notes"`

	events int // archived events read, for the notes
}

// marketDay is the New York trading date of a bar or an event.
func marketDay(t time.Time) string { return t.In(marketdata.Market).Format("2006-01-02") }

// sessionClose is 4pm New York on a bar's trading date.
func sessionClose(t time.Time) time.Time {
	y, m, d := t.In(marketdata.Market).Date()
	return time.Date(y, m, d, 16, 0, 0, 0, marketdata.Market)
}

// filedKnown is when a Form 4 filed on a date is taken as public: that
// evening in New York, as the event study does.
func filedKnown(d time.Time) time.Time {
	y, m, day := d.In(marketdata.Market).Date()
	return time.Date(y, m, day, 20, 0, 0, 0, marketdata.Market)
}

const (
	// analysisBars is how much daily history analysis reads: about five
	// years, so earnings and insider reactions rest on twenty quarters rather
	// than the four a year would give.
	analysisBars = 1300
	// chartSessions is the window for the chart, beta and the biggest days.
	chartSessions = 253
)

// newsTypesNotMeasured are event types whose reaction table would mislead: a
// story about the price move itself, kinds measured better from their own
// filings (earnings, Form 4s), automated 13F-holder notices, and filing kinds
// with no US producer that survive only in the archive.
var newsTypesNotMeasured = map[string]bool{
	"UNCLASSIFIED": true, "PRICE_MOVEMENT": true, "VOLUME_SPURT": true,
	"EARNINGS": true, "INSIDER_TRANSACTION": true, "SHAREHOLDING_CHANGE": true,
	"NEWS_VERIFICATION": true, "NEWSPAPER_PUBLICATION": true, "ADMINISTRATIVE": true,
	"ALLOTMENT": true, "BONUS": true, "PLEDGE": true, "PROMOTER_TRANSACTION": true,
	"RECORD_DATE": true, "TRADING_WINDOW": true, "BOARD_MEETING": true, "AGM": true,
	"EGM": true, "CONCALL": true, "DEMISE": true, "FUND_NAV": true, "RIGHTS_ISSUE": true,
}

// newsLabels names event types the way a US reader would.
var newsLabels = map[string]string{
	"GUIDANCE": "Guidance and outlook", "DIVIDEND": "Dividend news", "STOCK_SPLIT": "Stock splits",
	"BUYBACK": "Buybacks", "ORDER_WIN": "Orders won", "ORDER_CANCELLED": "Orders cancelled",
	"CONTRACT": "Contracts and agreements", "PARTNERSHIP": "Partnerships", "ACQUISITION": "Acquisitions",
	"MERGER": "Mergers", "DEMERGER": "Spin-offs", "STAKE_SALE": "Stake sales", "INSOLVENCY": "Bankruptcy and distress",
	"CAPEX": "Capital spending", "NEW_PLANT": "New facilities", "NEW_PRODUCT": "Product news",
	"MANAGEMENT_CHANGE": "Executive changes", "AUDITOR_CHANGE": "Auditor changes", "FUND_RAISE": "Share and debt offerings",
	"DEBT": "Debt news", "CREDIT_RATING": "Credit ratings", "LITIGATION": "Lawsuits",
	"REGULATORY_ACTION": "Regulatory action", "REGULATORY_APPROVAL": "Regulatory approvals", "TAX_ACTION": "Tax disputes",
	"ANNUAL_REPORT": "Annual reports", "INVESTOR_PRESENTATION": "Investor presentations", "PRESS_RELEASE": "Press releases",
	"SECTOR_EVENT": "Industry news", "MACRO_EVENT": "Economic news", "COMMODITY_EVENT": "Commodity moves",
	"GEOPOLITICAL_EVENT": "Geopolitics", "REGULATORY_POLICY": "Policy and regulation",
	"SHAREHOLDING_CHANGE": "Ownership change", "NEWS_VERIFICATION": "Response to a report",
	"PRICE_MOVEMENT": "Price move", "INSIDER_TRANSACTION": "Insider trade", "CONCALL": "Earnings call",
	"NEWSPAPER_PUBLICATION": "Public notice", "ALLOTMENT": "Share issuance",
}

// NewsLabel is how an event type reads to a US investor.
func NewsLabel(t string) string {
	if l, ok := newsLabels[t]; ok {
		return l
	}
	s := strings.ToLower(strings.ReplaceAll(t, "_", " "))
	if s == "" {
		return "News"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// analyse builds the analysis for up to three subject symbols.
func (e *Engine) analyse(ctx context.Context, symbols []string) []Analysis {
	if e.prices == nil || e.analysisDeps == nil || len(symbols) == 0 {
		return nil
	}
	if len(symbols) > 3 {
		symbols = symbols[:3]
	}
	bench, err := e.prices.DailyBars(ctx, Benchmark, analysisBars)
	if err != nil || len(bench) < 60 {
		e.log.Warn("research: benchmark unavailable, analysis skipped", "err", err)
		return nil
	}
	var out []Analysis
	for _, sym := range symbols {
		bars, err := e.prices.DailyBars(ctx, sym, analysisBars)
		if err != nil || len(bars) < 60 {
			continue
		}
		a := e.analyseOne(ctx, sym, bars, bench)
		if a == nil {
			continue
		}
		if e.analysisDeps.Name != nil {
			e.explainFromWeb(ctx, a, e.analysisDeps.Name(sym))
			a.Notes = notes(a, a.events)
		}
		out = append(out, *a)
	}
	return out
}

// sample is one event measured: the session that first traded on it, the
// abnormal return that session and over five.
type sample struct {
	at     int
	d1, d5 float64
}

type reactionAcc struct {
	key, label, source string
	samples            []sample
	seen               map[int]bool
}

// add counts a session once per kind: forty headlines about one product
// launch are one event for the stock, not forty.
func (r *reactionAcc) add(s sample) {
	if r.seen == nil {
		r.seen = map[int]bool{}
	}
	if r.seen[s.at] {
		return
	}
	r.seen[s.at] = true
	r.samples = append(r.samples, s)
}

func (r *reactionAcc) reaction(day func(int) string) Reaction {
	out := Reaction{Type: r.key, Label: r.label, Source: r.source, Count: len(r.samples)}
	if len(r.samples) == 0 {
		return out
	}
	first := r.samples[0].at
	var d1, d5, abs float64
	hits := 0
	for _, s := range r.samples {
		first = min(first, s.at)
		d1 += s.d1
		d5 += s.d5
		abs += math.Abs(s.d1)
		if s.d5 > 0 {
			hits++
		}
	}
	n := float64(len(r.samples))
	out.Since = day(first)
	out.Day1Mean, out.Day5Mean, out.AbsMean = round2(d1/n), round2(d5/n), round2(abs/n)
	out.HitRate = round2(float64(hits) / n * 100)
	return out
}

func (e *Engine) analyseOne(ctx context.Context, sym string, bars, bench []Bar) *Analysis {
	// Align both series on shared trading days.
	bByDay := map[string]Bar{}
	for _, b := range bench {
		bByDay[marketDay(b.Time)] = b
	}
	type row struct {
		day       string
		s, b, vol float64
		t, close  time.Time
	}
	var all []row
	for _, b := range bars {
		d := marketDay(b.Time)
		if m, ok := bByDay[d]; ok && b.Close > 0 && m.Close > 0 {
			all = append(all, row{d, b.Close, m.Close, b.Volume, b.Time, sessionClose(b.Time)})
		}
	}
	if len(all) < 60 {
		return nil
	}
	N := len(all)
	off := max(0, N-chartSessions)
	rows := all[off:]
	n := len(rows)
	a := &Analysis{Symbol: sym, Benchmark: "S&P 500", AsOf: rows[n-1].day, Bars: n}

	// Daily returns over the whole history; beta from the last year only.
	rs := make([]float64, N)
	rb := make([]float64, N)
	for i := 1; i < N; i++ {
		rs[i] = all[i].s/all[i-1].s - 1
		rb[i] = all[i].b/all[i-1].b - 1
	}
	from := max(1, off)
	ms, mb := mean(rs[from:]), mean(rb[from:])
	var cov, vs, vb float64
	for i := from; i < N; i++ {
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
	ab := make([]float64, N)
	for i := 1; i < N; i++ {
		ab[i] = rs[i] - a.Beta*rb[i]
		if i >= from {
			if rs[i] > 0 {
				a.UpDays++
			} else if rs[i] < 0 {
				a.DownDays++
			}
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

	// firstAfter is the first session to close after a moment: the first
	// price a reader could have traded on once something was known.
	firstAfter := func(t time.Time) int {
		return sort.Search(N, func(i int) bool { return all[i].close.After(t) })
	}
	measure := func(t time.Time) (sample, bool) {
		i := firstAfter(t)
		if i == 0 || i+4 >= N {
			return sample{}, false
		}
		s := sample{at: i, d1: ab[i] * 100}
		for j := i; j < i+5; j++ {
			s.d5 += ab[j]
		}
		s.d5 *= 100
		return s, true
	}
	day := func(i int) string { return all[i].day }

	// Earnings, from the stored announcement history.
	earningsAt := map[int]string{}
	all5 := &reactionAcc{key: "EARNINGS", label: "Earnings reports, all", source: "earnings"}
	beat := &reactionAcc{key: "EARNINGS_BEAT", label: "Earnings beat estimates by 2%+", source: "earnings"}
	inline := &reactionAcc{key: "EARNINGS_IN_LINE", label: "Earnings within 2% of estimates", source: "earnings"}
	miss := &reactionAcc{key: "EARNINGS_MISS", label: "Earnings missed estimates by 2%+", source: "earnings"}
	if e.analysisDeps.Earnings != nil {
		if list, err := e.analysisDeps.Earnings(ctx, sym); err == nil {
			for _, er := range list {
				i := firstAfter(er.AnnouncedAt)
				if i == 0 || i >= N {
					continue
				}
				earningsAt[i] = earningsLine(er)
				s, ok := measure(er.AnnouncedAt)
				if !ok {
					continue
				}
				all5.add(s)
				if p := er.SurprisePct; p != nil {
					switch {
					case *p >= 2:
						beat.add(s)
					case *p > -2:
						inline.add(s)
					default:
						miss.add(s)
					}
				}
			}
		}
	}

	// Insider trading, from Form 4 filings: open-market purchases, and sales
	// not made under a pre-arranged plan. One sample per filing day.
	insiderAt := map[int]string{}
	buys := &reactionAcc{key: "INSIDER_BUYING", label: "Insider open-market buying", source: "insiders"}
	sells := &reactionAcc{key: "INSIDER_SELLING", label: "Insider selling, not pre-planned ($250K+)", source: "insiders"}
	if e.analysisDeps.Insiders != nil {
		if list, err := e.analysisDeps.Insiders(ctx, sym, all[0].t); err == nil {
			type dayTotal struct {
				buy, sell, planned float64
				who                string
			}
			byDay := map[int]*dayTotal{}
			for _, f := range list {
				i := firstAfter(filedKnown(f.FiledAt))
				if i == 0 || i >= N {
					continue
				}
				d := byDay[i]
				if d == nil {
					d = &dayTotal{}
					byDay[i] = d
				}
				switch {
				case f.Buy:
					d.buy += f.Value
				case f.Planned:
					d.planned += f.Value
				default:
					d.sell += f.Value
				}
				if d.who == "" {
					d.who = f.Who
				}
			}
			for i, d := range byDay {
				when := filedKnown(all[i-1].t)
				if d.buy > 0 {
					if s, ok := measure(when); ok && s.at == i {
						buys.add(s)
					}
				}
				if d.sell >= 250_000 {
					if s, ok := measure(when); ok && s.at == i {
						sells.add(s)
					}
				}
				switch {
				case d.buy >= 100_000:
					insiderAt[i] = fmt.Sprintf("Form 4 filed the evening before: insiders bought $%s on the open market", compactMoney(d.buy))
				case d.sell >= 5_000_000:
					insiderAt[i] = fmt.Sprintf("Form 4 filed the evening before: insiders sold $%s, not under a pre-arranged plan", compactMoney(d.sell))
				}
			}
		}
	}

	// News from the archive, which covers a company only from the day it
	// was first collected.
	var events []EventRef
	if e.analysisDeps.Events != nil {
		evs, err := e.analysisDeps.Events(ctx, sym, all[0].t.AddDate(0, 0, -2))
		if err == nil {
			events = evs
		}
	}
	var newsSince time.Time
	for _, ev := range events {
		if newsSince.IsZero() || ev.DiscoveredAt.Before(newsSince) {
			newsSince = ev.DiscoveredAt
		}
	}
	if !newsSince.IsZero() {
		a.NewsSince = marketDay(newsSince)
	}
	byType := map[string]*reactionAcc{}
	bestAt := map[int]EventMove{}
	for _, ev := range events {
		s, ok := measure(ev.DiscoveredAt)
		if !ok {
			continue
		}
		if !newsTypesNotMeasured[ev.Type] && ev.Type != "" {
			x := byType[ev.Type]
			if x == nil {
				x = &reactionAcc{key: ev.Type, label: NewsLabel(ev.Type), source: "news"}
				byType[ev.Type] = x
			}
			x.add(s)
		}
		if ev.Importance >= 6 && ev.Type != "PRICE_MOVEMENT" && ev.Type != "SHAREHOLDING_CHANGE" {
			if cur, ok := bestAt[s.at]; !ok || ev.Importance > cur.Event.Importance {
				bestAt[s.at] = EventMove{Event: ev, Session: day(s.at), Day1Pct: round2(s.d1), Day5Pct: round2(s.d5)}
			}
		}
	}

	// Biggest market-adjusted days of the last year, each with what is on
	// record about it: an earnings release it was the first session after, a
	// Form 4 that became public the evening before, and news found between
	// the previous close and the end of that day.
	idx := make([]int, 0, n)
	for i := from; i < N; i++ {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(x, y int) bool { return math.Abs(ab[idx[x]]) > math.Abs(ab[idx[y]]) })
	for _, i := range idx[:min(8, len(idx))] {
		m := BigMove{Date: all[i].day, ReturnPct: round2(rs[i] * 100), AbnormalPct: round2(ab[i] * 100),
			Earnings: earningsAt[i], Insider: insiderAt[i],
			Covered: !newsSince.IsZero() && !all[i].close.Before(newsSince)}
		if medVol > 0 {
			m.VolumeRatio = round2(all[i].vol / medVol)
		}
		lo, hi := all[i-1].close, all[i].close.Add(8*time.Hour)
		var found []EventRef
		for _, ev := range events {
			if ev.DiscoveredAt.After(lo) && !ev.DiscoveredAt.After(hi) && ev.Type != "SHAREHOLDING_CHANGE" {
				found = append(found, ev)
			}
		}
		// The most important first; a story that only reports the move
		// explains less than one about its cause.
		sort.SliceStable(found, func(x, y int) bool {
			px, py := found[x].Type == "PRICE_MOVEMENT", found[y].Type == "PRICE_MOVEMENT"
			if px != py {
				return !px
			}
			return found[x].Importance > found[y].Importance
		})
		m.Events = found[:min(3, len(found))]
		a.BigMoves = append(a.BigMoves, m)
	}
	sort.Slice(a.BigMoves, func(x, y int) bool { return a.BigMoves[x].Date > a.BigMoves[y].Date })

	// Reactions: earnings and insiders first, from their own long records,
	// then kinds of news seen on at least three separate sessions.
	for _, x := range []*reactionAcc{all5, beat, inline, miss, buys, sells} {
		if len(x.samples) >= 2 {
			a.Reactions = append(a.Reactions, x.reaction(day))
		}
	}
	var newsRows []Reaction
	for _, x := range byType {
		if len(x.samples) >= 3 {
			newsRows = append(newsRows, x.reaction(day))
		}
	}
	sort.Slice(newsRows, func(x, y int) bool {
		if newsRows[x].Count != newsRows[y].Count {
			return newsRows[x].Count > newsRows[y].Count
		}
		return newsRows[x].Type < newsRows[y].Type
	})
	a.Reactions = append(a.Reactions, newsRows[:min(6, len(newsRows))]...)

	for _, m := range bestAt {
		a.Notable = append(a.Notable, m)
	}
	sort.Slice(a.Notable, func(x, y int) bool { return math.Abs(a.Notable[x].Day1Pct) > math.Abs(a.Notable[y].Day1Pct) })
	if len(a.Notable) > 8 {
		a.Notable = a.Notable[:8]
	}

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
	a.events = len(events)
	a.Notes = notes(a, a.events)
	return a
}

// earningsLine says what an announcement was, in the words the table shows.
func earningsLine(er EarningsRef) string {
	t := er.AnnouncedAt.In(marketdata.Market)
	when := ""
	switch mins := t.Hour()*60 + t.Minute(); {
	case mins == 0:
		// A date with no time: the timing is not on record.
	case mins < 9*60+30:
		when = " before the open"
	case mins < 16*60:
		when = " during the session"
	default:
		when = " after the close"
	}
	line := fmt.Sprintf("Earnings%s on %s", when, t.Format("Jan 2, 2006"))
	if p := er.SurprisePct; p != nil {
		switch {
		case *p >= 0.5:
			line += fmt.Sprintf(": EPS beat estimates by %.1f%%", *p)
		case *p <= -0.5:
			line += fmt.Sprintf(": EPS missed estimates by %.1f%%", -*p)
		default:
			line += ": EPS in line with estimates"
		}
	}
	return line
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

	if len(a.BigMoves) > 0 {
		var earnings, insider, news, web, silent, before int
		for _, m := range a.BigMoves {
			switch {
			case m.Earnings != "":
				earnings++
			case m.Insider != "":
				insider++
			case len(m.Events) > 0:
				news++
			case len(m.Web) > 0:
				web++
			case m.Covered:
				silent++
			default:
				before++
			}
		}
		var parts []string
		if earnings > 0 {
			parts = append(parts, fmt.Sprintf("%d %s the first session after earnings", earnings, plural(earnings, "was", "were")))
		}
		if insider > 0 {
			parts = append(parts, fmt.Sprintf("%d followed a notable insider filing", insider))
		}
		if news > 0 {
			parts = append(parts, fmt.Sprintf("%d coincided with news in the archive", news))
		}
		if web > 0 {
			parts = append(parts, fmt.Sprintf("%d %s explained by coverage published that day, found by a dated news search", web, plural(web, "was", "were")))
		}
		if silent > 0 {
			parts = append(parts, fmt.Sprintf("%d had no news Bellwether collected", silent))
		}
		if before > 0 {
			since := "holds no news about it"
			if a.NewsSince != "" {
				since = "begins on " + longDate(a.NewsSince)
			}
			parts = append(parts, fmt.Sprintf("%d fell before the news archive for %s %s and turned up no coverage in a dated search", before, a.Symbol, since))
		}
		out = append(out, fmt.Sprintf("Of its %d largest market-adjusted days in the past year, %s.", len(a.BigMoves), joinParts(parts)))
	}

	for _, r := range a.Reactions {
		if r.Source == "earnings" && r.Type == "EARNINGS" && r.Count >= 3 {
			out = append(out, fmt.Sprintf("Across %d earnings reports since %s, the first session moved %.1f%% either way on average against the market (mean %+.2f%%), and the five sessions from it averaged %+.2f%%.",
				r.Count, longDate(r.Since), r.AbsMean, r.Day1Mean, r.Day5Mean))
		}
	}
	for _, r := range a.Reactions {
		if r.Type != "EARNINGS" && r.Count >= 3 {
			out = append(out, fmt.Sprintf("%s (%d sessions since %s): %+.2f%% against the market the first session and %+.2f%% over five, ahead of it %.0f%% of the time.",
				r.Label, r.Count, longDate(r.Since), r.Day1Mean, r.Day5Mean, r.HitRate))
		}
		if len(out) >= 8 {
			break
		}
	}
	if eventCount == 0 {
		out = append(out, "No archived news names this company, so reactions to news could not be measured.")
	} else if a.NewsSince != "" {
		out = append(out, fmt.Sprintf("Reactions to news rest on the archive since %s and count each session once, however many stories it carried; earnings and insider reactions use their full filing history.", longDate(a.NewsSince)))
	}
	if sm := a.SmartMoney; sm != nil && (sm.InsiderBuyValue > 0 || sm.InsiderSellValue > 0) {
		out = append(out, fmt.Sprintf("Insiders bought $%s and sold $%s on the open market over the last six months.",
			compactMoney(sm.InsiderBuyValue), compactMoney(sm.InsiderSellValue)))
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func joinParts(parts []string) string {
	switch len(parts) {
	case 0:
		return "none could be matched to anything on record"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// longDate writes a 2006-01-02 date as "Sep 16, 2026".
func longDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("Jan 2, 2006")
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
