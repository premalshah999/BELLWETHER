package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// defaultSchedules is when each interval's algorithms are evaluated: not as
// often as the interval (a daily rule needs no minute checks, and evaluating
// intraday rules every minute keeps the price cache cold). Daily and weekly
// checks are pinned to New York with CRON_TZ, so they land before the open,
// mid-session and before the close whatever DISPLAY_TZ is and through
// daylight-saving changes; cooldowns make the repeat checks free.
func defaultSchedules() map[marketdata.Interval][]string {
	return map[marketdata.Interval][]string{
		marketdata.Interval1m:  {"*/2 * * * *"},
		marketdata.Interval5m:  {"*/5 * * * *"},
		marketdata.Interval15m: {"*/15 * * * *"},
		marketdata.Interval1h:  {"5 * * * *"},
		marketdata.Interval1d:  {"CRON_TZ=America/New_York 15 8,11,15 * * 1-5"},
		marketdata.Interval1wk: {"CRON_TZ=America/New_York 30 8 * * 1"},
	}
}

// Scheduler drives periodic evaluation.
type Scheduler struct {
	engine    *Engine
	cron      *cron.Cron
	loc       *time.Location
	log       *slog.Logger
	schedules map[marketdata.Interval][]string

	mu      sync.Mutex
	started bool
	// lastRun records the most recent pass per interval, for the settings page.
	lastRun map[marketdata.Interval]RunSummary
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithSchedulerLogger sets the logger.
func WithSchedulerLogger(l *slog.Logger) SchedulerOption {
	return func(s *Scheduler) { s.log = l }
}

// NewScheduler builds a scheduler running in the given location.
func NewScheduler(engine *Engine, loc *time.Location, opts ...SchedulerOption) *Scheduler {
	if loc == nil {
		loc = time.UTC
	}
	s := &Scheduler{
		engine:    engine,
		cron:      cron.New(cron.WithLocation(loc)),
		loc:       loc,
		log:       slog.Default(),
		schedules: defaultSchedules(),
		lastRun:   map[marketdata.Interval]RunSummary{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Start registers every schedule and begins running them.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("scheduler already started")
	}

	for iv, specs := range s.schedules {
		for _, spec := range specs {
			iv, spec := iv, spec
			if _, err := s.cron.AddFunc(spec, func() {
				// Each pass gets its own bounded context: a stuck provider must
				// not wedge the schedule permanently.
				runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()

				summary, err := s.engine.RunInterval(runCtx, iv)
				if err != nil {
					s.log.Error("scheduled evaluation failed", "interval", iv, "err", err)
					return
				}
				s.mu.Lock()
				s.lastRun[iv] = summary
				s.mu.Unlock()
			}); err != nil {
				return fmt.Errorf("schedule %q for interval %s: %w", spec, iv, err)
			}
			s.log.Info("scheduled algorithm evaluation", "interval", iv, "cron", spec)
		}
	}

	s.cron.Start()
	s.started = true
	return nil
}

// Stop halts the scheduler and waits for any running pass to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	started := s.started
	s.started = false
	s.mu.Unlock()

	if !started {
		return
	}
	<-s.cron.Stop().Done()
}

// Schedules reports the configured cron expression per interval.
func (s *Scheduler) Schedules() map[string][]string {
	out := make(map[string][]string, len(s.schedules))
	for iv, specs := range s.schedules {
		out[string(iv)] = append([]string(nil), specs...)
	}
	return out
}

// LastRuns reports the most recent pass per interval.
func (s *Scheduler) LastRuns() map[string]RunSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]RunSummary, len(s.lastRun))
	for iv, summary := range s.lastRun {
		out[string(iv)] = summary
	}
	return out
}

// NextRuns reports when each interval's earliest check next fires, for the
// settings page.
func (s *Scheduler) NextRuns() map[string]time.Time {
	out := map[string]time.Time{}
	for iv, specs := range s.schedules {
		var earliest time.Time
		for _, spec := range specs {
			next, err := nextRunFor(spec, s.loc)
			if err != nil {
				s.log.Warn("could not compute next run", "interval", iv, "cron", spec, "err", err)
				continue
			}
			if earliest.IsZero() || next.Before(earliest) {
				earliest = next
			}
		}
		if !earliest.IsZero() {
			out[string(iv)] = earliest.UTC()
		}
	}
	return out
}

// nextRunFor computes when a single cron spec next fires. A spec carrying
// its own CRON_TZ=<zone> prefix is evaluated in that zone instead of
// defaultLoc, matching how robfig/cron itself resolves a per-job override --
// this is what keeps a US-anchored check landing at a fixed point in that
// market's session (see defaultSchedules) rather than drifting against
// defaultLoc's own untouched offset.
func nextRunFor(spec string, defaultLoc *time.Location) (time.Time, error) {
	loc := defaultLoc
	if rest, ok := strings.CutPrefix(spec, "CRON_TZ="); ok {
		zone, fields, ok := strings.Cut(rest, " ")
		if !ok {
			return time.Time{}, fmt.Errorf("malformed CRON_TZ spec %q", spec)
		}
		z, err := time.LoadLocation(zone)
		if err != nil {
			return time.Time{}, fmt.Errorf("load zone %q: %w", zone, err)
		}
		loc, spec = z, fields
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(spec)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(time.Now().In(loc)), nil
}
