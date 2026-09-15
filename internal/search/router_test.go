package search

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func quiet() RouterOption { return WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) }

type stubProvider struct {
	name       string
	configured bool
	results    []Result
	err        error
	mu         sync.Mutex
	calls      int
}

func (s *stubProvider) Name() string     { return s.name }
func (s *stubProvider) Configured() bool { return s.configured }
func (s *stubProvider) Search(context.Context, Query) ([]Result, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.results, nil
}
func (s *stubProvider) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type memCache struct {
	mu      sync.Mutex
	entries map[string]Results
	loadErr error
	saveErr error
}

func newMemCache() *memCache { return &memCache{entries: map[string]Results{}} }

func (m *memCache) LoadSearch(_ context.Context, key string) (Results, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return Results{}, false, m.loadErr
	}
	r, ok := m.entries[key]
	return r, ok, nil
}

func (m *memCache) SaveSearch(_ context.Context, key string, r Results) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.entries[key] = r
	return nil
}

func someResults(n int) []Result {
	out := make([]Result, n)
	for i := range out {
		out[i] = Result{Title: "Story", URL: "https://example.com/a", Snippet: "text", Source: "example.com"}
	}
	return out
}

var frozen = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

func newRouter(cache Cache, providers ...Provider) *Router {
	return NewRouter(cache, providers, quiet(), WithClock(func() time.Time { return frozen }))
}

