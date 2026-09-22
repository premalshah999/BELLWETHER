package marketdata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// memCache is an in-memory Cache with scriptable failures.
type memCache struct {
	mu      sync.Mutex
	series  map[string]CachedSeries
	quotes  map[string]CachedQuote
	loadErr error
	saveErr error
	saves   int
}

func newMemCache() *memCache {
	return &memCache{series: map[string]CachedSeries{}, quotes: map[string]CachedQuote{}}
}

func (m *memCache) key(sym Symbol, iv Interval) string { return sym.String() + "|" + string(iv) }

func (m *memCache) LoadCandles(_ context.Context, sym Symbol, iv Interval, limit int) (CachedSeries, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return CachedSeries{}, m.loadErr
	}
	return m.series[m.key(sym, iv)], nil
}

func (m *memCache) SaveCandles(_ context.Context, sym Symbol, iv Interval, source string, bars Bars, requestedLimit int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	if m.saveErr != nil {
		return m.saveErr
	}
	k := m.key(sym, iv)
	high := requestedLimit
	if prev, ok := m.series[k]; ok && prev.RequestedLimit > high {
		high = prev.RequestedLimit
	}
	m.series[k] = CachedSeries{
		Candles: bars.Candles, Source: source,
		ResolvedSymbol: bars.ResolvedSymbol, FetchedAt: time.Now().UTC(),
		RequestedLimit: high,
	}
	return nil
}

func (m *memCache) LoadQuote(_ context.Context, sym Symbol) (CachedQuote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return CachedQuote{}, m.loadErr
	}
	return m.quotes[sym.String()], nil
}

func (m *memCache) SaveQuote(_ context.Context, source string, q Quote) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.quotes[q.Symbol.String()] = CachedQuote{Quote: q, Source: source, FetchedAt: time.Now().UTC()}
	return nil
}

// seed writes candles into the cache with an explicit fetch time so tests can
// control staleness precisely.
func (m *memCache) seed(sym Symbol, iv Interval, source string, fetchedAt time.Time, closes ...float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var cs []Candle
	for i, c := range closes {
		cs = append(cs, Candle{
			Time: base.Add(time.Duration(i) * 24 * time.Hour),
			Open: c, High: c, Low: c, Close: c, Volume: 1,
		})
	}
	// Model a cache that has already been filled as deeply as anyone asks:
	// these tests are about freshness and fallback, not coverage, which
	// TestRouterCacheCoverage exercises directly.
	m.series[m.key(sym, iv)] = CachedSeries{
		Candles: cs, Source: source, FetchedAt: fetchedAt, RequestedLimit: 10_000,
	}
}

