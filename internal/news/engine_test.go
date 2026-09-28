package news

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRawStore records what the engine saves.
type fakeRawStore struct {
	mu      sync.Mutex
	items   []RawItem
	health  map[string]SourceHealth
	seen    map[string]bool // source_id|content_hash
	saveErr error
}

func newFakeStore() *fakeRawStore {
	return &fakeRawStore{health: map[string]SourceHealth{}, seen: map[string]bool{}}
}

func (f *fakeRawStore) SaveRawItems(_ context.Context, items []RawItem) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return 0, f.saveErr
	}
	added := 0
	for _, it := range items {
		key := it.SourceID + "|" + it.ContentHash
		if f.seen[key] {
			continue
		}
		f.seen[key] = true
		f.items = append(f.items, it)
		added++
	}
	return added, nil
}

func (f *fakeRawStore) LoadSourceHealth(context.Context) ([]SourceHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SourceHealth, 0, len(f.health))
	for _, h := range f.health {
		out = append(out, h)
	}
	return out, nil
}

func (f *fakeRawStore) SaveSourceHealth(_ context.Context, h SourceHealth) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health[h.SourceID] = h
	return nil
}

func (f *fakeRawStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

const rssFixture = `<?xml version="1.0"?><rss version="2.0"><channel>
<title>Test Feed</title>
<item><title>Reliance Industries wins contract</title><link>https://example.com/a?utm_source=rss&amp;id=1</link><pubDate>Mon, 25 Aug 2026 10:00:00 GMT</pubDate><description>Details here</description></item>
<item><title>Infosys raises guidance</title><link>https://example.com/b</link><pubDate>Mon, 25 Aug 2026 11:00:00 GMT</pubDate></item>
</channel></rss>`

func testSource(url string, opts ...func(*Source)) Source {
	s := Source{
		ID: "test-src", Name: "Test", URL: url, Method: MethodRSS,
		Category: "markets", Trust: TrustMajorFin, Refresh: time.Minute,
		Timeout: 5 * time.Second, Usage: UsagePublicReviewed,
		Display: DisplayFull, Enabled: true,
	}
	for _, o := range opts {
		o(&s)
	}
	return s
}

func newTestEngine(t *testing.T, store RawStore, now func() time.Time, sources ...Source) *Engine {
	t.Helper()
	reg, err := NewRegistry(sources...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return NewEngine(reg, store, WithEngineLogger(quietLogger()), WithEngineClock(now))
}

func TestEngineFetchesAndStores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, rssFixture)
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock }, testSource(srv.URL))

	res := e.RunOnce(context.Background())
	if res.Attempted != 1 || res.Succeeded != 1 {
		t.Fatalf("run = %+v, want one attempt that succeeded", res)
	}
	if res.NewItems != 2 {
		t.Errorf("NewItems = %d, want 2", res.NewItems)
	}
	if store.count() != 2 {
		t.Fatalf("stored %d items, want 2", store.count())
	}

	got := store.items[0]
	if got.DiscoveredAt != clock {
		t.Errorf("DiscoveredAt = %v, want the fetch time %v", got.DiscoveredAt, clock)
	}
	if got.PublishedAt.IsZero() {
		t.Error("PublishedAt was not parsed from the feed")
	}
	// The tracking parameter must be gone but the real one kept.
	if want := "https://example.com/a?id=1"; got.CanonicalURL != want {
		t.Errorf("CanonicalURL = %q, want %q", got.CanonicalURL, want)
	}
}

// TestEngineDedupesAcrossPolls is the property that makes a one-minute cadence
// affordable: re-fetching an unchanged feed must add nothing.
func TestEngineDedupesAcrossPolls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, rssFixture)
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock }, testSource(srv.URL))

	first := e.RunOnce(context.Background())
	clock = clock.Add(30 * time.Minute) // past the lane floor and any jitter
	second := e.RunOnce(context.Background())

	if first.NewItems != 2 {
		t.Fatalf("first pass new items = %d, want 2", first.NewItems)
	}
	if second.Attempted != 1 {
		t.Fatalf("second pass attempted %d, want 1", second.Attempted)
	}
	if second.NewItems != 0 {
		t.Errorf("second pass added %d items, want 0 — the feed was unchanged", second.NewItems)
	}
}

func TestEngineRespectsSchedule(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, rssFixture)
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock },
		testSource(srv.URL, func(s *Source) { s.Refresh = time.Hour }))

	e.RunOnce(context.Background())
	clock = clock.Add(time.Minute) // well inside the hour
	res := e.RunOnce(context.Background())

	if res.Attempted != 0 || res.Skipped != 1 {
		t.Errorf("run = %+v, want the source skipped as not yet due", res)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream hit %d times, want 1", got)
	}
}

