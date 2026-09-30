package alerts

import (
	"testing"
)

// TestSchedulerStartRegistersEveryDailySpec exercises the real cron parser
// against every spec defaultSchedules() produces -- the single most direct
// regression check for a typo in the cron syntax itself, which unit-testing
// the string alone would not catch.
func TestSchedulerStartRegistersEveryDailySpec(t *testing.T) {
	e := NewEngine(nil, nil, nil, nil, nil)
	s := NewScheduler(e)

	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
}
