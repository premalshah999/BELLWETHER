package marketdata

import (
	"testing"
	"time"
)

// A daily chart loaded mid-session was served from a six-hour cache, so it
// showed the morning's price all afternoon while reporting itself fresh.
func TestFormingBarIsNotCachedForHours(t *testing.T) {
	midSession := time.Date(2026, 8, 26, 12, 52, 0, 0, Market)
	overnight := time.Date(2026, 8, 26, 22, 0, 0, 0, Market)
	aapl := Symbol{Ticker: "AAPL"}

	r := &Router{ttl: defaultTTLs(), now: func() time.Time { return midSession }}
	if got := r.ttlForSymbol(aapl, Interval1d); got > liveTTL {
		t.Errorf("daily TTL during a session = %v, want no more than %v", got, liveTTL)
	}
	// Intervals that were already short are left alone.
	if base := r.ttlFor(Interval1m); r.ttlForSymbol(aapl, Interval1m) != base || base > liveTTL {
		t.Errorf("1m TTL during a session = %v, want %v and within %v", r.ttlForSymbol(aapl, Interval1m), base, liveTTL)
	}

	r.now = func() time.Time { return overnight }
	if got := r.ttlForSymbol(aapl, Interval1d); got != 6*time.Hour {
		t.Errorf("daily TTL overnight = %v, want the full 6h once the bar has settled", got)
	}
}

// The UI polls a visible price every minute; a cache lifetime at or above
// that returns identical bytes on every other poll.
func TestQuoteTTLBeatsThePoll(t *testing.T) {
	aapl := Symbol{Ticker: "AAPL"}
	r := &Router{ttl: defaultTTLs(), now: func() time.Time { return time.Date(2026, 8, 26, 12, 52, 0, 0, Market) }}
	if got := r.quoteTTL(aapl); got >= time.Minute {
		t.Errorf("live quote TTL = %v, want shorter than the one-minute poll", got)
	}
	r.now = func() time.Time { return time.Date(2026, 8, 26, 22, 0, 0, 0, Market) }
	if got := r.quoteTTL(aapl); got != QuoteTTL {
		t.Errorf("overnight quote TTL = %v, want %v", got, QuoteTTL)
	}
}
