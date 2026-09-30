package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Daily and weekly checks are anchored to New York, so they track the US
// session whatever DISPLAY_TZ is.
func TestDefaultSchedulesAreAnchoredToNewYork(t *testing.T) {
	for _, iv := range []marketdata.Interval{marketdata.Interval1d, marketdata.Interval1wk} {
		for _, spec := range defaultSchedules()[iv] {
			if !strings.HasPrefix(spec, "CRON_TZ=America/New_York ") {
				t.Errorf("%s spec %q is not anchored to New York", iv, spec)
			}
		}
	}
}

// TestSchedulerStartRegistersEveryDailySpec exercises the real cron parser
// against every spec defaultSchedules() produces -- the single most direct
// regression check for a typo in the cron syntax itself, which unit-testing
// the string alone would not catch.
func TestSchedulerStartRegistersEveryDailySpec(t *testing.T) {
	e := NewEngine(nil, nil, nil, nil, nil)
	s := NewScheduler(e, time.UTC)

	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
}
