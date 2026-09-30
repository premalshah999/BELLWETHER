package news

import (
	"strings"
	"sync"
	"time"
)

// Heat is how much attention an instrument currently deserves.
type Heat string

const (
	HeatNormal Heat = "normal"
	HeatWarm   Heat = "warm"
	HeatHot    Heat = "hot"
)

// Heat thresholds and decay.
//
// A score is added to when something happens and decays continuously, so an
// instrument cools on its own rather than needing a sweep to reset it. The
// half-life is set so that a single filing keeps a company warm for most of a
// trading session and hot for the half hour when follow-up coverage actually
// arrives.
const (
	hotThreshold  = 6.0
	warmThreshold = 2.0
	heatHalfLife  = 45 * time.Minute
	// heatCeiling stops a company in a genuine storm of news from
	// accumulating a score that takes days to decay.
	heatCeiling = 20.0
)

// Tracker records which instruments and sectors are currently eventful, so
// polling effort goes where something is happening: a company with a fresh
// filing is worth checking far more often, for about an hour. Safe for
// concurrent use.
type Tracker struct {
	mu      sync.RWMutex
	symbols map[string]*heatEntry
	sectors map[string]*heatEntry
	now     func() time.Time
}

type heatEntry struct {
	score     float64
	updatedAt time.Time
	// reason records what last raised this, so an operator looking at the
	// hot list can see why rather than only that.
	reason string
}

// NewTracker builds an empty tracker.
func NewTracker(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{
		symbols: map[string]*heatEntry{},
		sectors: map[string]*heatEntry{},
		now:     now,
	}
}

// Weights for the kinds of thing that raise attention.
//
// An exchange-confirmed filing outweighs a news report of the same subject,
// because the filing is the event and the report is coverage of it. Importance
// scales the whole thing, so a routine disclosure barely moves the needle.
const (
	weightOfficial = 1.0
	weightReported = 0.4
)

// ObserveSignal raises heat by an explicit weight. Observe derives its weight
// from an article's importance, which accumulates over a morning of coverage;
// a market signal arrives once and carries its whole meaning in that
// observation.
func (t *Tracker) ObserveSignal(symbols []string, sectors []string, weight float64, reason string) {
	if weight <= 0 {
		return
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, sym := range symbols {
		t.bump(t.symbols, strings.ToUpper(sym), weight, now, reason)
	}
	for _, sec := range sectors {
		t.bump(t.sectors, sec, weight*0.35, now, reason)
	}
}

func (t *Tracker) Observe(symbols []string, sectors []string, importance int, official bool, reason string) {
	if importance <= 0 {
		return
	}
	weight := weightReported
	if official {
		weight = weightOfficial
	}
	// Importance is 0-10; normalising against the midpoint means a 5 is
	// neutral, a 9 is strongly elevating and a 2 barely registers.
	delta := weight * (float64(importance) / 5.0)

	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, sym := range symbols {
		t.bump(t.symbols, strings.ToUpper(sym), delta, now, reason)
	}
	// Sector heat rises more slowly than company heat: a single company's
	// order win says little about its industry, and letting it promote the
	// whole sector would elevate a hundred companies on one data point.
	for _, sec := range sectors {
		t.bump(t.sectors, sec, delta*0.35, now, reason)
	}
}

func (t *Tracker) bump(m map[string]*heatEntry, key string, delta float64, now time.Time, reason string) {
	if key == "" {
		return
	}
	e, ok := m[key]
	if !ok {
		e = &heatEntry{updatedAt: now}
		m[key] = e
	}
	e.score = decay(e.score, e.updatedAt, now) + delta
	if e.score > heatCeiling {
		e.score = heatCeiling
	}
	e.updatedAt = now
	if reason != "" {
		e.reason = reason
	}
}

// decay applies exponential decay between two instants.
//
// Continuous decay rather than a scheduled reset means heat is correct
// whenever it is read, with no sweep to run and nothing to go stale if the
// sweep is missed.
func decay(score float64, from, to time.Time) float64 {
	if score <= 0 || !to.After(from) {
		return score
	}
	elapsed := to.Sub(from)
	halfLives := float64(elapsed) / float64(heatHalfLife)
	// 2^-n, computed without importing math for one call.
	factor := 1.0
	whole := int(halfLives)
	for i := 0; i < whole && i < 32; i++ {
		factor /= 2
	}
	frac := halfLives - float64(whole)
	// Linear interpolation across the final half-life is close enough for a
	// scheduling heuristic and avoids a transcendental in a hot path.
	factor *= 1 - 0.5*frac
	return score * factor
}

// SymbolHeat reports the current heat of one instrument.
func (t *Tracker) SymbolHeat(symbol string) Heat {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return classify(t.current(t.symbols, strings.ToUpper(symbol)))
}

func (t *Tracker) current(m map[string]*heatEntry, key string) float64 {
	e, ok := m[key]
	if !ok {
		return 0
	}
	return decay(e.score, e.updatedAt, t.now())
}

func classify(score float64) Heat {
	switch {
	case score >= hotThreshold:
		return HeatHot
	case score >= warmThreshold:
		return HeatWarm
	default:
		return HeatNormal
	}
}

// HotEntry is one elevated instrument or sector, for the API and for the
// scheduler.
type HotEntry struct {
	Key    string  `json:"key"`
	Score  float64 `json:"score"`
	Heat   Heat    `json:"heat"`
	Reason string  `json:"reason,omitempty"`
	Since  string  `json:"since,omitempty"`
}

// Sweep drops entries that have decayed to nothing.
//
// Not required for correctness — decay is applied on read — but without it the
// maps grow to hold every symbol ever mentioned, each with a score of
// effectively zero.
func (t *Tracker) Sweep() (removed int) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, m := range []map[string]*heatEntry{t.symbols, t.sectors} {
		for key, e := range m {
			if decay(e.score, e.updatedAt, now) < 0.05 {
				delete(m, key)
				removed++
			}
		}
	}
	return removed
}

// Multiplier scales a polling interval for a source that covers hot ground.
//
// A source is checked up to three times as often when it covers an instrument
// that is currently eventful. The bound matters: without it, a busy day would
// have the whole catalog polling at its floor and the politeness that keeps
// these feeds available would be the first thing lost.
func (h Heat) Multiplier() float64 {
	switch h {
	case HeatHot:
		return 1.0 / 3.0
	case HeatWarm:
		return 1.0 / 1.5
	default:
		return 1
	}
}
