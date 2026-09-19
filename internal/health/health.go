// Package health aggregates the liveness of every external dependency into the
// status dots the top bar renders and the degraded banner the UI shows.
//
// The central distinction it draws is between a dependency that is broken and
// one that was never asked to work. An operator who has not supplied an Alpha
// Vantage key should see a neutral "unconfigured" dot forever, not a red one.
package health

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

// ProviderLLM is the dependency name the language model is tracked under.
// Named here rather than spelled as a literal at each end, so the declaration
// in main and the sink below cannot drift apart.
const ProviderLLM = "llm"

// Kind groups dependencies in the UI.
const (
	KindMarketData = "marketdata"
	KindLLM        = "llm"
	KindSearch     = "search"
	KindNotify     = "notify"
	KindNews       = "news"
)

// downAfterConsecutiveFailures is how many faults in a row turn a degraded dot
// red. One transient 500 should not alarm anybody; three in a row should.
const downAfterConsecutiveFailures = 3

// Dep declares a dependency to track.
type Dep struct {
	Provider string
	Kind     string
	// Configured is false when no credentials were supplied. Unconfigured
	// dependencies report HealthUnconfigured and ignore outcomes.
	Configured bool
}

// Tracker holds current health in memory and writes it through to storage so
// the dots survive a restart.
type Tracker struct {
	store storage.HealthStore
	log   *slog.Logger
	now   func() time.Time

	mu       sync.RWMutex
	state    map[string]storage.ProviderHealth
	failures map[string]int
	deps     map[string]Dep
}

// Option configures a Tracker.
type Option func(*Tracker)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(t *Tracker) { t.log = l } }

// WithClock replaces the time source.
func WithClock(now func() time.Time) Option { return func(t *Tracker) { t.now = now } }

// New builds a Tracker seeded with the declared dependencies, so every dot has
// a defined state before the first request rather than popping into existence.
func New(store storage.HealthStore, deps []Dep, opts ...Option) *Tracker {
	t := &Tracker{
		store:    store,
		log:      slog.Default(),
		now:      func() time.Time { return time.Now().UTC() },
		state:    map[string]storage.ProviderHealth{},
		failures: map[string]int{},
		deps:     map[string]Dep{},
	}
	for _, o := range opts {
		o(t)
	}
	for _, d := range deps {
		t.deps[d.Provider] = d
		status := storage.HealthUnconfigured
		msg := "no credentials configured"
		if d.Configured {
			status = storage.HealthOK
			msg = "not yet contacted"
		}
		t.state[d.Provider] = storage.ProviderHealth{
			Provider:  d.Provider,
			Kind:      d.Kind,
			Status:    status,
			Message:   msg,
			UpdatedAt: t.now(),
		}
	}
	return t
}

// Observe records the result of one interaction with a dependency.
//
// skipped means the dependency declined for an expected reason — spent budget,
// unsupported symbol — which is worth surfacing but is not a fault and never
// escalates to red.
func (t *Tracker) Observe(ctx context.Context, provider string, ok, skipped bool, err error) {
	t.mu.Lock()

	dep, known := t.deps[provider]
	if !known {
		// A provider nobody declared: track it anyway rather than dropping
		// the signal, defaulting to the market data group.
		dep = Dep{Provider: provider, Kind: KindMarketData, Configured: true}
		t.deps[provider] = dep
	}

	cur, exists := t.state[provider]
	if !exists {
		cur = storage.ProviderHealth{Provider: provider, Kind: dep.Kind}
	}

	if !dep.Configured {
		// Nothing an unconfigured dependency reports should change its dot.
		t.mu.Unlock()
		return
	}

	now := t.now()
	cur.UpdatedAt = now
	switch {
	case ok:
		t.failures[provider] = 0
		cur.Status = storage.HealthOK
		cur.Message = ""
		cur.LastOKAt = &now
	case skipped:
		// Budget exhaustion is a real limitation the operator must see, but
		// the provider itself is healthy.
		cur.Status = storage.HealthDegraded
		cur.Message = errText(err)
	default:
		t.failures[provider]++
		if t.failures[provider] >= downAfterConsecutiveFailures {
			cur.Status = storage.HealthDown
		} else {
			cur.Status = storage.HealthDegraded
		}
		cur.Message = errText(err)
		cur.LastErrorAt = &now
	}
	t.state[provider] = cur
	snapshot := cur
	t.mu.Unlock()

	// Persist outside the lock; a slow disk must not stall the request path.
	if t.store != nil {
		if err := t.store.RecordHealth(ctx, snapshot); err != nil {
			t.log.Warn("could not persist health", "provider", provider, "err", err)
		}
	}
}

