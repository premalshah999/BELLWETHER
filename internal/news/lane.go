package news

import (
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
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

// MarketPhase is where the US trading day currently is, reported on the
// data-sources page so stale news can be told apart from a quiet hour.
type MarketPhase string

const (
	PhasePreOpen   MarketPhase = "pre_open"   // 04:00-09:30 ET, pre-market
	PhaseOpen      MarketPhase = "open"       // the regular session
	PhasePostClose MarketPhase = "post_close" // 16:00-20:00 ET, when earnings land
	PhaseOvernight MarketPhase = "overnight"
	PhaseWeekend   MarketPhase = "weekend"
)

// PhaseAt reports the phase of the US trading day at t.
func PhaseAt(t time.Time) MarketPhase {
	local := t.In(marketdata.Market)
	if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return PhaseWeekend
	}
	switch m := local.Hour()*60 + local.Minute(); {
	case m >= 4*60 && m < 9*60+30:
		return PhasePreOpen
	case m >= 9*60+30 && m < 16*60:
		return PhaseOpen
	case m >= 16*60 && m < 20*60:
		return PhasePostClose
	}
	return PhaseOvernight
}

// cadenceMultiplier scales a source's interval for the phase. Coverage runs
// around the clock, because what happens overnight sets the open; only the
// weekend eases off.
func cadenceMultiplier(phase MarketPhase) float64 {
	if phase == PhaseWeekend {
		return 1.5
	}
	return 1
}

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

// adaptiveInterval computes the next polling interval from the source's
// cadence, how productive recent polls were, and the market phase, clamped to
// the lane's bounds so adaptation can never make a filings feed hourly.
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

	base = time.Duration(float64(base) * cadenceMultiplier(phase))

	lo, hi := src.Lane().Bounds()
	if base < lo {
		base = lo
	}
	if base > hi {
		base = hi
	}
	return base
}
