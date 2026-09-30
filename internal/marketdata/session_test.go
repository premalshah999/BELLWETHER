package marketdata

import (
	"testing"
	"time"
)

func et(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02 15:04", s, Market)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestSessionActive(t *testing.T) {
	cases := []struct {
		when string
		want bool
		why  string
	}{
		{"2026-08-26 09:00", false, "before the open"},
		{"2026-08-26 09:30", true, "at the open"},
		{"2026-08-26 12:52", true, "midday, when a six-hour cache used to freeze the day"},
		{"2026-08-26 15:59", true, "a minute before the close"},
		{"2026-08-26 16:15", true, "inside the settling window"},
		{"2026-08-26 16:45", false, "after the settling window"},
		{"2026-08-26 23:00", false, "overnight"},
		{"2026-08-29 12:00", false, "Saturday"},
		{"2026-08-30 12:00", false, "Sunday"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			if got := ExchangeUS.SessionActive(et(t, tc.when)); got != tc.want {
				t.Errorf("SessionActive(%s) = %v, want %v", tc.when, got, tc.want)
			}
		})
	}
}

func TestSessionClose(t *testing.T) {
	// A daily bar stamped at midnight UTC belongs to that day's session.
	bar := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	if got, _ := ExchangeUS.SessionClose(bar); !got.Equal(et(t, "2026-08-26 16:00")) {
		t.Errorf("SessionClose = %v, want 16:00 ET that day", got)
	}
}