// TestEngineConditionalRequest checks that we send back what the publisher
// gave us, and treat 304 as a cheap success rather than as an error.
func TestEngineConditionalRequest(t *testing.T) {
	var sawIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			sawIfNoneMatch = inm
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc123"`)
		fmt.Fprint(w, rssFixture)
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock }, testSource(srv.URL))

	e.RunOnce(context.Background())
	clock = clock.Add(30 * time.Minute)
	res := e.RunOnce(context.Background())

	if sawIfNoneMatch != `"abc123"` {
		t.Errorf("If-None-Match = %q, want the stored ETag", sawIfNoneMatch)
	}
	if res.Failed != 0 || res.Succeeded != 1 {
		t.Errorf("run = %+v, want 304 counted as a success", res)
	}
	if store.count() != 2 {
		t.Errorf("stored %d items, want the original 2 unchanged", store.count())
	}
}

// TestEngineCircuitBreaker checks that a failing source is backed off rather
// than retried on every tick.
func TestEngineCircuitBreaker(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock },
		testSource(srv.URL, func(s *Source) { s.Refresh = time.Minute }))

	if res := e.RunOnce(context.Background()); res.Failed != 1 {
		t.Fatalf("first run = %+v, want one failure", res)
	}
	// One failure earns roughly 2x the base interval, so a minute later the
	// source must still be held open.
	clock = clock.Add(90 * time.Second)
	if res := e.RunOnce(context.Background()); res.Attempted != 0 {
		t.Errorf("run = %+v, want the breaker to hold the source open", res)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream hit %d times, want 1 — a failing source must not be hammered", got)
	}

	h := store.health["test-src"]
	if h.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", h.ConsecutiveFailures)
	}
	if h.LastError == "" {
		t.Error("expected the failure reason to be recorded")
	}
}

func TestEngineBackoffGrowsAndIsCapped(t *testing.T) {
	e := NewEngine(&Registry{}, newFakeStore(), WithEngineLogger(quietLogger()))
	base := time.Minute
	const cap = 30 * time.Minute
	// Jitter is applied after the cap, so growth is only monotonic while the
	// raw value is still below it. Above the cap the delay is deliberately
	// flat, and asserting continued growth there would be asserting a bug.
	var prev time.Duration
	for failures := 1; failures <= 4; failures++ {
		got := e.backoff(failures, base)
		if failures > 1 && got <= prev {
			t.Errorf("backoff(%d) = %v, not longer than backoff(%d) = %v", failures, got, failures-1, prev)
		}
		prev = got
	}
	for _, failures := range []int{5, 6, 20, 500} {
		if got := e.backoff(failures, base); got > cap+cap/5 {
			t.Errorf("backoff(%d) = %v, exceeds the cap plus jitter", failures, got)
		}
	}
	// A source that has failed all night must still be retried, not abandoned.
	if got := e.backoff(500, base); got > 35*time.Minute {
		t.Errorf("backoff(500) = %v, must remain capped", got)
	}
}

// TestEnginePrimeResumesInsideBackoff checks that restarting the process does
// not reset a failing source's penalty, which would turn every restart into a
// fresh burst of traffic at a publisher that is already struggling.
func TestEnginePrimeResumesInsideBackoff(t *testing.T) {
	store := newFakeStore()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	store.health["test-src"] = SourceHealth{
		SourceID:            "test-src",
		ConsecutiveFailures: 3,
		LastFailureAt:       now.Add(-10 * time.Second),
	}
	e := newTestEngine(t, store, func() time.Time { return now },
		testSource("https://example.invalid/feed", func(s *Source) { s.Refresh = time.Minute }))

	if err := e.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if res := e.RunOnce(context.Background()); res.Attempted != 0 {
		t.Errorf("run = %+v, want the restored backoff to hold", res)
	}
}

// TestEnginePerHostLimit checks that many sources on one host do not become a
// burst of simultaneous requests at that host.
func TestEnginePerHostLimit(t *testing.T) {
	var (
		mu      sync.Mutex
		current int
		peak    int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		current++
		if current > peak {
			peak = current
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		current--
		mu.Unlock()
		fmt.Fprint(w, rssFixture)
	}))
	defer srv.Close()

	var sources []Source
	for i := 0; i < 8; i++ {
		s := testSource(fmt.Sprintf("%s/feed%d", srv.URL, i))
		s.ID = fmt.Sprintf("src-%d", i)
		sources = append(sources, s)
	}
	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	reg, err := NewRegistry(sources...)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(reg, store, WithEngineLogger(quietLogger()),
		WithEngineClock(func() time.Time { return clock }),
		WithWorkers(8), WithPerHostLimit(2))

	e.RunOnce(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if peak > 2 {
		t.Errorf("peak concurrent requests to one host = %d, want at most 2", peak)
	}
}

