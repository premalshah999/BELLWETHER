package eventstudy

import (
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// session is midnight New York on a January weekday, the way daily bars are
// stamped; its close is 16:00 ET that day.
func session(d int) time.Time {
	return time.Date(2026, 1, d, 0, 0, 0, 0, marketdata.Market)
}

func bars(closes ...float64) []marketdata.Candle {
	out := make([]marketdata.Candle, len(closes))
	for i, c := range closes {
		out[i] = marketdata.Candle{Time: session(i + 1), Close: c}
	}
	return out
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if got < want-0.01 || got > want+0.01 {
		t.Errorf("%s = %.3f, want %.3f", name, got, want)
	}
}

// News at 10:00 ET on day 2 is known before day 2's close, so the reaction
// runs from day 1's close to day 2's and the drift starts at day 2's close.
func TestBuildSampleAnchorsOnTheSessionClose(t *testing.T) {
	stock := bars(100, 110, 121, 121)
	bench := bars(100, 101, 101, 101)
	at := session(2).Add(10 * time.Hour)

	s, ok := BuildSample("AAPL", at, 1, stock, bench)
	if !ok {
		t.Fatal("BuildSample: want ok")
	}
	near(t, "reaction", s.ReactionPct(), 10-1)
	near(t, "drift", s.AbnormalReturnPct(), 10)
}

// After the close the news is priced the next session, never the one that
// had already ended.
func TestNewsAfterTheCloseReactsTheNextSession(t *testing.T) {
	stock := bars(100, 100, 90, 90)
	bench := bars(100, 100, 100, 100)
	at := session(2).Add(16*time.Hour + 5*time.Minute)

	s, ok := BuildSample("AAPL", at, 1, stock, bench)
	if !ok {
		t.Fatal("BuildSample: want ok")
	}
	near(t, "reaction", s.ReactionPct(), -10)
	if s.EntryClose != 90 || s.PreClose != 100 {
		t.Errorf("pre/entry = %v/%v, want 100/90", s.PreClose, s.EntryClose)
	}
}

// A window the series cannot see is excluded, never guessed.
func TestBuildSampleRejectsInsufficientHistory(t *testing.T) {
	at := session(1).Add(10 * time.Hour)
	if _, ok := BuildSample("AAPL", at, 1, bars(100, 105, 110), bars(100, 101, 102)); ok {
		t.Error("an event on the first bar has no close before it")
	}
	at = session(2).Add(10 * time.Hour)
	if _, ok := BuildSample("AAPL", at, 5, bars(100, 105, 110), bars(100, 101, 102, 103, 104, 105, 106, 107)); ok {
		t.Error("want !ok when the stock's series does not reach the exit bar")
	}
	if _, ok := BuildSample("AAPL", at, 5, bars(100, 105, 110, 115, 120, 125, 130, 135), bars(100, 101, 102)); ok {
		t.Error("want !ok when the benchmark does not reach the exit bar")
	}
	if _, ok := BuildSample("AAPL", at, 0, bars(100, 105, 110), bars(100, 101, 102)); ok {
		t.Error("want !ok for a zero-day window")
	}
}

func TestRunSplitsGroupsInTheOrderAsked(t *testing.T) {
	mk := func(group string, entry, exit float64) Sample {
		return Sample{Group: group, At: session(2), PreClose: 100, EntryClose: entry, ExitClose: exit,
			BenchPreClose: 100, BenchEntryClose: 100, BenchExitClose: 100}
	}
	res := Run("EARNINGS_SURPRISE", "S&P 500", 5, 4, []Sample{
		mk("miss", 90, 88), mk("beat", 110, 111), mk("beat", 106, 107), mk("in line", 100, 100),
	}, "beat", "in line", "miss")

	if len(res.Groups) != 3 || res.Groups[0].Label != "beat" || res.Groups[2].Label != "miss" {
		t.Fatalf("groups = %+v", res.Groups)
	}
	near(t, "beat reaction", res.Groups[0].Reaction.Mean, 8)
	near(t, "miss reaction", res.Groups[2].Reaction.Mean, -10)
	near(t, "abs reaction", res.AbsReaction, (10+10+6+0)/4.0)
	if res.Samples != 4 || res.Since == nil {
		t.Errorf("samples=%d since=%v", res.Samples, res.Since)
	}
}

func TestRunWarnsOnSmallSamplesAndCoverage(t *testing.T) {
	s := Sample{At: session(2), PreClose: 100, EntryClose: 101, ExitClose: 102,
		BenchPreClose: 100, BenchEntryClose: 100, BenchExitClose: 100}
	res := Run("X", "S&P 500", 5, 10, []Sample{s})
	if len(res.Warnings) != 2 {
		t.Errorf("warnings = %v, want small-sample and coverage", res.Warnings)
	}
	if empty := Run("X", "S&P 500", 5, 3, nil); empty.Samples != 0 || len(empty.Warnings) != 1 {
		t.Errorf("empty = %+v", empty)
	}
}

func TestStatsT(t *testing.T) {
	st := statsOf([]float64{1, 2, 3, 4, 5})
	near(t, "mean", st.Mean, 3)
	near(t, "t", st.T, 3/(1.5811/2.2361))
	near(t, "hit rate", st.HitRate, 100)
}