// stubProvider is a Provider whose every response the test dictates.
type stubProvider struct {
	name     string
	candles  []Candle
	quote    Quote
	resolved string
	err      error
	mu       sync.Mutex
	calls    int
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Candles(context.Context, Symbol, Interval, int) (Bars, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.err != nil {
		return Bars{}, s.err
	}
	return Bars{Candles: s.candles, ResolvedSymbol: s.resolved}, nil
}

func (s *stubProvider) Quote(context.Context, Symbol) (Quote, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.err != nil {
		return Quote{}, s.err
	}
	return s.quote, nil
}

func (s *stubProvider) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func makeCandles(closes ...float64) []Candle {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var out []Candle
	for i, c := range closes {
		out = append(out, Candle{
			Time: base.Add(time.Duration(i) * 24 * time.Hour),
			Open: c, High: c + 1, Low: c - 1, Close: c, Volume: 100,
		})
	}
	return out
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var testSym = MustParseSymbol("RELIANCE.BSE")

func TestRouterCandles(t *testing.T) {
	frozen := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return frozen }

	tests := []struct {
		name        string
		setupCache  func(*memCache)
		providers   []*stubProvider
		wantSource  string
		wantStale   bool
		wantCloses  []float64
		wantErr     bool
		wantCalls   map[string]int
		description string
	}{
		{
			name: "fresh cache serves without touching any provider",
			setupCache: func(m *memCache) {
				m.seed(testSym, Interval1d, "yahoo", frozen.Add(-time.Minute), 10, 11, 12)
			},
			providers:  []*stubProvider{{name: "yahoo", candles: makeCandles(99)}},
			wantSource: "yahoo",
			wantCloses: []float64{10, 11, 12},
			wantCalls:  map[string]int{"yahoo": 0},
		},
		{
			name: "expired cache refetches",
			setupCache: func(m *memCache) {
				m.seed(testSym, Interval1d, "yahoo", frozen.Add(-24*time.Hour), 10, 11, 12)
			},
			providers:  []*stubProvider{{name: "yahoo", candles: makeCandles(20, 21)}},
			wantSource: "yahoo",
			wantCloses: []float64{20, 21},
			wantCalls:  map[string]int{"yahoo": 1},
		},
		{
			name:       "first provider wins",
			providers:  []*stubProvider{{name: "yahoo", candles: makeCandles(1, 2)}, {name: "av", candles: makeCandles(9, 9)}},
			wantSource: "yahoo",
			wantCloses: []float64{1, 2},
			wantCalls:  map[string]int{"yahoo": 1, "av": 0},
		},
		{
			name:       "falls through to the next provider on failure",
			providers:  []*stubProvider{{name: "yahoo", err: errors.New("429 rate limited")}, {name: "av", candles: makeCandles(5, 6)}},
			wantSource: "av",
			wantCloses: []float64{5, 6},
			wantCalls:  map[string]int{"yahoo": 1, "av": 1},
		},
		{
			name:       "budget exhaustion falls through without being a fault",
			providers:  []*stubProvider{{name: "av", err: ErrBudgetExhausted}, {name: "yahoo", candles: makeCandles(7)}},
			wantSource: "yahoo",
			wantCloses: []float64{7},
			wantCalls:  map[string]int{"av": 1, "yahoo": 1},
		},
		{
			name:       "an empty provider answer is not accepted as data",
			providers:  []*stubProvider{{name: "yahoo", candles: nil}, {name: "av", candles: makeCandles(3)}},
			wantSource: "av",
			wantCloses: []float64{3},
		},
		{
			name: "every provider down serves stale cache, flagged",
			setupCache: func(m *memCache) {
				m.seed(testSym, Interval1d, "yahoo", frozen.Add(-30*24*time.Hour), 10, 11)
			},
			providers: []*stubProvider{
				{name: "yahoo", err: errors.New("connection refused")},
				{name: "av", err: errors.New("500")},
			},
			wantSource: "yahoo",
			wantStale:  true,
			wantCloses: []float64{10, 11},
		},
		{
			name:      "every provider down with no cache is an error",
			providers: []*stubProvider{{name: "yahoo", err: errors.New("connection refused")}},
			wantErr:   true,
		},
		{
			name:      "no providers and no cache is an error",
			providers: nil,
			wantErr:   true,
		},
		{
			name: "no providers but warm cache still serves",
			setupCache: func(m *memCache) {
				m.seed(testSym, Interval1d, "yahoo", frozen.Add(-time.Minute), 42)
			},
			providers:  nil,
			wantSource: "yahoo",
			wantCloses: []float64{42},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := newMemCache()
			if tc.setupCache != nil {
				tc.setupCache(cache)
			}
			provs := make([]Provider, 0, len(tc.providers))
			for _, p := range tc.providers {
				provs = append(provs, p)
			}
			r := NewRouter(cache, provs, WithClock(clock), WithLogger(quietLogger()))

			got, err := r.Candles(context.Background(), testSym, Interval1d, 100)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got series from %q", got.Source)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Source != tc.wantSource {
				t.Errorf("source = %q, want %q", got.Source, tc.wantSource)
			}
			if got.Stale != tc.wantStale {
				t.Errorf("stale = %v, want %v", got.Stale, tc.wantStale)
			}
			if len(got.Candles) != len(tc.wantCloses) {
				t.Fatalf("got %d candles, want %d", len(got.Candles), len(tc.wantCloses))
			}
			for i, w := range tc.wantCloses {
				if got.Candles[i].Close != w {
					t.Errorf("candle %d close = %v, want %v", i, got.Candles[i].Close, w)
				}
			}
			for _, p := range tc.providers {
				if want, ok := tc.wantCalls[p.name]; ok && p.Calls() != want {
					t.Errorf("provider %q calls = %d, want %d", p.name, p.Calls(), want)
				}
			}
		})
	}
}

func TestRouterReportsOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		providerErr error
		wantOK      bool
		wantSkipped bool
	}{
		{name: "success", providerErr: nil, wantOK: true},
		{name: "hard failure is a fault", providerErr: errors.New("connection refused")},
		{name: "unsupported is a skip", providerErr: ErrNotSupported, wantSkipped: true},
		{name: "budget exhausted is a skip", providerErr: ErrBudgetExhausted, wantSkipped: true},
		{name: "a wrapped budget error is still a skip", providerErr: fmt.Errorf("alphavantage: %w", ErrBudgetExhausted), wantSkipped: true},
		{name: "an error that merely mentions the text is a fault", providerErr: errors.New("budget exhausted, allegedly"), wantSkipped: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []Outcome
			var mu sync.Mutex
			sink := func(_ context.Context, o Outcome) {
				mu.Lock()
				got = append(got, o)
				mu.Unlock()
			}
			p := &stubProvider{name: "yahoo", candles: makeCandles(1), err: tc.providerErr}
			r := NewRouter(newMemCache(), []Provider{p},
				WithOutcomeSink(sink), WithLogger(quietLogger()))

			r.Candles(context.Background(), testSym, Interval1d, 10)

			if len(got) != 1 {
				t.Fatalf("got %d outcomes, want 1", len(got))
			}
			if got[0].Provider != "yahoo" {
				t.Errorf("provider = %q, want yahoo", got[0].Provider)
			}
			if got[0].OK != tc.wantOK {
				t.Errorf("OK = %v, want %v", got[0].OK, tc.wantOK)
			}
			if got[0].Skipped != tc.wantSkipped {
				t.Errorf("Skipped = %v, want %v", got[0].Skipped, tc.wantSkipped)
			}
		})
	}
}

func TestRouterWriteThrough(t *testing.T) {
	cache := newMemCache()
	p := &stubProvider{name: "yahoo", candles: makeCandles(1, 2, 3)}
	r := NewRouter(cache, []Provider{p}, WithLogger(quietLogger()))

	if _, err := r.Candles(context.Background(), testSym, Interval1d, 10); err != nil {
		t.Fatal(err)
	}
	stored, _ := cache.LoadCandles(context.Background(), testSym, Interval1d, 10)
	if len(stored.Candles) != 3 {
		t.Errorf("cache holds %d candles, want 3 — every fetched datum must be cached", len(stored.Candles))
	}
	if stored.Source != "yahoo" {
		t.Errorf("cached source = %q, want yahoo", stored.Source)
	}
}

func TestRouterDropsMalformedCandlesBeforeCaching(t *testing.T) {
	cache := newMemCache()
	rows := makeCandles(100, 101, 102)
	// Reproduces a real live vendor row: the opening trade was reported above
	// the forming bar's high, which violates the database and chart invariant.
	rows[1].Open = rows[1].High + 1
	p := &stubProvider{name: "yahoo", candles: rows}
	r := NewRouter(cache, []Provider{p}, WithLogger(quietLogger()))

	got, err := r.Candles(context.Background(), testSym, Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candles) != 2 {
		t.Fatalf("returned %d candles, want the two valid rows", len(got.Candles))
	}
	stored, _ := cache.LoadCandles(context.Background(), testSym, Interval1d, 10)
	if len(stored.Candles) != 2 {
		t.Fatalf("cached %d candles, want the two valid rows", len(stored.Candles))
	}
	for _, candle := range stored.Candles {
		if candle.High < candle.Open || candle.High < candle.Close ||
			candle.Low > candle.Open || candle.Low > candle.Close {
			t.Fatalf("malformed candle reached the cache: %+v", candle)
		}
	}
}