func TestEngineHandlesEmptyAndBadBodies(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
	}{
		{"empty body", "", http.StatusOK},
		{"not xml", "this is not a feed at all", http.StatusOK},
		{"server error", "boom", http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			store := newFakeStore()
			clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
			e := newTestEngine(t, store, func() time.Time { return clock }, testSource(srv.URL))
			// The contract is that a bad response is recorded and survived,
			// never that it panics or blocks the rest of the pass.
			res := e.RunOnce(context.Background())
			if res.Attempted != 1 {
				t.Fatalf("run = %+v", res)
			}
			if store.count() != 0 {
				t.Errorf("stored %d items from a bad response, want 0", store.count())
			}
		})
	}
}

func TestEngineParseGDELT(t *testing.T) {
	body := `{"articles":[
	 {"url":"https://example.com/x","title":"Reliance in talks","seendate":"20260825T103000Z","domain":"example.com","language":"English","sourcecountry":"India"},
	 {"url":"","title":"no url","seendate":"20260825T103000Z"},
	 {"url":"https://example.com/y","title":"","seendate":"20260825T103000Z"}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock },
		testSource(srv.URL, func(s *Source) { s.Method = MethodGDELT }))

	res := e.RunOnce(context.Background())
	if res.Failed != 0 {
		t.Fatalf("run = %+v, want success", res)
	}
	if store.count() != 1 {
		t.Fatalf("stored %d items, want 1 — entries lacking a URL or title are unusable", store.count())
	}
	got := store.items[0]
	want := time.Date(2026, 8, 25, 10, 30, 0, 0, time.UTC)
	if !got.PublishedAt.Equal(want) {
		t.Errorf("PublishedAt = %v, want %v", got.PublishedAt, want)
	}
}

// TestGDELTRateLimitProseIsAnError guards a real failure mode: GDELT answers
// an over-eager client with a sentence, not JSON, and that must surface as a
// failure rather than as an empty but successful fetch.
func TestGDELTRateLimitProseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "Please limit requests to one every 5 seconds")
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock },
		testSource(srv.URL, func(s *Source) { s.Method = MethodGDELT }))

	if res := e.RunOnce(context.Background()); res.Failed != 1 {
		t.Errorf("run = %+v, want the rate-limit reply treated as a failure", res)
	}
}

// Go omits the User-Agent header entirely when it is set to the empty string.
// This pins that behaviour, since the whole fetch strategy depends on it.
func TestNoUserAgentIsSent(t *testing.T) {
	var got string
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		_, present = r.Header["User-Agent"]
		fmt.Fprint(w, rssFixture)
	}))
	defer srv.Close()

	store := newFakeStore()
	e := newTestEngine(t, store, func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) }, testSource(srv.URL))
	e.RunOnce(context.Background())

	if present || got != "" {
		t.Errorf("User-Agent sent as %q (present=%v), want no header at all", got, present)
	}
}

// TestEngineHonoursRateLimit checks that a 429 is treated as the source
// telling us to slow down, not as the source being broken.
func TestEngineHonoursRateLimit(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock },
		testSource(srv.URL, func(s *Source) { s.Refresh = 10 * time.Second }))

	if res := e.RunOnce(context.Background()); res.Failed != 1 {
		t.Fatalf("run = %+v, want one failure", res)
	}
	// The source asked for two minutes. A refresh interval of ten seconds
	// must not override that.
	clock = clock.Add(60 * time.Second)
	if res := e.RunOnce(context.Background()); res.Attempted != 0 {
		t.Errorf("retried after 60s despite Retry-After: 120 — run = %+v", res)
	}
	clock = clock.Add(70 * time.Second) // now past the stated window
	if res := e.RunOnce(context.Background()); res.Attempted != 1 {
		t.Errorf("did not retry after the stated window elapsed — run = %+v", res)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("upstream hit %d times, want 2", got)
	}
}

// TestRateLimitWithoutRetryAfter covers the common case where a source
// refuses us but says nothing about when to come back. GDELT does exactly
// this, answering with prose and no header.
func TestRateLimitWithoutRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "Please limit requests to one every 5 seconds")
	}))
	defer srv.Close()

	store := newFakeStore()
	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	e := newTestEngine(t, store, func() time.Time { return clock },
		testSource(srv.URL, func(s *Source) { s.Refresh = time.Second }))

	e.RunOnce(context.Background())
	clock = clock.Add(20 * time.Minute)
	if res := e.RunOnce(context.Background()); res.Attempted != 0 {
		t.Errorf("run = %+v, want a long wait when no Retry-After is given", res)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"30", 30 * time.Second},
		{"0", 0},
		{"-5", 0},
		{"Tue, 25 Aug 2026 12:05:00 GMT", 5 * time.Minute},
		{"Tue, 25 Aug 2026 11:00:00 GMT", 0}, // already past
		{"not a header", 0},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.in, now); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
