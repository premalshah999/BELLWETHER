package news

import (
	"time"
)

// Lane is how urgently a source is polled.
//
// Splitting the catalog into lanes is what keeps a large source list
// affordable. Polling everything at the fastest cadence any source deserves
// would be both wasteful and rude; polling everything at the slowest would
// make exchange filings arrive minutes late, which for filings is the whole
// value. The lane is a property of what the source carries, not of how eager
// we feel.
type Lane string

const (
	// LaneFast is for sources where one new item can matter immediately:
	// exchange filings, regulator orders, breaking wires.
	LaneFast Lane = "fast"
	// LaneNormal is ordinary financial press and discovery.
	LaneNormal Lane = "normal"
	// LaneSlow is material that changes on a quarterly or annual rhythm:
	// shareholding patterns, annual reports, long-form research.
	LaneSlow Lane = "slow"
)

// Base cadences per lane. A source may override with its own Refresh, but the
// lane sets the floor and the ceiling that adaptation moves between.
var laneBounds = map[Lane]struct{ min, max time.Duration }{
	LaneFast:   {30 * time.Second, 5 * time.Minute},
	LaneNormal: {2 * time.Minute, 20 * time.Minute},
	LaneSlow:   {15 * time.Minute, 6 * time.Hour},
}

// Bounds returns the cadence range a lane permits.
func (l Lane) Bounds() (min, max time.Duration) {
	b, ok := laneBounds[l]
	if !ok {
		b = laneBounds[LaneNormal]
	}
	return b.min, b.max
}

// MarketPhase is where the Indian trading day currently is.
//
// Cadence follows the phase because information density does. A filing at
// 15:45 IST moves tomorrow's open; the same filing at 03:00 IST is being read
// by nobody for six hours, and polling for it every thirty seconds spends the
// publisher's bandwidth and ours to learn the same thing later anyway.
type MarketPhase string

const (
	// PhasePreOpen is the run-up to the session, when overnight developments
	// are being priced and the exchange publishes its pre-open data.
	PhasePreOpen MarketPhase = "pre_open"
	// PhaseOpen is the continuous session.
	PhaseOpen MarketPhase = "open"
	// PhasePostClose is the results-and-filings window. It is deliberately
	// treated as aggressively as the session itself: Indian companies file
	// their results after the close, so this is when the highest-value
	// disclosures of the day actually arrive.
	PhasePostClose MarketPhase = "post_close"
	// PhaseOvernight is when Indian sources go quiet.
	PhaseOvernight MarketPhase = "overnight"
	// PhaseWeekend is Saturday and Sunday.
	PhaseWeekend MarketPhase = "weekend"
)

// PhaseAt reports the market phase for an instant, in the given location.
//
// The boundaries are in IST regardless of where the process runs, because they
// describe the Indian trading day rather than the server's timezone.
func PhaseAt(t time.Time, loc *time.Location) MarketPhase {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	switch local.Weekday() {
	case time.Saturday, time.Sunday:
		return PhaseWeekend
	}

	minutes := local.Hour()*60 + local.Minute()
	switch {
	case minutes >= 7*60 && minutes < 9*60+15:
		return PhasePreOpen
	case minutes >= 9*60+15 && minutes < 15*60+30:
		return PhaseOpen
	case minutes >= 15*60+30 && minutes < 21*60:
		return PhasePostClose
	default:
		return PhaseOvernight
	}
}

// cadenceMultiplier scales a source's interval for the current phase.
//
// Values below one mean "poll more often". Indian company sources slow down
// overnight and at weekends; sources whose subject matter is global do not,
// because what happens in Washington or Riyadh at 02:00 IST is exactly what
// moves the Indian open.
func cadenceMultiplier(phase MarketPhase, indian bool) float64 {
	if !indian {
		// Global and geopolitical coverage runs at a steady cadence around
		// the clock. Slowing it overnight would blind the system during the
		// hours when the news that sets the Indian open is actually made.
		switch phase {
		case PhaseWeekend:
			return 1.5
		default:
			return 1
		}
	}
	switch phase {
	case PhasePreOpen, PhaseOpen:
		return 1
	case PhasePostClose:
		// Results and filings land here. This is not a quiet period.
		return 1
	case PhaseOvernight:
		return 4
	case PhaseWeekend:
		return 8
	default:
		return 1
	}
}

// Indian reports whether a source's subject matter is Indian, and therefore
// whether it should quieten outside Indian market hours.
func (s Source) Indian() bool { return s.Country == "IN" }

// Lane classifies a source by how urgent its contents are.
//
// Derived from what the source is rather than stored separately, so the two
// cannot drift apart: an exchange or regulator feed is fast by definition, and
// a quarterly disclosure is slow however often it is republished.
func (s Source) Lane() Lane {
	switch s.Category {
	case "filings", "results", "regulatory", "corporate_actions", "board_meetings":
		return LaneFast
	case "shareholding", "annual_reports", "insider", "circulars":
		return LaneSlow
	}
	if s.Official() {
		return LaneFast
	}
	// A watched company sits in the fast lane. The whole point of a watchlist
	// is that these are the positions where being late matters.
	if s.Watchlist() {
		return LaneFast
	}
	switch s.Category {
	case "markets", "companies", "discovery":
		return LaneNormal
	case "economy", "industry", "money", "finance", "government":
		return LaneNormal
	}
	return LaneNormal
}

// adaptiveInterval computes the next polling interval for a source.
//
// Three inputs combine: the source's configured cadence, how productive recent
// polls have been, and the market phase. A feed that has returned nothing for
// several consecutive polls is asked less often, and one that just produced
// something new is asked more often — which is how a fixed catalog spends its
// effort where things are actually happening.
//
// The result is always clamped to the lane's bounds, so adaptation can never
// turn a filings feed into an hourly one, nor a quarterly disclosure into a
// thirty-second poll.
func adaptiveInterval(src Source, emptyPolls int, phase MarketPhase) time.Duration {
	base := src.Refresh
	if base <= 0 {
		base = 5 * time.Minute
	}

	// Back off gently: each unproductive poll adds a quarter of the base
	// interval, so a feed that is genuinely quiet drifts towards its ceiling
	// rather than jumping there and missing the moment it wakes up.
	if emptyPolls > 0 {
		grow := 1 + 0.25*float64(min(emptyPolls, 8))
		base = time.Duration(float64(base) * grow)
	}

	base = time.Duration(float64(base) * cadenceMultiplier(phase, src.Indian()))

	lo, hi := src.Lane().Bounds()
	if base < lo {
		base = lo
	}
	if base > hi {
		base = hi
	}
	return base
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
