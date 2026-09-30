// Package eventstudy answers whether a kind of event actually moves the
// stocks it names, measured against the S&P 500.
//
// Two numbers per event, because they answer different questions:
//
//   - The reaction: from the last close before the event was known to the
//     first close after. How much the news moved the price. Nobody could
//     have traded it, since it happens as the news lands.
//   - The drift: from that first close after to N trading days later. What
//     someone acting on the news once it was known would have made. This is
//     the tradable edge, and in an efficient market it is usually near zero.
//
// The anchor is the knowledge time: when this system discovered the event,
// or for backfilled history the official announcement time. Anything later
// would let a study use a price that moved before the news was public.
package eventstudy

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// minSamplesForResult is how many cases a mean needs before it describes a
// pattern rather than a handful of events.
const minSamplesForResult = 20

// Sample is one event's outcome, resolved to the prices the study needs.
type Sample struct {
	Symbol string
	At     time.Time
	// Group splits a study: an earnings beat or miss, a positive or
	// negative classification. Empty when the study has no split.
	Group string

	PreClose, EntryClose, ExitClose                float64
	BenchPreClose, BenchEntryClose, BenchExitClose float64
}

func pct(to, from float64) float64 { return (to/from - 1) * 100 }

// ReactionPct is the abnormal move from the close before the news to the
// first close after it.
func (s Sample) ReactionPct() float64 {
	return pct(s.EntryClose, s.PreClose) - pct(s.BenchEntryClose, s.BenchPreClose)
}

// AbnormalReturnPct is the abnormal drift over the holding window, entered
// at the first close after the news was known.
func (s Sample) AbnormalReturnPct() float64 {
	return pct(s.ExitClose, s.EntryClose) - pct(s.BenchExitClose, s.BenchEntryClose)
}

// Stats summarises one population of abnormal returns.
type Stats struct {
	Mean   float64 `json:"mean_pct"`
	Median float64 `json:"median_pct"`
	StdDev float64 `json:"stddev_pct"`
	// HitRate is the share above zero, already scaled to 0-100.
	HitRate float64 `json:"hit_rate"`
	// T is the mean divided by its standard error: beyond about 2 it is
	// unlikely to be chance.
	T float64 `json:"t"`
}