func TestRouterSurvivesBrokenCache(t *testing.T) {
	t.Run("read failure still fetches", func(t *testing.T) {
		cache := newMemCache()
		cache.loadErr = errors.New("disk on fire")
		p := &stubProvider{name: "yahoo", candles: makeCandles(1, 2)}
		r := NewRouter(cache, []Provider{p}, WithLogger(quietLogger()))

		got, err := r.Candles(context.Background(), testSym, Interval1d, 10)
		if err != nil {
			t.Fatalf("a cache read failure must not fail the request: %v", err)
		}
		if len(got.Candles) != 2 {
			t.Errorf("got %d candles, want 2", len(got.Candles))
		}
	})

	t.Run("write failure still returns data", func(t *testing.T) {
		cache := newMemCache()
		cache.saveErr = errors.New("disk full")
		p := &stubProvider{name: "yahoo", candles: makeCandles(1, 2)}
		r := NewRouter(cache, []Provider{p}, WithLogger(quietLogger()))

		got, err := r.Candles(context.Background(), testSym, Interval1d, 10)
		if err != nil {
			t.Fatalf("a cache write failure must not fail the request: %v", err)
		}
		if len(got.Candles) != 2 {
			t.Errorf("got %d candles, want 2", len(got.Candles))
		}
	})
}

func TestRouterTTLPerInterval(t *testing.T) {
	// Overnight IST, so that every age below also lands outside the trading
	// session. Inside one, a bar is still forming and the interval's lifetime
	// deliberately no longer applies — that rule is exercised on its own in
	// TestRouterRefetchesProvisionalBar.
	frozen := time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		interval  Interval
		age       time.Duration
		wantFetch bool
	}{
		{name: "daily inside 6h TTL", interval: Interval1d, age: 3 * time.Hour, wantFetch: false},
		{name: "daily past 6h TTL", interval: Interval1d, age: 7 * time.Hour, wantFetch: true},
		{name: "1m inside 1m TTL", interval: Interval1m, age: 30 * time.Second, wantFetch: false},
		{name: "1m past 1m TTL", interval: Interval1m, age: 2 * time.Minute, wantFetch: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := newMemCache()
			cache.seed(testSym, tc.interval, "yahoo", frozen.Add(-tc.age), 1, 2)
			p := &stubProvider{name: "yahoo", candles: makeCandles(9)}
			r := NewRouter(cache, []Provider{p},
				WithClock(func() time.Time { return frozen }), WithLogger(quietLogger()))

			if _, err := r.Candles(context.Background(), testSym, tc.interval, 10); err != nil {
				t.Fatal(err)
			}
			gotFetch := p.Calls() > 0
			if gotFetch != tc.wantFetch {
				t.Errorf("provider called = %v, want %v (age %v)", gotFetch, tc.wantFetch, tc.age)
			}
		})
	}
}

// TestRouterRefetchesProvisionalBar: a bar captured mid-session is a snapshot
// of a half-finished day — the wrong close, and roughly half the real volume.
// Once trading stops it must be refetched even though its lifetime has not
// expired, or a mid-session guess becomes the permanent record for that day.
func TestRouterRefetchesProvisionalBar(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	capturedMidSession := time.Date(2026, 8, 24, 12, 52, 0, 0, loc)
	afterTheClose := time.Date(2026, 8, 24, 17, 30, 0, 0, loc)

	cache := newMemCache()
	cache.seed(testSym, Interval1d, "yahoo", capturedMidSession, 1, 2)
	p := &stubProvider{name: "yahoo", candles: makeCandles(9)}
	r := NewRouter(cache, []Provider{p},
		WithClock(func() time.Time { return afterTheClose }), WithLogger(quietLogger()))

	if _, err := r.Candles(context.Background(), testSym, Interval1d, 10); err != nil {
		t.Fatal(err)
	}
	if p.Calls() == 0 {
		t.Error("served a mid-session snapshot after the close instead of refetching the settled bar")
	}

	// A bar that was already settled when it was cached is not provisional,
	// and must still be served from cache.
	settled := time.Date(2026, 8, 24, 19, 0, 0, 0, loc)
	cache2 := newMemCache()
	cache2.seed(testSym, Interval1d, "yahoo", settled, 1, 2)
	p2 := &stubProvider{name: "yahoo", candles: makeCandles(9)}
	r2 := NewRouter(cache2, []Provider{p2},
		WithClock(func() time.Time { return settled.Add(time.Hour) }), WithLogger(quietLogger()))

	if _, err := r2.Candles(context.Background(), testSym, Interval1d, 10); err != nil {
		t.Fatal(err)
	}
	if p2.Calls() != 0 {
		t.Error("refetched a bar that was already settled when cached")
	}
}

