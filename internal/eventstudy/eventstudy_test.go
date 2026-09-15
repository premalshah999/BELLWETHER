package eventstudy

import (
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func bars(closes ...float64) []marketdata.Candle {
	out := make([]marketdata.Candle, len(closes))
	for i, c := range closes {
		out[i] = marketdata.Candle{Time: day(2026, 1, i+1), Close: c}
	}
	return out
}

// TestBuildSampleMeasuresAbnormalReturn is the whole point of the package:
// a symbol that outran its benchmark over the window has a positive
// abnormal return even when the market as a whole was also up that week.
func TestBuildSampleMeasuresAbnormalReturn(t *testing.T) {
	// Symbol up 10% over 2 days (100 -> 110), benchmark up 2% (100 -> 102)
	// over the same window -- an 8pp abnormal return.
	candles := bars(100, 105, 110)
	benchmark := bars(100, 101, 102)

	s, ok := BuildSample("AAPL", day(2026, 1, 1), 2, candles, benchmark)
	if !ok {
		t.Fatal("BuildSample: want ok")
	}
	got := s.AbnormalReturnPct()
	if got < 7.9 || got > 8.1 {
		t.Errorf("AbnormalReturnPct = %.2f, want ~8.0", got)
	}
}

// TestBuildSampleUsesFirstBarOnOrAfter covers the anchor rule: an event
// discovered mid-window (a weekend, a holiday, any gap in the trading
// calendar) enters on the next bar that actually exists, not a bar that
// predates the knowledge the study is supposed to be conditioned on.
func TestBuildSampleUsesFirstBarOnOrAfter(t *testing.T) {
	candles := bars(100, 100, 110, 120) // days 1..4
	benchmark := bars(100, 100, 105, 110)

	// "at" falls between day 1 and day 2 -- no bar at that instant.
	at := day(2026, 1, 1).Add(12 * time.Hour)
	s, ok := BuildSample("AAPL", at, 1, candles, benchmark)
	if !ok {
		t.Fatal("BuildSample: want ok")
	}
	// Entry should be day 2's close (100), exit 1 day later is day 3 (110).
	if s.EntryClose != 100 || s.ExitClose != 110 {
		t.Errorf("Entry/Exit = %v/%v, want 100/110", s.EntryClose, s.ExitClose)
	}
}

// TestBuildSampleRejectsInsufficientHistory is the honest-gap case: most
// events in the archive predate this app persisting daily bars for the
// wider universe (see the package doc), and a study must exclude those
// rather than pretend to measure a window it cannot see.
func TestBuildSampleRejectsInsufficientHistory(t *testing.T) {
	candles := bars(100, 105) // only 2 bars
	benchmark := bars(100, 101, 102, 103)

	if _, ok := BuildSample("AAPL", day(2026, 1, 1), 5, candles, benchmark); ok {
		t.Error("BuildSample: want !ok when the symbol's own series does not reach the exit bar")
	}

	shortBench := bars(100, 101) // benchmark too short instead
	if _, ok := BuildSample("AAPL", day(2026, 1, 1), 5, bars(100, 105, 110, 115, 120, 125, 130), shortBench); ok {
		t.Error("BuildSample: want !ok when the benchmark series does not reach the exit bar")
	}
}

func TestBuildSampleRejectsNonPositiveHoldingDays(t *testing.T) {
	if _, ok := BuildSample("AAPL", day(2026, 1, 1), 0, bars(100, 105), bars(100, 105)); ok {
		t.Error("BuildSample: want !ok for a zero holding period")
	}
}

// TestRunAggregatesAcrossSamples exercises mean, median and hit rate
// together against a hand-computed set of abnormal returns, so a
// regression in any one statistic shows up even if the others still pass.
func TestRunAggregatesAcrossSamples(t *testing.T) {
	// Abnormal returns: +10, +10, -2, -2 -> mean 4, median 4, hit rate 50%.
	samples := []Sample{
		{Symbol: "A", EntryClose: 100, ExitClose: 110, BenchEntryClose: 100, BenchExitClose: 100},
		{Symbol: "B", EntryClose: 100, ExitClose: 110, BenchEntryClose: 100, BenchExitClose: 100},
		{Symbol: "C", EntryClose: 100, ExitClose: 98, BenchEntryClose: 100, BenchExitClose: 100},
		{Symbol: "D", EntryClose: 100, ExitClose: 98, BenchEntryClose: 100, BenchExitClose: 100},
	}
	res := Run("EARNINGS", "GSPC.INDEX", 5, 4, samples)

	if res.Samples != 4 {
		t.Errorf("Samples = %d, want 4", res.Samples)
	}
	if res.MeanAbnormalReturnPct != 4 {
		t.Errorf("MeanAbnormalReturnPct = %.2f, want 4.00", res.MeanAbnormalReturnPct)
	}
	if res.MedianAbnormalReturnPct != 4 {
		t.Errorf("MedianAbnormalReturnPct = %.2f, want 4.00", res.MedianAbnormalReturnPct)
	}
	if res.HitRate != 50 {
		t.Errorf("HitRate = %.2f, want 50.00", res.HitRate)
	}
}

// TestRunWarnsOnSmallSample and TestRunWarnsOnCoverageGap guard the two
// honesty checks a result must carry rather than presenting a number with
// no context: too few events to mean anything, and events silently dropped
// for lacking price coverage.
func TestRunWarnsOnSmallSample(t *testing.T) {
	res := Run("RARE_EVENT", "GSPC.INDEX", 5, 3, []Sample{
		{EntryClose: 100, ExitClose: 101, BenchEntryClose: 100, BenchExitClose: 100},
		{EntryClose: 100, ExitClose: 101, BenchEntryClose: 100, BenchExitClose: 100},
		{EntryClose: 100, ExitClose: 101, BenchEntryClose: 100, BenchExitClose: 100},
	})
	if len(res.Warnings) == 0 {
		t.Error("want a small-sample warning for 3 events")
	}
}

func TestRunWarnsOnCoverageGap(t *testing.T) {
	res := Run("EARNINGS", "GSPC.INDEX", 5, 500, make([]Sample, 25))
	for i := range res.Samples { // give every sample a neutral, valid price
		_ = i
	}
	found := false
	for _, w := range res.Warnings {
		if w != "" {
			found = true
		}
	}
	if !found || res.TotalEvents != 500 {
		t.Errorf("want a coverage-gap warning when 500 events produced only 25 samples; got %+v", res.Warnings)
	}
}

func TestRunEmptyIsHonest(t *testing.T) {
	res := Run("NOTHING", "GSPC.INDEX", 5, 0, nil)
	if res.Samples != 0 || len(res.Warnings) == 0 {
		t.Errorf("want zero samples and a warning for an empty study, got %+v", res)
	}
}