func statsOf(xs []float64) Stats {
	if len(xs) == 0 {
		return Stats{}
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	var sum float64
	var hits int
	for _, x := range xs {
		sum += x
		if x > 0 {
			hits++
		}
	}
	mean := sum / float64(len(xs))
	sd := stddev(xs, mean)
	st := Stats{
		Mean: round2(mean), Median: round2(median(sorted)), StdDev: round2(sd),
		HitRate: round2(float64(hits) / float64(len(xs)) * 100),
	}
	if sd > 0 {
		st.T = round2(mean / (sd / math.Sqrt(float64(len(xs)))))
	}
	return st
}

// Group is one slice of a study.
type Group struct {
	Label    string `json:"label"`
	Samples  int    `json:"samples"`
	Reaction Stats  `json:"reaction"`
	Drift    Stats  `json:"drift"`
	// AbsReaction is the typical size of the move, whichever way it went.
	AbsReaction float64 `json:"abs_reaction_pct"`
}

// Result is one event type's study.
type Result struct {
	EventType   string `json:"event_type"`
	Benchmark   string `json:"benchmark"`
	HoldingDays int    `json:"holding_days"`

	// TotalEvents is how many event/symbol pairs the archive holds before
	// price-history coverage narrowed them down to Samples.
	TotalEvents int `json:"total_events"`
	Samples     int `json:"samples"`

	// The drift, kept at the top level for existing callers.
	MeanAbnormalReturnPct   float64 `json:"mean_abnormal_return_pct"`
	MedianAbnormalReturnPct float64 `json:"median_abnormal_return_pct"`
	StdDevPct               float64 `json:"stddev_pct"`
	HitRate                 float64 `json:"hit_rate"`

	Reaction    Stats   `json:"reaction"`
	Drift       Stats   `json:"drift"`
	AbsReaction float64 `json:"abs_reaction_pct"`
	Groups      []Group `json:"groups,omitempty"`
	// Since and Until bound the events measured.
	Since *time.Time `json:"since,omitempty"`
	Until *time.Time `json:"until,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

func summarise(samples []Sample) (reaction, drift Stats, abs float64) {
	r := make([]float64, len(samples))
	d := make([]float64, len(samples))
	var absSum float64
	for i, s := range samples {
		r[i], d[i] = s.ReactionPct(), s.AbnormalReturnPct()
		absSum += math.Abs(r[i])
	}
	if len(samples) > 0 {
		abs = round2(absSum / float64(len(samples)))
	}
	return statsOf(r), statsOf(d), abs
}

// Run aggregates built samples into a Result. totalEvents is the count before
// coverage filtering. groupOrder fixes the order groups are reported in;
// groups not named in it follow alphabetically.
func Run(eventType, benchmark string, holdingDays, totalEvents int, samples []Sample, groupOrder ...string) Result {
	res := Result{
		EventType: eventType, Benchmark: benchmark, HoldingDays: holdingDays,
		TotalEvents: totalEvents, Samples: len(samples),
	}
	if len(samples) == 0 {
		res.Warnings = append(res.Warnings,
			"None of these events had a resolved symbol with price history covering its window.")
		return res
	}
	res.Reaction, res.Drift, res.AbsReaction = summarise(samples)
	res.MeanAbnormalReturnPct, res.MedianAbnormalReturnPct = res.Drift.Mean, res.Drift.Median
	res.StdDevPct, res.HitRate = res.Drift.StdDev, res.Drift.HitRate

	first, last := samples[0].At, samples[0].At
	byGroup := map[string][]Sample{}
	for _, s := range samples {
		if s.At.Before(first) {
			first = s.At
		}
		if s.At.After(last) {
			last = s.At
		}
		if s.Group != "" {
			byGroup[s.Group] = append(byGroup[s.Group], s)
		}
	}
	res.Since, res.Until = &first, &last

	rank := map[string]int{}
	for i, g := range groupOrder {
		rank[g] = i + 1
	}
	for label, ss := range byGroup {
		g := Group{Label: label, Samples: len(ss)}
		g.Reaction, g.Drift, g.AbsReaction = summarise(ss)
		res.Groups = append(res.Groups, g)
	}
	sort.Slice(res.Groups, func(i, j int) bool {
		ri, rj := rank[res.Groups[i].Label], rank[res.Groups[j].Label]
		if ri == 0 {
			ri = len(groupOrder) + 1
		}
		if rj == 0 {
			rj = len(groupOrder) + 1
		}
		if ri != rj {
			return ri < rj
		}
		return res.Groups[i].Label < res.Groups[j].Label
	})

	if res.Samples < minSamplesForResult {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"Only %d sample(s). A mean and a hit rate need far more than this before they describe a pattern rather than this particular handful of events.",
			res.Samples))
	}
	if res.Samples < res.TotalEvents {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%d of %d events were excluded: the stock has no stored daily history covering the window, or the window has not finished yet.",
			res.TotalEvents-res.Samples, res.TotalEvents))
	}
	return res
}

// closeTime is when a daily bar's price was set: the session close on the
// day it labels.
func closeTime(c marketdata.Candle) time.Time {
	t, _ := marketdata.ExchangeUS.SessionClose(c.Time)
	return t
}

// firstCloseAfter is the index of the first bar whose close came after at.
func firstCloseAfter(candles []marketdata.Candle, at time.Time) (int, bool) {
	i := sort.Search(len(candles), func(i int) bool { return closeTime(candles[i]).After(at) })
	return i, i < len(candles)
}

// BuildSample locates, in the symbol's series and the benchmark's, the last
// close before at, the first close after it, and the close holdingDays
// trading days later. ok is false when either series does not cover that.
// Both series must be ordered oldest first.
func BuildSample(symbol string, at time.Time, holdingDays int, candles, benchmark []marketdata.Candle) (Sample, bool) {
	if holdingDays <= 0 {
		return Sample{}, false
	}
	ei, ok := firstCloseAfter(candles, at)
	if !ok || ei == 0 || ei+holdingDays >= len(candles) {
		return Sample{}, false
	}
	// The benchmark is aligned by the symbol's own entry session, so a gap in
	// one series does not shift the other's window.
	bi, ok := firstCloseAfter(benchmark, closeTime(candles[ei]).Add(-time.Minute))
	if !ok || bi == 0 || bi+holdingDays >= len(benchmark) {
		return Sample{}, false
	}
	s := Sample{
		Symbol: symbol, At: at,
		PreClose: candles[ei-1].Close, EntryClose: candles[ei].Close, ExitClose: candles[ei+holdingDays].Close,
		BenchPreClose: benchmark[bi-1].Close, BenchEntryClose: benchmark[bi].Close,
		BenchExitClose: benchmark[bi+holdingDays].Close,
	}
	if s.PreClose <= 0 || s.EntryClose <= 0 || s.BenchPreClose <= 0 || s.BenchEntryClose <= 0 {
		return Sample{}, false
	}
	return s, true
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func stddev(xs []float64, mean float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var ss float64
	for _, x := range xs {
		d := x - mean
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

func round2(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*100) / 100
}