func TestRouterSingleFlight(t *testing.T) {
	// Ten watchlist tiles refreshing the same symbol at once must cost one
	// upstream call, not ten.
	release := make(chan struct{})
	p := &blockingProvider{stub: stubProvider{name: "yahoo", candles: makeCandles(1, 2)}, gate: release}
	r := NewRouter(newMemCache(), []Provider{p}, WithLogger(quietLogger()))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Candles(context.Background(), testSym, Interval1d, 10)
		}()
	}
	// Give the goroutines a moment to pile up on the in-flight call.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := p.Calls(); got != 1 {
		t.Errorf("provider calls = %d, want 1 — concurrent requests must coalesce", got)
	}
}

type blockingProvider struct {
	stub stubProvider
	gate chan struct{}
}

func (b *blockingProvider) Name() string { return b.stub.Name() }
func (b *blockingProvider) Candles(ctx context.Context, s Symbol, iv Interval, n int) (Bars, error) {
	<-b.gate
	return b.stub.Candles(ctx, s, iv, n)
}
func (b *blockingProvider) Quote(ctx context.Context, s Symbol) (Quote, error) {
	<-b.gate
	return b.stub.Quote(ctx, s)
}
func (b *blockingProvider) Calls() int { return b.stub.Calls() }

func TestRouterQuote(t *testing.T) {
	frozen := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return frozen }

	t.Run("falls back and flags stale", func(t *testing.T) {
		cache := newMemCache()
		cache.quotes[testSym.String()] = CachedQuote{
			Quote:     Quote{Symbol: testSym, Price: 100, Currency: "INR"},
			Source:    "yahoo",
			FetchedAt: frozen.Add(-time.Hour),
		}
		p := &stubProvider{name: "yahoo", err: errors.New("429")}
		r := NewRouter(cache, []Provider{p}, WithClock(clock), WithLogger(quietLogger()))

		got, err := r.Quote(context.Background(), testSym)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Stale {
			t.Error("stale = false, want true")
		}
		if got.Quote.Price != 100 {
			t.Errorf("price = %v, want 100", got.Quote.Price)
		}
	})

	t.Run("fresh cache skips the provider", func(t *testing.T) {
		cache := newMemCache()
		cache.quotes[testSym.String()] = CachedQuote{
			Quote:     Quote{Symbol: testSym, Price: 100},
			Source:    "yahoo",
			FetchedAt: frozen.Add(-30 * time.Second),
		}
		p := &stubProvider{name: "yahoo", quote: Quote{Price: 999}}
		r := NewRouter(cache, []Provider{p}, WithClock(clock), WithLogger(quietLogger()))

		got, err := r.Quote(context.Background(), testSym)
		if err != nil {
			t.Fatal(err)
		}
		if got.Quote.Price != 100 || p.Calls() != 0 {
			t.Errorf("price = %v with %d provider calls, want 100 with 0", got.Quote.Price, p.Calls())
		}
	})

	t.Run("currency is filled from the symbol", func(t *testing.T) {
		p := &stubProvider{name: "yahoo", quote: Quote{Price: 100}}
		r := NewRouter(newMemCache(), []Provider{p}, WithClock(clock), WithLogger(quietLogger()))

		got, err := r.Quote(context.Background(), testSym)
		if err != nil {
			t.Fatal(err)
		}
		if got.Quote.Currency != "INR" {
			t.Errorf("currency = %q, want INR", got.Quote.Currency)
		}
		if got.Quote.Symbol != testSym {
			t.Errorf("symbol = %v, want %v", got.Quote.Symbol, testSym)
		}
	})
}

