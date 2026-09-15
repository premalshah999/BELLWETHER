package alphavantage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// fakeBudget is an in-memory stand-in for the persistent counter, so these
// tests exercise the adapter's budgeting decisions without a database.
type fakeBudget struct {
	mu   sync.Mutex
	used map[string]int
}

func newFakeBudget() *fakeBudget { return &fakeBudget{used: map[string]int{}} }

func (f *fakeBudget) ConsumeBudget(_ context.Context, provider, period string, limit int) (bool, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := provider + "|" + period
	if f.used[k] >= limit {
		return false, 0, nil
	}
	f.used[k]++
	return true, limit - f.used[k], nil
}

func (f *fakeBudget) BudgetUsage(_ context.Context, provider, period string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.used[provider+"|"+period], nil
}

// countingServer serves the recorded daily payload and counts requests, so a
// test can assert that a refused call never reached the network.
func countingServer(t *testing.T, fixture string) (*httptest.Server, *int32Counter) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	hits := &int32Counter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

type int32Counter struct {
	mu sync.Mutex
	n  int
}

func (c *int32Counter) Add(n int) { c.mu.Lock(); c.n += n; c.mu.Unlock() }
func (c *int32Counter) Get() int  { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func TestDailyBudget(t *testing.T) {
	ibm := marketdata.MustParseSymbol("IBM")

	tests := []struct {
		name         string
		limit        int
		apiKey       string
		calls        int
		wantOK       int
		wantHTTPHits int
		wantFinalErr error
	}{
		{
			name:         "calls within the limit succeed",
			limit:        3,
			apiKey:       "test-key",
			calls:        3,
			wantOK:       3,
			wantHTTPHits: 3,
		},
		{
			name:         "the call past the limit is refused before the network",
			limit:        2,
			apiKey:       "test-key",
			calls:        3,
			wantOK:       2,
			wantHTTPHits: 2,
			wantFinalErr: marketdata.ErrBudgetExhausted,
		},
		{
			name:         "a zero limit disables the provider entirely",
			limit:        0,
			apiKey:       "test-key",
			calls:        1,
			wantOK:       0,
			wantHTTPHits: 0,
			wantFinalErr: marketdata.ErrBudgetExhausted,
		},
		{
			name:         "a negative limit disables the provider entirely",
			limit:        -5,
			apiKey:       "test-key",
			calls:        1,
			wantOK:       0,
			wantHTTPHits: 0,
			wantFinalErr: marketdata.ErrBudgetExhausted,
		},
		{
			name:         "no API key means unsupported, not exhausted",
			limit:        25,
			apiKey:       "",
			calls:        1,
			wantOK:       0,
			wantHTTPHits: 0,
			wantFinalErr: marketdata.ErrNotSupported,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, hits := countingServer(t, "daily_ibm.json")
			c := New(tc.apiKey, tc.limit, newFakeBudget(), WithBaseURL(srv.URL))

			var ok int
			var lastErr error
			for i := 0; i < tc.calls; i++ {
				if _, err := c.Candles(context.Background(), ibm, marketdata.Interval1d, 50); err != nil {
					lastErr = err
				} else {
					ok++
				}
			}
			if ok != tc.wantOK {
				t.Errorf("successful calls = %d, want %d (last error: %v)", ok, tc.wantOK, lastErr)
			}
			if hits.Get() != tc.wantHTTPHits {
				t.Errorf("HTTP requests = %d, want %d — a refused call must not reach the network", hits.Get(), tc.wantHTTPHits)
			}
			if tc.wantFinalErr != nil && !errors.Is(lastErr, tc.wantFinalErr) {
				t.Errorf("last error = %v, want %v", lastErr, tc.wantFinalErr)
			}
		})
	}
}

