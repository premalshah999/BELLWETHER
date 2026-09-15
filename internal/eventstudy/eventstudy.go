// Package eventstudy answers the question retail tools never ask: does this
// event type actually move the stocks it names, historically?
//
// For each event of a given type, it compares what a symbol actually
// returned over the N trading days after the event against what the
// market's own benchmark returned over the same window -- the abnormal
// return, the part of the move the broad market does not already explain.
// Averaged across every event of a type, that is a real, falsifiable
// answer to "does an EARNINGS event move this stock more than a random
// Tuesday would", instead of a chart that merely shows the two happened
// near each other.
//
// The anchor for "when did this event happen" is deliberately the
// knowledge timestamp -- news.Event.KnowledgeTime(), which is
// discovered_at -- not occurred_at or published_at. This package is not
// asking "did the stock move around the true event date" (a question that
// can leak information nobody had yet); it is asking "would a return
// measured from the moment this system actually knew about the event have
// looked abnormal", which is the only version of the question a downstream
// trading decision could ever have acted on. Anchoring anywhere else would
// make every result here look better than it could have been used.
package eventstudy

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// minSamplesForResult mirrors the spirit of the backtester's own small-
// sample warnings (internal/backtest's minBarsForResult / trade-count
// checks): a mean and a hit rate computed from a handful of events describe
// the sample, not a pattern.
const minSamplesForResult = 20

// Sample is one event's outcome, already resolved to the four prices the
// study needs. Building this (finding the right bars in a symbol's own
// series and in its benchmark's) is BuildSample's job; Run only aggregates,
// which keeps the statistics themselves trivial to test independently of
// any candle-lookup logic.
type Sample struct {
	Symbol                          string
	At                              time.Time
	EntryClose, ExitClose           float64
	BenchEntryClose, BenchExitClose float64
}

// AbnormalReturnPct is the symbol's return over the window minus the
// benchmark's return over the same window -- the part of the move the
// broad market does not already explain.
func (s Sample) AbnormalReturnPct() float64 {
	symRet := (s.ExitClose/s.EntryClose - 1) * 100
	benchRet := (s.BenchExitClose/s.BenchEntryClose - 1) * 100
	return symRet - benchRet
}

// Result is one event type's study.
type Result struct {
	EventType   string `json:"event_type"`
	Benchmark   string `json:"benchmark"`
	HoldingDays int    `json:"holding_days"`

	// TotalEvents is how many event/symbol pairs of this type exist in the
	// archive before price-history coverage narrowed them down to Samples.
	// The gap between the two is itself informative: this feature only
	// started persisting daily bars for the whole scanned universe
	// recently (see internal/scanner's Result.Series), so an event type
	// whose archive reaches back further than that will show a real,
	// expected gap here rather than a bug.
	TotalEvents int `json:"total_events"`
	Samples     int `json:"samples"`

	MeanAbnormalReturnPct   float64 `json:"mean_abnormal_return_pct"`
	MedianAbnormalReturnPct float64 `json:"median_abnormal_return_pct"`
	StdDevPct               float64 `json:"stddev_pct"`
	// HitRate is the fraction of samples with a positive abnormal return --
	// distinct from the mean, which one large outlier can dominate.
	HitRate float64 `json:"hit_rate"`

	Warnings []string `json:"warnings,omitempty"`
}

// Run aggregates already-built samples into a Result. totalEvents is the
// count before coverage filtering, passed in separately because Run has no
// other way to know how many were dropped for lacking price history.
func Run(eventType, benchmark string, holdingDays, totalEvents int, samples []Sample) Result {
	res := Result{
		EventType: eventType, Benchmark: benchmark, HoldingDays: holdingDays,
		TotalEvents: totalEvents, Samples: len(samples),
	}
	if len(samples) == 0 {
		res.Warnings = append(res.Warnings,
			"None of these events had a resolved symbol with price history covering its window.")
		return res
	}

	abnormal := make([]float64, len(samples))
	var sum float64
	var hits int
	for i, s := range samples {
		a := s.AbnormalReturnPct()
		abnormal[i] = a
		sum += a
		if a > 0 {
			hits++
		}
	}
	sort.Float64s(abnormal)

	res.MeanAbnormalReturnPct = round2(sum / float64(len(abnormal)))
	res.MedianAbnormalReturnPct = round2(median(abnormal))
	res.HitRate = round2(float64(hits) / float64(len(abnormal)) * 100)
	res.StdDevPct = round2(stddev(abnormal, sum/float64(len(abnormal))))

	if res.Samples < minSamplesForResult {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"Only %d sample(s). A mean and a hit rate need far more than this before they describe a pattern rather than this particular handful of events.",
			res.Samples))
	}
	if res.Samples < res.TotalEvents {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%d of %d events in the archive were excluded for lacking price history over the full window -- most likely because they predate this app persisting daily bars for the wider universe.",
			res.TotalEvents-res.Samples, res.TotalEvents))
	}
	return res
}

// BuildSample locates the entry bar (the first bar on or after at) and the
// exit bar holdingDays trading days later, in both the symbol's own series
// and its benchmark's, and reports ok=false when either series does not
// reach far enough to cover the window. candles and benchmark must be
// ordered oldest first, the convention every candle store in this app
// already returns.
func BuildSample(symbol string, at time.Time, holdingDays int, candles, benchmark []marketdata.Candle) (Sample, bool) {
	if holdingDays <= 0 {
		return Sample{}, false
	}
	ei, ok := firstOnOrAfter(candles, at)
	if !ok {
		return Sample{}, false
	}
	xi := ei + holdingDays
	if xi >= len(candles) {
		return Sample{}, false
	}

	bi, ok := firstOnOrAfter(benchmark, candles[ei].Time)
	if !ok {
		return Sample{}, false
	}
	bxi := bi + holdingDays
	if bxi >= len(benchmark) {
		return Sample{}, false
	}

	entry, exit := candles[ei].Close, candles[xi].Close
	benchEntry, benchExit := benchmark[bi].Close, benchmark[bxi].Close
	if entry <= 0 || benchEntry <= 0 {
		return Sample{}, false
	}
	return Sample{
		Symbol: symbol, At: at,
		EntryClose: entry, ExitClose: exit,
		BenchEntryClose: benchEntry, BenchExitClose: benchExit,
	}, true
}

func firstOnOrAfter(candles []marketdata.Candle, at time.Time) (int, bool) {
	for i, c := range candles {
		if !c.Time.Before(at) {
			return i, true
		}
	}
	return 0, false
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
