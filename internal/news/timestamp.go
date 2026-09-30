package news

import "time"

// TimestampTrust says how much a source's published_at can be believed. A
// filing's stamp is exact and a publisher's own feed is close, but an
// aggregator's is when it surfaced the item: Google News stamped a March
// article with an August date. Recording which kind it is lets the interface
// say "published" only when that is known.
type TimestampTrust string

const (
	// TrustExact means the timestamp is the event's own, from the party that
	// created it. Exchange filings and regulator releases.
	TimestampExact TimestampTrust = "exact"
	// TimestampPublisher means the publisher's own feed stated it. Normally
	// right, occasionally rounded to the hour, and worth believing.
	TimestampPublisher TimestampTrust = "publisher"
	// TimestampObserved means the value is when some intermediary saw the
	// item. It is an upper bound on publication, never the publication time,
	// and it can be wildly later than the truth.
	TimestampObserved TimestampTrust = "observed"
)

// Reliable reports whether a timestamp of this kind may be presented as a
// publication time.
func (t TimestampTrust) Reliable() bool { return t != TimestampObserved }

// TimestampTrust classifies a source's published_at.
func (s Source) TimestampTrust() TimestampTrust {
	switch s.Method {
	case MethodGoogleNews:
		// The whole reason this type exists.
		return TimestampObserved
	case MethodGDELT:
		// GDELT reports a crawl time, which is explicitly an observation.
		return TimestampObserved
	}
	if s.Official() {
		return TimestampExact
	}
	return TimestampPublisher
}

// clockSkewTolerance is how far a source's clock may run ahead of ours before
// its timestamp is rejected. Feeds are commonly a minute or two out; four
// hours, as observed, is not clock skew.
const clockSkewTolerance = 5 * time.Minute

// ValidatePublished checks a claimed publication time against when we saw it.
//
// A publication time later than our own fetch is impossible: we cannot hold
// bytes that have not been published. Rather than storing the impossible value
// and hoping a consumer notices, the timestamp is discarded and the reason
// returned, so an item with an unusable stamp is undated rather than wrongly
// dated. Undated sorts to the bottom; wrongly dated sorts to the top.
func ValidatePublished(published, discovered time.Time, trust TimestampTrust) (time.Time, string) {
	if published.IsZero() {
		return time.Time{}, ""
	}
	if discovered.IsZero() {
		return published, ""
	}
	if published.After(discovered.Add(clockSkewTolerance)) {
		return time.Time{}, "published_at postdates discovery"
	}
	// An observed timestamp equal to the observation is no information at
	// all: it says the aggregator noticed it when it noticed it.
	if !trust.Reliable() && published.After(discovered.Add(-time.Minute)) {
		return time.Time{}, "aggregator reported its own observation time"
	}
	return published, ""
}

// Staleness is how old an item's content is, as distinct from how recently we
// found it.
//
// The two diverge constantly and the difference matters. An aggregator
// resurfaces a July article in August; we discover it today, and sorted by
// discovery it sits at the top of a feed next to this morning's news looking
// exactly like it. A reader glancing at "RIL Q1 Profit Falls 22%" has no way
// to know it is five weeks old unless the interface says so.
type Staleness string

const (
	// StalenessFresh is same-day content.
	StalenessFresh Staleness = "fresh"
	// StalenessRecent is within the working week.
	StalenessRecent Staleness = "recent"
	// StalenessOld is older than a week: still worth showing, but never
	// without saying how old it is.
	StalenessOld Staleness = "old"
	// StalenessUnknown is where no trustworthy publication time exists.
	// Distinct from fresh: not knowing is not the same as knowing it is new.
	StalenessUnknown Staleness = "unknown"
)

// StalenessAt classifies content age at a given moment.
func StalenessAt(published time.Time, trust TimestampTrust, now time.Time) Staleness {
	if published.IsZero() {
		return StalenessUnknown
	}
	age := now.Sub(published)
	switch {
	case age < 0:
		// Dated in the future. Nothing sensible can be said about its age.
		return StalenessUnknown
	case age < 24*time.Hour:
		return StalenessFresh
	case age < 7*24*time.Hour:
		return StalenessRecent
	default:
		return StalenessOld
	}
}

// Stale reports whether an item's content predates the window a reader would
// assume from its position in a feed.
func (s Staleness) Stale() bool { return s == StalenessOld }
