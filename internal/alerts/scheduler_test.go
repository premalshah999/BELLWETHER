package alerts

import (
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// TestDefaultSchedulesDailyCoversBothVenues is the regression for the whole
// reason defaultSchedules moved from one spec per interval to several: a
// single fixed-zone daily check cannot track two markets whose sessions
// don't share a timezone (NSE's IST session and the US's DST-shifting ET
// one), and a user-defined algorithm can legitimately name symbols on both
// venues at once, so the fix widens the check set rather than splitting it
// by venue the way the market scanner's cron does.
func TestDefaultSchedulesDailyCoversBothVenues(t *testing.T) {
	specs := defaultSchedules()[marketdata.Interval1d]
	if len(specs) < 2 {
		t.Fatalf("Interval1d has %d spec(s), want at least 2 (NSE-tuned and US-tuned)", len(specs))
	}
	var haveNSE, haveUS bool
	for _, s := range specs {
		if s == "CRON_TZ=America/New_York 15 8,11,15 * * 1-5" {
			haveUS = true
		}
		if !haveUS && s != "" {
			haveNSE = true // the bare (DISPLAY_TZ) spec
		}
	}
	if !haveUS {
		t.Errorf("specs = %v, want a CRON_TZ=America/New_York entry", specs)
	}
	if !haveNSE {
		t.Errorf("specs = %v, want a plain (DISPLAY_TZ) entry too", specs)
	}
}

// TestNextRunForHonoursCRON_TZ is the regression for the settings-page
// reporting path: a spec's own CRON_TZ prefix must be evaluated in that
// zone, not the scheduler's default display timezone, or a US-anchored
// check would report a "next run" computed against the wrong clock.
func TestNextRunForHonoursCRON_TZ(t *testing.T) {
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	// A plain spec uses the default location.
	plain, err := nextRunFor("15 8,11,15 * * *", ist)
	if err != nil {
		t.Fatalf("nextRunFor(plain): %v", err)
	}
	if h := plain.In(ist).Hour(); h != 8 && h != 11 && h != 15 {
		t.Errorf("plain spec's next run is %02d:00 IST, want 08, 11 or 15", h)
	}

	// The same clock-times, but CRON_TZ-anchored to New York -- the computed
	// instant must land at 08/11/15 in New York, not in the default zone.
	tz, err := nextRunFor("CRON_TZ=America/New_York 15 8,11,15 * * *", ist)
	if err != nil {
		t.Fatalf("nextRunFor(CRON_TZ): %v", err)
	}
	if h := tz.In(ny).Hour(); h != 8 && h != 11 && h != 15 {
		t.Errorf("CRON_TZ spec's next run is %02d:00 in New York, want 08, 11 or 15", h)
	}
	// And it must genuinely differ from interpreting the same clock-face
	// numbers in the default zone -- otherwise the prefix was silently
	// ignored.
	untagged, err := nextRunFor("15 8,11,15 * * *", ny)
	if err != nil {
		t.Fatal(err)
	}
	if !tz.Equal(untagged) {
		// Both should compute the same instant: one via the prefix, one via
		// passing New York as the default directly.
		t.Errorf("CRON_TZ=America/New_York next run = %v, want to match nextRunFor with ny as the default (%v)", tz, untagged)
	}
}

func TestNextRunForRejectsMalformedCRON_TZ(t *testing.T) {
	if _, err := nextRunFor("CRON_TZ=NotAZone", time.UTC); err == nil {
		t.Error("want an error for a CRON_TZ spec with no fields after the zone")
	}
	if _, err := nextRunFor("CRON_TZ=Not/AZone 15 8 * * *", time.UTC); err == nil {
		t.Error("want an error for an unloadable zone name")
	}
}

// TestSchedulerStartRegistersEveryDailySpec exercises the real cron parser
// against every spec defaultSchedules() produces -- the single most direct
// regression check for a typo in the cron syntax itself, which unit-testing
// the string alone would not catch.
func TestSchedulerStartRegistersEveryDailySpec(t *testing.T) {
	e := NewEngine(nil, nil, nil, nil, nil)
	loc, _ := time.LoadLocation("Asia/Kolkata")
	s := NewScheduler(e, loc)

	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	got := s.Schedules()
	want := defaultSchedules()
	if len(got[string(marketdata.Interval1d)]) != len(want[marketdata.Interval1d]) {
		t.Errorf("Schedules()[1d] = %v, want %v", got[string(marketdata.Interval1d)], want[marketdata.Interval1d])
	}

	next := s.NextRuns()
	if _, ok := next[string(marketdata.Interval1d)]; !ok {
		t.Error("NextRuns() has no entry for 1d")
	}
}
