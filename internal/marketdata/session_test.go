package marketdata

import (
	"testing"
	"time"
)

func ist(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestNSESessionActive(t *testing.T) {
	cases := []struct {
		when string
		want bool
		why  string
	}{
		{"2026-08-26 08:00", false, "before the open"},
		{"2026-08-26 09:15", true, "at the open"},
		{"2026-08-26 12:52", true, "midday — the moment a six-hour cache used to freeze the day"},
		{"2026-08-26 15:29", true, "a minute before the close"},
		{"2026-08-26 15:45", true, "inside the settling window, when the close is still landing"},
		{"2026-08-26 16:30", false, "after the settling window"},
		{"2026-08-26 23:00", false, "overnight"},
		{"2026-08-29 12:00", false, "Saturday"},
		{"2026-08-30 12:00", false, "Sunday"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			if got := ExchangeNSE.SessionActive(ist(t, tc.when)); got != tc.want {
				t.Errorf("SessionActive(%s) = %v, want %v", tc.when, got, tc.want)
			}
		})
	}
}

// TestVenuesDifferOnTheSameInstant: an Indian and a US instrument are not
// both live at the same moment, and a single global "is the market open"
// would keep one of them stale for hours.
func TestVenuesDifferOnTheSameInstant(t *testing.T) {
	noon := ist(t, "2026-08-26 12:00") // 02:30 ET — US shut, NSE trading
	if !ExchangeNSE.SessionActive(noon) {
		t.Error("NSE should be trading at midday IST")
	}
	if ExchangeUS.SessionActive(noon) {
		t.Error("US markets are not open at 02:30 ET")
	}

	evening := ist(t, "2026-08-26 21:00") // 11:30 ET — US trading, NSE shut
	if ExchangeNSE.SessionActive(evening) {
		t.Error("NSE is closed at 21:00 IST")
	}
	if !ExchangeUS.SessionActive(evening) {
		t.Error("US markets are trading at 11:30 ET")
	}
}

func TestExchangeLocation(t *testing.T) {
	if got := ExchangeNSE.Location().String(); got != "Asia/Kolkata" {
		t.Errorf("NSE location = %s, want Asia/Kolkata", got)
	}
	if got := ExchangeBSE.Location().String(); got != "Asia/Kolkata" {
		t.Errorf("BSE location = %s, want Asia/Kolkata", got)
	}
	if got := ExchangeUS.Location().String(); got != "America/New_York" {
		t.Errorf("US location = %s, want America/New_York", got)
	}
	// A benchmark index has no venue of its own; it defaults to US hours,
	// which is where the major indices this app cares about (^GSPC, ^VIX)
	// actually trade.
	if got := ExchangeIndex.Location().String(); got != "America/New_York" {
		t.Errorf("index location = %s, want America/New_York", got)
	}
}