// MarketDataSink adapts the Tracker to the router's outcome port.
func (t *Tracker) MarketDataSink() marketdata.OutcomeSink {
	return func(ctx context.Context, o marketdata.Outcome) {
		t.Observe(ctx, o.Provider, o.OK, o.Skipped, o.Err)
	}
}

// LLMSink adapts the Tracker to the AI client's outcome port.
//
// The LLM was the one declared dependency nothing ever reported on. Its
// Status() answers from configuration and budget without making a call, so a
// configured-but-rejected key read as healthy: /api/health showed a green dot
// and "not yet contacted" while every AI feature in the app was failing 401.
// A monitor watching that endpoint would never have found out.
func (t *Tracker) LLMSink() ai.OutcomeSink {
	return func(ctx context.Context, o ai.Outcome) {
		t.Observe(ctx, ProviderLLM, o.OK, o.Skipped, o.Err)
	}
}

// SetConfigured updates whether a dependency has credentials, which happens
// when the operator saves settings without restarting.
func (t *Tracker) SetConfigured(provider string, configured bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.deps[provider]
	if !ok {
		return
	}
	if d.Configured == configured {
		return
	}
	d.Configured = configured
	t.deps[provider] = d

	cur := t.state[provider]
	if configured {
		cur.Status = storage.HealthOK
		cur.Message = "not yet contacted"
	} else {
		cur.Status = storage.HealthUnconfigured
		cur.Message = "no credentials configured"
	}
	cur.UpdatedAt = t.now()
	t.state[provider] = cur
}

// Snapshot returns current health for every dependency, ordered by kind then
// provider so the UI's dot order is stable across polls.
func (t *Tracker) Snapshot() []storage.ProviderHealth {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]storage.ProviderHealth, 0, len(t.state))
	for _, h := range t.state {
		out = append(out, h)
	}
	// Insertion sort: this list is a handful of entries.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func less(a, b storage.ProviderHealth) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Provider < b.Provider
}

// DegradedProviders lists configured market data providers that are not
// currently healthy, whether or not another provider is covering for them.
//
// This is the difference between "your prices are wrong" and "one source is
// down and the fallback is holding". Both are worth telling the operator; they
// are not worth telling them the same way.
func (t *Tracker) DegradedProviders() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var out []string
	for name, h := range t.state {
		if h.Kind != KindMarketData {
			continue
		}
		if d, ok := t.deps[name]; !ok || !d.Configured {
			continue
		}
		if h.Status != storage.HealthOK {
			out = append(out, name)
		}
	}
	// Stable order so the banner text does not shuffle between polls.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// MarketDataDegraded reports whether price data is currently compromised, which
// is true when every configured market data provider is failing. One healthy
// provider is enough to keep the loud banner down, because that is the point of
// having a fallback.
func (t *Tracker) MarketDataDegraded() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var configured, healthy int
	for name, h := range t.state {
		if h.Kind != KindMarketData {
			continue
		}
		if d, ok := t.deps[name]; !ok || !d.Configured {
			continue
		}
		configured++
		if h.Status == storage.HealthOK {
			healthy++
		}
	}
	return configured > 0 && healthy == 0
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	const max = 200
	s := err.Error()
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