func TestRouterPropagatesResolvedSymbol(t *testing.T) {
	// A venue substitution must reach the API, and must survive the trip
	// through the cache — a chart drawn from a sibling listing has to stay
	// labelled as such on every subsequent read.
	cache := newMemCache()
	p := &stubProvider{name: "yahoo", candles: makeCandles(1, 2), resolved: "RELIANCE.NSE"}
	r := NewRouter(cache, []Provider{p}, WithLogger(quietLogger()))

	got, err := r.Candles(context.Background(), testSym, Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResolvedSymbol != "RELIANCE.NSE" {
		t.Errorf("ResolvedSymbol = %q, wanted the substitution surfaced", got.ResolvedSymbol)
	}

	cached, err := r.Candles(context.Background(), testSym, Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if cached.ResolvedSymbol != "RELIANCE.NSE" {
		t.Errorf("after a cache hit, ResolvedSymbol = %q, want it preserved", cached.ResolvedSymbol)
	}
}

func TestRouterNoResolvedSymbolWhenExact(t *testing.T) {
	p := &stubProvider{name: "yahoo", candles: makeCandles(1, 2)}
	r := NewRouter(newMemCache(), []Provider{p}, WithLogger(quietLogger()))
	got, err := r.Candles(context.Background(), testSym, Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResolvedSymbol != "" {
		t.Errorf("ResolvedSymbol = %q, want empty when the exact listing served the data", got.ResolvedSymbol)
	}
}

func TestRouterCacheCoverage(t *testing.T) {
	// A shallow warm-up must not starve a deeper request for the rest of the
	// TTL: the sparkline asks for 30 bars, the chart then asks for 400 and
	// has to actually get them.
	frozen := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return frozen }

	deep := make([]float64, 400)
	for i := range deep {
		deep[i] = float64(i)
	}

	cache := newMemCache()
	p := &stubProvider{name: "yahoo", candles: makeCandles(deep...)}
	r := NewRouter(cache, []Provider{p}, WithClock(clock), WithLogger(quietLogger()))

	shallow, err := r.Candles(context.Background(), testSym, Interval1d, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(shallow.Candles) != 400 {
		// The stub returns everything it has; the point is the fetch happened.
		t.Logf("warm-up returned %d bars", len(shallow.Candles))
	}
	if p.Calls() != 1 {
		t.Fatalf("warm-up made %d calls, want 1", p.Calls())
	}

	full, err := r.Candles(context.Background(), testSym, Interval1d, 400)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Candles) != 400 {
		t.Errorf("deep request returned %d bars, want 400", len(full.Candles))
	}
}

func TestRouterCacheServesWhenCoverageIsSufficient(t *testing.T) {
	frozen := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return frozen }

	tests := []struct {
		name        string
		cachedBars  int
		coverage    int
		requested   int
		wantRefetch bool
	}{
		{name: "enough bars cached", cachedBars: 100, coverage: 100, requested: 50, wantRefetch: false},
		{name: "exactly enough bars", cachedBars: 50, coverage: 50, requested: 50, wantRefetch: false},
		{name: "too few bars, shallow prior fetch", cachedBars: 30, coverage: 30, requested: 400, wantRefetch: true},
		{
			// The provider was already asked for 400 and only had 30; asking
			// again inside the TTL would just burn budget for the same answer.
			name: "too few bars but the provider has no more", cachedBars: 30, coverage: 400,
			requested: 400, wantRefetch: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			closes := make([]float64, tc.cachedBars)
			for i := range closes {
				closes[i] = float64(i)
			}
			cache := newMemCache()
			cache.seed(testSym, Interval1d, "yahoo", frozen.Add(-time.Minute), closes...)
			// seed() sets coverage to the bar count; override for the case
			// that models a deep-but-empty-handed prior fetch.
			cached := cache.series[cache.key(testSym, Interval1d)]
			cached.RequestedLimit = tc.coverage
			cache.series[cache.key(testSym, Interval1d)] = cached

			p := &stubProvider{name: "yahoo", candles: makeCandles(1, 2, 3)}
			r := NewRouter(cache, []Provider{p}, WithClock(clock), WithLogger(quietLogger()))

			if _, err := r.Candles(context.Background(), testSym, Interval1d, tc.requested); err != nil {
				t.Fatal(err)
			}
			gotRefetch := p.Calls() > 0
			if gotRefetch != tc.wantRefetch {
				t.Errorf("refetched = %v, want %v", gotRefetch, tc.wantRefetch)
			}
		})
	}
}