func TestBudgetChargesQuotesToo(t *testing.T) {
	// Quotes and candles draw on the same daily allowance; a quote must not be
	// a free call.
	srv, hits := countingServer(t, "quote_ibm.json")
	c := New("test-key", 1, newFakeBudget(), WithBaseURL(srv.URL))
	ibm := marketdata.MustParseSymbol("IBM")

	if _, err := c.Quote(context.Background(), ibm); err != nil {
		t.Fatalf("first quote: %v", err)
	}
	_, err := c.Quote(context.Background(), ibm)
	if !errors.Is(err, marketdata.ErrBudgetExhausted) {
		t.Errorf("second quote error = %v, want ErrBudgetExhausted", err)
	}
	if hits.Get() != 1 {
		t.Errorf("HTTP requests = %d, want 1", hits.Get())
	}
}

func TestBudgetResetsOnDayBoundary(t *testing.T) {
	srv, hits := countingServer(t, "daily_ibm.json")
	budget := newFakeBudget()

	day := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	c := New("test-key", 1, budget, WithBaseURL(srv.URL), WithClock(func() time.Time { return day }))
	ibm := marketdata.MustParseSymbol("IBM")

	if _, err := c.Candles(context.Background(), ibm, marketdata.Interval1d, 10); err != nil {
		t.Fatalf("day 1 first call: %v", err)
	}
	if _, err := c.Candles(context.Background(), ibm, marketdata.Interval1d, 10); !errors.Is(err, marketdata.ErrBudgetExhausted) {
		t.Fatalf("day 1 second call error = %v, want ErrBudgetExhausted", err)
	}

	// Crossing into the next UTC day starts a fresh bucket.
	day = day.Add(24 * time.Hour)
	if _, err := c.Candles(context.Background(), ibm, marketdata.Interval1d, 10); err != nil {
		t.Fatalf("day 2 call should be allowed, got: %v", err)
	}
	if hits.Get() != 2 {
		t.Errorf("HTTP requests = %d, want 2", hits.Get())
	}
}

func TestUsageReporting(t *testing.T) {
	srv, _ := countingServer(t, "daily_ibm.json")
	c := New("test-key", 25, newFakeBudget(), WithBaseURL(srv.URL))
	ibm := marketdata.MustParseSymbol("IBM")

	used, limit, err := c.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if used != 0 || limit != 25 {
		t.Fatalf("initial usage = %d/%d, want 0/25", used, limit)
	}

	for i := 0; i < 3; i++ {
		if _, err := c.Candles(context.Background(), ibm, marketdata.Interval1d, 10); err != nil {
			t.Fatal(err)
		}
	}
	used, _, err = c.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if used != 3 {
		t.Errorf("usage after 3 calls = %d, want 3", used)
	}
}

func TestUnconfiguredReporting(t *testing.T) {
	if New("", 25, newFakeBudget()).Configured() {
		t.Error("Configured() = true for an empty API key")
	}
	if !New("k", 25, newFakeBudget()).Configured() {
		t.Error("Configured() = false for a supplied API key")
	}
}

func TestUpstreamThrottleDoesNotDoubleCharge(t *testing.T) {
	// When Alpha Vantage answers 200 with an "Information" throttle notice,
	// the request is already spent. The adapter must surface budget
	// exhaustion so the router degrades rather than hammering the endpoint.
	srv, hits := countingServer(t, "information_ratelimit.json")
	c := New("test-key", 5, newFakeBudget(), WithBaseURL(srv.URL))

	_, err := c.Candles(context.Background(), marketdata.MustParseSymbol("IBM"), marketdata.Interval1d, 10)
	if !errors.Is(err, marketdata.ErrBudgetExhausted) {
		t.Errorf("error = %v, want ErrBudgetExhausted", err)
	}
	if hits.Get() != 1 {
		t.Errorf("HTTP requests = %d, want 1", hits.Get())
	}
	used, _, _ := c.Usage(context.Background())
	if used != 1 {
		t.Errorf("usage = %d, want 1 — the spent request must still be counted", used)
	}
}
