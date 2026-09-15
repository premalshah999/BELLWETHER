package twelvedata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeBudget is an in-memory stand-in for the persistent counter.
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

// server serves a fixture and records the query it was called with.
type server struct {
	*httptest.Server
	mu      sync.Mutex
	queries []url.Values
	body    []byte
	status  int
}

func newServer(t *testing.T, body []byte) *server {
	t.Helper()
	s := &server{body: body, status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query())
		body, status := s.body, s.status
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

func (s *server) at(i int) url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries[i]
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

func TestParseRealResponse(t *testing.T) {
	candles, err := ParseTimeSeries(fixture(t, "daily_aapl.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 100 {
		t.Fatalf("got %d candles, want 100", len(candles))
	}

	// Twelve Data returns newest first; every consumer expects oldest first.
	for i := 1; i < len(candles); i++ {
		if !candles[i].Time.After(candles[i-1].Time) {
			t.Fatalf("candle %d at %v is not after %v — series is not oldest-first",
				i, candles[i].Time, candles[i-1].Time)
		}
	}

	last := candles[len(candles)-1]
	if last.Time.Format("2006-01-02") != "2026-08-24" {
		t.Errorf("last bar is %v, want 2026-08-24", last.Time)
	}
	if last.Close != 310.35001 {
		t.Errorf("last close = %v, want 310.35001", last.Close)
	}
	if last.Volume != 34379936 {
		t.Errorf("last volume = %v, want 34379936", last.Volume)
	}
	for i, c := range candles {
		if c.High < c.Low || c.Close <= 0 {
			t.Errorf("candle %d is incoherent: %+v", i, c)
		}
	}
}

func TestParseIntraday(t *testing.T) {
	candles, err := ParseTimeSeries(fixture(t, "intraday_5min.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("got %d candles, want 2", len(candles))
	}
	// The date-time form must parse, not just the date-only one.
	if candles[1].Time.Format("2006-01-02 15:04") != "2026-08-25 15:25" {
		t.Errorf("intraday timestamp = %v", candles[1].Time)
	}
}

func TestParseDropsUnusableRows(t *testing.T) {
	// Four rows: one with an empty high, one with a malformed date. Both are
	// dropped; the empty-volume row survives because volume may be absent.
	candles, err := ParseTimeSeries(fixture(t, "daily_dirty.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("got %d candles, want 2", len(candles))
	}
	for i, c := range candles {
		if c.Open == 0 || c.High == 0 || c.Low == 0 || c.Close == 0 {
			t.Errorf("candle %d carries a zero price: %+v", i, c)
		}
	}
	if candles[0].Volume != 0 {
		t.Errorf("the empty-volume row should read 0, got %v", candles[0].Volume)
	}
}

func TestParseErrorEnvelopes(t *testing.T) {
	// Twelve Data reports failures in a JSON envelope, sometimes under an
	// HTTP 200. Each maps to the error the router knows how to route around.
	tests := []struct {
		name    string
		fixture string
		wantErr error
	}{
		{
			// Out of credits: fall through to another provider, do not mark
			// this one down.
			name: "rate limited", fixture: "error_ratelimit.json",
			wantErr: marketdata.ErrBudgetExhausted,
		},
		{
			name: "unknown symbol", fixture: "error_notfound.json",
			wantErr: marketdata.ErrNoData,
		},
		{
			// A bad key is a configuration problem, not something retrying
			// will fix.
			name: "bad api key", fixture: "error_unauthorized.json",
			wantErr: marketdata.ErrNotSupported,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candles, err := ParseTimeSeries(fixture(t, tc.fixture))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if len(candles) != 0 {
				t.Errorf("got %d candles alongside an error", len(candles))
			}
		})
	}
}

func TestParseMalformed(t *testing.T) {
	for _, body := range []string{"", "not json", "[]", `{"values":"a string"}`, `{"status":"ok","values":[]}`} {
		candles, err := ParseTimeSeries([]byte(body))
		if err == nil {
			t.Errorf("body %q: want an error, got %d candles", body, len(candles))
		}
	}
}

// ---------------------------------------------------------------------------
// Symbol and interval mapping
// ---------------------------------------------------------------------------

func TestVendorParams(t *testing.T) {
	// RELIANCE trades on both NSE and BSE under the same ticker, so the
	// exchange parameter is what disambiguates the listing.
	tests := []struct {
		in           string
		wantSymbol   string
		wantExchange string
	}{
		{in: "AAPL", wantSymbol: "AAPL", wantExchange: ""},
		{in: "RELIANCE.NSE", wantSymbol: "RELIANCE", wantExchange: "NSE"},
		{in: "RELIANCE.BSE", wantSymbol: "RELIANCE", wantExchange: "BSE"},
		{in: "TCS.BSE", wantSymbol: "TCS", wantExchange: "BSE"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := vendorParams(marketdata.MustParseSymbol(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if got.Get("symbol") != tc.wantSymbol {
				t.Errorf("symbol = %q, want %q", got.Get("symbol"), tc.wantSymbol)
			}
			if got.Get("exchange") != tc.wantExchange {
				t.Errorf("exchange = %q, want %q", got.Get("exchange"), tc.wantExchange)
			}
		})
	}
}

func TestVendorInterval(t *testing.T) {
	tests := map[marketdata.Interval]string{
		marketdata.Interval1m:  "1min",
		marketdata.Interval5m:  "5min",
		marketdata.Interval15m: "15min",
		marketdata.Interval1h:  "1h",
		marketdata.Interval1d:  "1day",
		marketdata.Interval1wk: "1week",
	}
	for iv, want := range tests {
		got, err := vendorInterval(iv)
		if err != nil {
			t.Fatalf("%s: %v", iv, err)
		}
		if got != want {
			t.Errorf("vendorInterval(%s) = %q, want %q", iv, got, want)
		}
	}
	if _, err := vendorInterval(marketdata.Interval("3mo")); !errors.Is(err, marketdata.ErrNotSupported) {
		t.Errorf("error = %v, want ErrNotSupported", err)
	}
}

// ---------------------------------------------------------------------------
// Client behaviour
// ---------------------------------------------------------------------------

func TestCandlesRequestShape(t *testing.T) {
	srv := newServer(t, fixture(t, "daily_aapl.json"))
	c := New("test-key", 800, newFakeBudget(), WithBaseURL(srv.URL))

	if _, err := c.Candles(context.Background(),
		marketdata.MustParseSymbol("RELIANCE.BSE"), marketdata.Interval1d, 150); err != nil {
		t.Fatal(err)
	}

	q := srv.at(0)
	if q.Get("symbol") != "RELIANCE" || q.Get("exchange") != "BSE" {
		t.Errorf("symbol/exchange = %q/%q", q.Get("symbol"), q.Get("exchange"))
	}
	if q.Get("interval") != "1day" {
		t.Errorf("interval = %q", q.Get("interval"))
	}
	if q.Get("outputsize") != "150" {
		t.Errorf("outputsize = %q, want 150", q.Get("outputsize"))
	}
	if q.Get("apikey") != "test-key" {
		t.Errorf("apikey = %q", q.Get("apikey"))
	}
	// Bar times are stored in UTC and displayed in IST; asking for UTC keeps
	// the storage side unambiguous.
	if q.Get("timezone") != "UTC" {
		t.Errorf("timezone = %q, want UTC", q.Get("timezone"))
	}
}

func TestCandlesRespectsLimit(t *testing.T) {
	srv := newServer(t, fixture(t, "daily_aapl.json"))
	c := New("k", 800, newFakeBudget(), WithBaseURL(srv.URL))

	bars, err := c.Candles(context.Background(),
		marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture holds 100 bars regardless of what we asked for; the client
	// trims to the most recent window.
	if len(bars.Candles) != 10 {
		t.Errorf("got %d candles, want 10", len(bars.Candles))
	}
	if bars.ResolvedSymbol != "" {
		t.Errorf("ResolvedSymbol = %q, want empty: twelvedata carries both venues directly",
			bars.ResolvedSymbol)
	}
}

func TestOutputSizeIsCapped(t *testing.T) {
	srv := newServer(t, fixture(t, "daily_aapl.json"))
	c := New("k", 800, newFakeBudget(), WithBaseURL(srv.URL))

	c.Candles(context.Background(), marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 99999)
	if got := srv.at(0).Get("outputsize"); got != "5000" {
		t.Errorf("outputsize = %q, want it capped at 5000", got)
	}
}

func TestQuoteDerivesFromTheSeries(t *testing.T) {
	// One call, not two: the quote and the chart then always agree, and it
	// costs a single API credit.
	srv := newServer(t, fixture(t, "daily_aapl.json"))
	c := New("k", 800, newFakeBudget(), WithBaseURL(srv.URL))

	q, err := c.Quote(context.Background(), marketdata.MustParseSymbol("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	if srv.count() != 1 {
		t.Errorf("made %d requests for a quote, want 1", srv.count())
	}
	if q.Price != 310.35001 {
		t.Errorf("price = %v, want 310.35001", q.Price)
	}
	if q.PrevClose != 309.35001 {
		t.Errorf("prevClose = %v, want 309.35001", q.PrevClose)
	}
	if q.Currency != "USD" {
		t.Errorf("currency = %q", q.Currency)
	}
	wantChange := 310.35001 - 309.35001
	if diff := q.Change - wantChange; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("change = %v, want %v", q.Change, wantChange)
	}
}

func TestDailyBudget(t *testing.T) {
	tests := []struct {
		name         string
		limit        int
		apiKey       string
		calls        int
		wantOK       int
		wantRequests int
		wantErr      error
	}{
		{name: "within the limit", limit: 3, apiKey: "k", calls: 3, wantOK: 3, wantRequests: 3},
		{
			name:  "the call past the limit never reaches the network",
			limit: 2, apiKey: "k", calls: 3, wantOK: 2, wantRequests: 2,
			wantErr: marketdata.ErrBudgetExhausted,
		},
		{
			name:  "a zero limit disables the provider",
			limit: 0, apiKey: "k", calls: 1, wantOK: 0, wantRequests: 0,
			wantErr: marketdata.ErrBudgetExhausted,
		},
		{
			name:  "no key is unsupported, not exhausted",
			limit: 800, apiKey: "", calls: 1, wantOK: 0, wantRequests: 0,
			wantErr: marketdata.ErrNotSupported,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, fixture(t, "daily_aapl.json"))
			c := New(tc.apiKey, tc.limit, newFakeBudget(), WithBaseURL(srv.URL))

			var ok int
			var lastErr error
			for i := 0; i < tc.calls; i++ {
				if _, err := c.Candles(context.Background(),
					marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10); err != nil {
					lastErr = err
				} else {
					ok++
				}
			}
			if ok != tc.wantOK {
				t.Errorf("successful calls = %d, want %d (last error: %v)", ok, tc.wantOK, lastErr)
			}
			if srv.count() != tc.wantRequests {
				t.Errorf("HTTP requests = %d, want %d", srv.count(), tc.wantRequests)
			}
			if tc.wantErr != nil && !errors.Is(lastErr, tc.wantErr) {
				t.Errorf("error = %v, want %v", lastErr, tc.wantErr)
			}
		})
	}
}

func TestBudgetResetsDaily(t *testing.T) {
	srv := newServer(t, fixture(t, "daily_aapl.json"))
	day := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	c := New("k", 1, newFakeBudget(), WithBaseURL(srv.URL),
		WithClock(func() time.Time { return day }))
	sym := marketdata.MustParseSymbol("AAPL")

	if _, err := c.Candles(context.Background(), sym, marketdata.Interval1d, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Candles(context.Background(), sym, marketdata.Interval1d, 5); !errors.Is(err, marketdata.ErrBudgetExhausted) {
		t.Fatalf("second call error = %v, want ErrBudgetExhausted", err)
	}
	day = day.Add(24 * time.Hour)
	if _, err := c.Candles(context.Background(), sym, marketdata.Interval1d, 5); err != nil {
		t.Errorf("the new day should have a fresh allowance, got %v", err)
	}
}

func TestUpstreamRateLimitStillCountsTheSpentCall(t *testing.T) {
	srv := newServer(t, fixture(t, "error_ratelimit.json"))
	budget := newFakeBudget()
	c := New("k", 800, budget, WithBaseURL(srv.URL))

	_, err := c.Candles(context.Background(),
		marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10)
	if !errors.Is(err, marketdata.ErrBudgetExhausted) {
		t.Errorf("error = %v, want ErrBudgetExhausted", err)
	}
	used, _, _ := c.Usage(context.Background())
	if used != 1 {
		t.Errorf("usage = %d, want 1 — a spent request must still be counted", used)
	}
}

func TestConfigured(t *testing.T) {
	if New("", 800, newFakeBudget()).Configured() {
		t.Error("Configured = true without a key")
	}
	if !New("k", 800, newFakeBudget()).Configured() {
		t.Error("Configured = false with a key")
	}
}

func TestAPIKeyNotLeakedInErrors(t *testing.T) {
	const key = "td-SUPER-SECRET-KEY"
	c := New(key, 800, newFakeBudget(), WithBaseURL("http://127.0.0.1:1"))

	_, err := c.Candles(context.Background(),
		marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10)
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "SUPER-SECRET-KEY") {
		t.Errorf("the API key leaked into an error: %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := newServer(t, fixture(t, "daily_aapl.json"))
	c := New("k", 800, newFakeBudget(), WithBaseURL(srv.URL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Candles(ctx, marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10); err == nil {
		t.Error("want an error on a cancelled context")
	}
}