func TestPreferredProviderWins(t *testing.T) {
	tavily := &stubProvider{name: "tavily", configured: true, results: someResults(3)}
	brave := &stubProvider{name: "brave", configured: true, results: someResults(5)}
	r := newRouter(newMemCache(), tavily, brave)

	got, err := r.Search(context.Background(), Query{Text: "reliance news"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "tavily" || len(got.Results) != 3 {
		t.Errorf("results came from %q with %d hits, want tavily with 3", got.Provider, len(got.Results))
	}
	if brave.count() != 0 {
		t.Error("the fallback provider was called despite the preferred one succeeding")
	}
}

func TestFallsBackToTheOtherProvider(t *testing.T) {
	tests := []struct {
		name       string
		primaryErr error
		primaryRes []Result
	}{
		{name: "primary errors", primaryErr: errors.New("503 service unavailable")},
		{name: "primary returns nothing", primaryRes: nil},
		{name: "primary rate limited", primaryErr: errors.New("429 too many requests")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tavily := &stubProvider{name: "tavily", configured: true, err: tc.primaryErr, results: tc.primaryRes}
			brave := &stubProvider{name: "brave", configured: true, results: someResults(2)}
			r := newRouter(newMemCache(), tavily, brave)

			got, err := r.Search(context.Background(), Query{Text: "q"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Provider != "brave" {
				t.Errorf("provider = %q, want brave", got.Provider)
			}
			if brave.count() != 1 {
				t.Errorf("fallback calls = %d, want 1", brave.count())
			}
		})
	}
}

func TestUnconfiguredProvidersAreSkipped(t *testing.T) {
	tavily := &stubProvider{name: "tavily", configured: false, results: someResults(9)}
	brave := &stubProvider{name: "brave", configured: true, results: someResults(2)}
	r := newRouter(newMemCache(), tavily, brave)

	got, err := r.Search(context.Background(), Query{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if tavily.count() != 0 {
		t.Error("an unconfigured provider was called")
	}
	if got.Provider != "brave" {
		t.Errorf("provider = %q, want brave", got.Provider)
	}
}

func TestNoProvidersConfigured(t *testing.T) {
	r := newRouter(newMemCache(),
		&stubProvider{name: "tavily", configured: false},
		&stubProvider{name: "brave", configured: false})

	if r.Configured() {
		t.Error("Configured = true with no keys")
	}
	_, err := r.Search(context.Background(), Query{Text: "q"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("error = %v, want ErrNotConfigured", err)
	}
}

func TestCaching(t *testing.T) {
	provider := &stubProvider{name: "tavily", configured: true, results: someResults(3)}
	cache := newMemCache()
	r := newRouter(cache, provider)
	ctx := context.Background()

	first, err := r.Search(ctx, Query{Text: "reliance news"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached {
		t.Error("the first result claimed to be cached")
	}

	second, err := r.Search(ctx, Query{Text: "reliance news"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached {
		t.Error("the second identical query was not served from cache")
	}
	if provider.count() != 1 {
		t.Errorf("provider calls = %d, want 1", provider.count())
	}
}

func TestCacheKeyNormalisation(t *testing.T) {
	// Casing and whitespace differences must not fragment the cache.
	provider := &stubProvider{name: "tavily", configured: true, results: someResults(1)}
	r := newRouter(newMemCache(), provider)
	ctx := context.Background()

	for _, q := range []string{"Reliance News", "reliance news", "  RELIANCE   news  "} {
		if _, err := r.Search(ctx, Query{Text: q}); err != nil {
			t.Fatal(err)
		}
	}
	if provider.count() != 1 {
		t.Errorf("provider calls = %d, want 1 — these are the same query", provider.count())
	}
}

func TestCacheExpiry(t *testing.T) {
	provider := &stubProvider{name: "tavily", configured: true, results: someResults(1)}
	cache := newMemCache()
	now := frozen
	r := NewRouter(cache, []Provider{provider}, quiet(), WithClock(func() time.Time { return now }))
	ctx := context.Background()

	if _, err := r.Search(ctx, Query{Text: "q"}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		advance   time.Duration
		wantCalls int
	}{
		{name: "well inside the hour", advance: 30 * time.Minute, wantCalls: 1},
		{name: "just inside the hour", advance: 59 * time.Minute, wantCalls: 1},
		{name: "past the hour", advance: 61 * time.Minute, wantCalls: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now = frozen.Add(tc.advance)
			if _, err := r.Search(ctx, Query{Text: "q"}); err != nil {
				t.Fatal(err)
			}
			if provider.count() != tc.wantCalls {
				t.Errorf("provider calls = %d, want %d", provider.count(), tc.wantCalls)
			}
		})
	}
}

func TestStaleCacheServedWhenEverythingFails(t *testing.T) {
	// Search only decorates an answer, so stale sources beat no sources.
	provider := &stubProvider{name: "tavily", configured: true, results: someResults(2)}
	cache := newMemCache()
	now := frozen
	r := NewRouter(cache, []Provider{provider}, quiet(), WithClock(func() time.Time { return now }))
	ctx := context.Background()

	if _, err := r.Search(ctx, Query{Text: "q"}); err != nil {
		t.Fatal(err)
	}

	now = frozen.Add(24 * time.Hour)
	provider.err = errors.New("provider down")

	got, err := r.Search(ctx, Query{Text: "q"})
	if err != nil {
		t.Fatalf("stale cache should have covered this: %v", err)
	}
	if !got.Cached || len(got.Results) != 2 {
		t.Errorf("got %+v, want the stale cached results flagged as cached", got)
	}
}

func TestBrokenCacheDoesNotBlockSearch(t *testing.T) {
	provider := &stubProvider{name: "tavily", configured: true, results: someResults(2)}
	cache := newMemCache()
	cache.loadErr = errors.New("disk on fire")
	r := newRouter(cache, provider)

	got, err := r.Search(context.Background(), Query{Text: "q"})
	if err != nil {
		t.Fatalf("a cache failure must not fail the search: %v", err)
	}
	if len(got.Results) != 2 {
		t.Errorf("got %d results, want 2", len(got.Results))
	}
}

func TestEmptyQueryRejected(t *testing.T) {
	r := newRouter(newMemCache(), &stubProvider{name: "tavily", configured: true, results: someResults(1)})
	for _, q := range []string{"", "   ", "\n\t"} {
		if _, err := r.Search(context.Background(), Query{Text: q}); err == nil {
			t.Errorf("query %q should be rejected", q)
		}
	}
}

func TestObserverReceivesOutcomes(t *testing.T) {
	type outcome struct {
		provider string
		ok       bool
		skipped  bool
	}
	var got []outcome
	var mu sync.Mutex

	tavily := &stubProvider{name: "tavily", configured: true, err: errors.New("boom")}
	brave := &stubProvider{name: "brave", configured: true, results: someResults(1)}
	r := NewRouter(newMemCache(), []Provider{tavily, brave}, quiet(),
		WithClock(func() time.Time { return frozen }),
		WithObserver(func(_ context.Context, p string, ok, skipped bool, _ error) {
			mu.Lock()
			got = append(got, outcome{p, ok, skipped})
			mu.Unlock()
		}))

	if _, err := r.Search(context.Background(), Query{Text: "q"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("observed %d outcomes, want 2", len(got))
	}
	if got[0].provider != "tavily" || got[0].ok {
		t.Errorf("first outcome = %+v, want a tavily failure", got[0])
	}
	if got[1].provider != "brave" || !got[1].ok {
		t.Errorf("second outcome = %+v, want a brave success", got[1])
	}
}

func TestProviderNamesListsOnlyConfigured(t *testing.T) {
	r := newRouter(newMemCache(),
		&stubProvider{name: "tavily", configured: false},
		&stubProvider{name: "brave", configured: true})

	names := r.ProviderNames()
	if len(names) != 1 || names[0] != "brave" {
		t.Errorf("ProviderNames = %v, want [brave]", names)
	}
}
