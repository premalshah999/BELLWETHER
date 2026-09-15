package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// defaultSchedules map an interval to a cron expression.
//
// These are deliberately not "as often as the interval". A daily algorithm
// does not need evaluating every minute, and evaluating an intraday one every
// minute would keep the market data cache permanently cold. Cooldowns then
// prevent repeat notifications regardless of how often a rule is checked.
//
// Expressions are interpreted in the operator's display timezone, so "30 3 * * *"
// means 03:30 IST, not 03:30 UTC.
func defaultSchedules() map[marketdata.Interval]string {
	return map[marketdata.Interval]string{
		marketdata.Interval1m:  "*/2 * * * *",
		marketdata.Interval5m:  "*/5 * * * *",
		marketdata.Interval15m: "*/15 * * * *",
		marketdata.Interval1h:  "5 * * * *",
		// Daily rules are checked a few times through the Indian and US
		// sessions, so an operator hears about a trigger the same day rather
		// than the next morning.
		marketdata.Interval1d:  "20 4,7,10,14,18,21 * * *",
		marketdata.Interval1wk: "30 4 * * 1",
	}
}

// Scheduler drives periodic evaluation.
type Scheduler struct {
	engine    *Engine
	cron      *cron.Cron
	log       *slog.Logger
	schedules map[marketdata.Interval]string

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

// WithSchedule overrides the cron expression for one interval.
func WithSchedule(iv marketdata.Interval, spec string) SchedulerOption {
	return func(s *Scheduler) { s.schedules[iv] = spec }
}

// NewScheduler builds a scheduler running in the given location.
func NewScheduler(engine *Engine, loc *time.Location, opts ...SchedulerOption) *Scheduler {
	if loc == nil {
		loc = time.UTC
	}
	s := &Scheduler{
		engine:    engine,
		cron:      cron.New(cron.WithLocation(loc)),
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

	for iv, spec := range s.schedules {
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
func (s *Scheduler) Schedules() map[string]string {
	out := make(map[string]string, len(s.schedules))
	for iv, spec := range s.schedules {
		out[string(iv)] = spec
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

// NextRuns reports when each schedule fires next, for the settings page.
func (s *Scheduler) NextRuns() map[string]time.Time {
	out := map[string]time.Time{}
	entries := s.cron.Entries()
	// cron does not expose the interval a job belongs to, so pair entries with
	// schedules by parsing each spec and matching the computed next time.
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	now := time.Now()
	for iv, spec := range s.schedules {
		sched, err := parser.Parse(spec)
		if err != nil {
			continue
		}
		next := sched.Next(now)
		for _, e := range entries {
			if e.Next.Sub(next).Abs() < time.Minute {
				next = e.Next
				break
			}
		}
		out[string(iv)] = next.UTC()
	}
	return out
}
