package marketdata

import (
	"testing"
	"time"
)

// TestFormingBarIsNotCachedForHours is the regression that prompted this
// code. A daily chart loaded during a session was served from a six-hour
// cache, so it showed the morning's price all afternoon — a rupee value
// several rupees off and less than half the day's real volume — while
// reporting itself as fresh.
func TestFormingBarIsNotCachedForHours(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	midSession := time.Date(2026, 8, 26, 12, 52, 0, 0, loc)
	overnight := time.Date(2026, 8, 26, 22, 0, 0, 0, loc)

	nse := Symbol{Ticker: "RELIANCE", Exchange: ExchangeNSE}

	r := &Router{ttl: defaultTTLs(), now: func() time.Time { return midSession }}
	if got := r.ttlForSymbol(nse, Interval1d); got > liveTTL {
		t.Errorf("daily TTL during a session = %v, want no more than %v", got, liveTTL)
	}

	r.now = func() time.Time { return overnight }
	if got := r.ttlForSymbol(nse, Interval1d); got != 6*time.Hour {
		t.Errorf("daily TTL overnight = %v, want the full 6h once the bar has settled", got)
	}

	// Intervals that were already short must not be lengthened by any of
	// this. Asserted as an invariant rather than a literal: minute bars are
	// the live quote path during a session, so their lifetime is tuned
	// against how fresh a streamed price needs to be, and pinning the exact
	// value here would make that a two-file change for no benefit.
	r.now = func() time.Time { return midSession }
	base := r.ttlFor(Interval1m)
	if got := r.ttlForSymbol(nse, Interval1m); got != base {
		t.Errorf("1m TTL during a session = %v, want it left at %v", got, base)
	}
	if base > liveTTL {
		t.Errorf("1m TTL = %v, longer than the %v live ceiling", base, liveTTL)
	}

	// A US instrument at midday IST is outside its session and keeps the long
	// lifetime, on the same clock tick that NSE gets the short one.
	us := Symbol{Ticker: "AAPL", Exchange: ExchangeUS}
	if got := r.ttlForSymbol(us, Interval1d); got != 6*time.Hour {
		t.Errorf("US daily TTL at 12:52 IST = %v, want the full 6h", got)
	}
}

// TestQuoteTTLBeatsThePoll: the front end polls a visible price every sixty
// seconds. A cache lifetime at or above that interval means every second poll
// returns identical bytes and the number sits still while the market moves.
func TestQuoteTTLBeatsThePoll(t *testing.T) {
	const uiPollInterval = 60 * time.Second

	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	midSession := time.Date(2026, 8, 26, 12, 52, 0, 0, loc)
	overnight := time.Date(2026, 8, 26, 22, 0, 0, 0, loc)
	nse := Symbol{Ticker: "RELIANCE", Exchange: ExchangeNSE}

	r := &Router{ttl: defaultTTLs(), now: func() time.Time { return midSession }}
	if got := r.quoteTTL(nse); got >= uiPollInterval {
		t.Errorf("live quote TTL = %v, which is not shorter than the %v poll", got, uiPollInterval)
	}

	r.now = func() time.Time { return overnight }
	if got := r.quoteTTL(nse); got != QuoteTTL {
		t.Errorf("overnight quote TTL = %v, want %v — nothing is trading, so nothing needs re-asking", got, QuoteTTL)
	}
}
