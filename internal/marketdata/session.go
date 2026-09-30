package marketdata

import "time"

// The US regular session, 09:30-16:00 ET. The cache needs it because a bar's
// lifetime depends on whether it has finished forming: last week's daily bar
// is final, today's changes on every trade. Holidays are not modelled; the
// cost is one extra fetch a minute per charted symbol on a closed day.

// Market is where every covered listing trades.
var Market = mustLoad("America/New_York")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		// Missing tzdata: UTC makes sessions look wrong loudly rather than
		// quietly caching stale prices.
		return time.UTC
	}
	return loc
}

const (
	openMinute  = 9*60 + 30
	closeMinute = 16 * 60
	// settlingWindow keeps refetching briefly after the close, because the
	// settled close is published a little after trading stops.
	settlingWindow = 30 * time.Minute
)

// Location is the timezone session-based indicators bucket by.
func (e Exchange) Location() *time.Location { return Market }

func sessionAt(t time.Time, minute int) time.Time {
	local := t.In(Market)
	return time.Date(local.Year(), local.Month(), local.Day(), minute/60, minute%60, 0, 0, Market)
}

// SessionActive reports whether prices may still be moving, or moved recently
// enough that the last fetch is unlikely to be final.
func (e Exchange) SessionActive(now time.Time) bool {
	switch now.In(Market).Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	return !now.Before(sessionAt(now, openMinute)) && now.Before(sessionAt(now, closeMinute).Add(settlingWindow))
}

// SessionClose is when trading ended on the day a bar belongs to. A daily
// bar's timestamp labels the session, so the close is the honest answer to
// "how old is this price".
func (e Exchange) SessionClose(barTime time.Time) (time.Time, bool) {
	if barTime.IsZero() {
		return time.Time{}, false
	}
	return sessionAt(barTime, closeMinute), true
}
