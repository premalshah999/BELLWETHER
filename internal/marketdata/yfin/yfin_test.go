package yfin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// sidecar stands in for the Python service, serving a configurable number of
// bars per vendor symbol and recording what was asked for.
type sidecar struct {
	*httptest.Server
	mu      sync.Mutex
	queries []url.Values
	bars    map[string]int
	status  int
	body    string
}

func newSidecar(t *testing.T, bars map[string]int) *sidecar {
	t.Helper()
	s := &sidecar{bars: bars, status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		s.mu.Lock()
		s.queries = append(s.queries, q)
		n, known := s.bars[q.Get("symbol")]
		status, body := s.status, s.body
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if body != "" {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		if !known {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"symbol": q.Get("symbol"), "candles": []any{}, "adjusted": false,
			})
			return
		}

		candles := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			day := 1 + i%27
			candles = append(candles, map[string]any{
				"t": "2026-01-" + twoDigit(day) + "T00:00:00Z",
				"o": 100 + float64(i), "h": 101 + float64(i),
				"l": 99 + float64(i), "c": 100.5 + float64(i), "v": 1000,
			})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"symbol": q.Get("symbol"), "interval": q.Get("interval"),
			"candles": candles, "adjusted": q.Get("adjust") == "true",
			"splits": []any{},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func twoDigit(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func (s *sidecar) at(i int) url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries[i]
}

func (s *sidecar) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

func TestSymbolMapping(t *testing.T) {
	for in, want := range map[string]string{"AAPL": "AAPL", "BRK.B": "BRK-B", "^GSPC": "^GSPC"} {
		if got := vendorSymbol(marketdata.MustParseSymbol(in)); got != want {
			t.Errorf("vendorSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCandlesRequestShape(t *testing.T) {
	srv := newSidecar(t, map[string]int{"AAPL": 100})
	c := New(srv.URL)

	if _, err := c.Candles(context.Background(),
		marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 1300); err != nil {
		t.Fatal(err)
	}
	q := srv.at(0)
	if q.Get("symbol") != "AAPL" || q.Get("interval") != "1d" {
		t.Errorf("symbol/interval = %q/%q", q.Get("symbol"), q.Get("interval"))
	}
	// 1300 daily bars is a little over five years, so it must ask for more
	// than the five-year default rather than silently truncating.
	if q.Get("years") == "" || q.Get("years") < "5" {
		t.Errorf("years = %q, want at least 5 for 1300 daily bars", q.Get("years"))
	}
	// As-traded prices by default: a chart should show what the stock
	// actually traded at.
	if q.Get("adjust") != "false" {
		t.Errorf("adjust = %q, want false by default", q.Get("adjust"))
	}
}

func TestFiveYearsIsTheDefaultDepth(t *testing.T) {
	srv := newSidecar(t, map[string]int{"AAPL": 100})
	c := New(srv.URL)

	// Even a small request pulls five years, because the cache is shared and
	// a later deep request should not need a second fetch.
	c.Candles(context.Background(), marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 30)
	if got := srv.at(0).Get("years"); got != "5" {
		t.Errorf("years = %q, want 5", got)
	}
}

func TestDividendAdjustmentIsOptIn(t *testing.T) {
	srv := newSidecar(t, map[string]int{"AAPL": 10})
	c := New(srv.URL, WithDividendAdjustment(true))

	c.Candles(context.Background(), marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10)
	if got := srv.at(0).Get("adjust"); got != "true" {
		t.Errorf("adjust = %q, want true when opted in", got)
	}
}

func TestUnusableRowsAreDropped(t *testing.T) {
	// A non-positive price is not data. Charting it would draw a spike to
	// zero that never happened.
	srv := newSidecar(t, nil)
	srv.body = `{"symbol":"AAPL","candles":[
      {"t":"2026-01-01T00:00:00Z","o":100,"h":101,"l":99,"c":100.5,"v":10},
      {"t":"2026-01-02T00:00:00Z","o":0,"h":0,"l":0,"c":0,"v":0},
      {"t":"not-a-time","o":1,"h":1,"l":1,"c":1,"v":1},
      {"t":"2026-01-03T00:00:00Z","o":102,"h":103,"l":101,"c":102.5,"v":10}],
      "adjusted":false}`
	c := New(srv.URL)

	bars, err := c.Candles(context.Background(),
		marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars.Candles) != 2 {
		t.Fatalf("got %d candles, want 2", len(bars.Candles))
	}
	for i, cd := range bars.Candles {
		if cd.Close <= 0 {
			t.Errorf("candle %d has a non-positive close: %+v", i, cd)
		}
	}
}

func TestSidecarErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{name: "empty series", status: 200, body: `{"symbol":"AAPL","candles":[]}`, wantErr: marketdata.ErrNoData},
		{name: "upstream error", status: 502, body: `{"error":"YFRateLimitError: too many requests"}`},
		{name: "unreadable body", status: 200, body: `<html>oops</html>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSidecar(t, nil)
			srv.status, srv.body = tc.status, tc.body
			c := New(srv.URL)

			_, err := c.Candles(context.Background(),
				marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10)
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestUnconfigured(t *testing.T) {
	c := New("")
	if c.Configured() {
		t.Error("Configured = true with no address")
	}
	_, err := c.Candles(context.Background(),
		marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10)
	if !errors.Is(err, marketdata.ErrNotSupported) {
		t.Errorf("error = %v, want ErrNotSupported", err)
	}
}

// TestQuoteUsesTheDailyBarWhenTheMarketIsShut.
//
// The clock is pinned rather than left to the wall clock. Quote behaviour now
// depends on whether a session is open, so a test that reads the real time
// passes overnight and fails during market hours — the worst kind of flake,
// because it looks like an unrelated change broke it.
func TestQuoteUsesTheDailyBarWhenTheMarketIsShut(t *testing.T) {
	srv := newSidecar(t, map[string]int{"AAPL": 5})
	// A Sunday: no venue is open.
	closed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	c := New(srv.URL, WithClock(func() time.Time { return closed }))

	q, err := c.Quote(context.Background(), marketdata.MustParseSymbol("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	if srv.count() != 1 {
		t.Errorf("made %d requests, want 1: with nothing trading there is no "+
			"intraday data worth fetching", srv.count())
	}
	if q.Price <= 0 || q.PrevClose <= 0 {
		t.Errorf("quote = %+v", q)
	}
	if q.Currency != "USD" {
		t.Errorf("currency = %q", q.Currency)
	}
}

// TestQuoteReadsMinuteBarsDuringASession is the fix for a frozen screen.
//
// The provider's daily bar for the session in progress updates only every few
// minutes: polled repeatedly it returns identical data, so a quote built from
// it never changes and nothing is ever published to the live stream. Measured
// on AAPL just after the open, the daily bar held one price across four
// fetches a minute apart while the minute bars moved on every one.
func TestQuoteReadsMinuteBarsDuringASession(t *testing.T) {
	srv := newSidecar(t, map[string]int{"AAPL": 5})
	// A Tuesday at 14:00 UTC, which is 10:00 in New York.
	open := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	c := New(srv.URL, WithClock(func() time.Time { return open }))

	if _, err := c.Quote(context.Background(), marketdata.MustParseSymbol("AAPL")); err != nil {
		t.Fatal(err)
	}
	if srv.count() < 2 {
		t.Errorf("made %d requests, want the daily series for the previous "+
			"close and the minute series for the live price", srv.count())
	}
}

// Intraday asks for the span it needs, not the upstream cap.
//
// This used to return a constant that the sidecar read as "give me the
// maximum", so drawing thirty five-minute bars downloaded fifty-nine days of
// them. The span must cover the request with a little slack and no more.
func TestYearsForIntradayCoversTheRequestAndNoMore(t *testing.T) {
	for _, tc := range []struct {
		interval     marketdata.Interval
		limit        int
		wantSessions float64
	}{
		{marketdata.Interval5m, 30, 2},   // 75 bars a session
		{marketdata.Interval5m, 500, 8},  // ceil(500/75)+1
		{marketdata.Interval15m, 150, 7}, // 25 a session
		{marketdata.Interval1h, 70, 11},  // 7 a session
	} {
		got := yearsFor(tc.interval, tc.limit)
		sessions := got * 252
		if math.Abs(sessions-tc.wantSessions) > 0.51 {
			t.Errorf("yearsFor(%s, %d) = %v years = %.1f sessions, want %.0f",
				tc.interval, tc.limit, got, sessions, tc.wantSessions)
		}
		// The whole point: a small request must not ask for a year.
		if got >= 1 {
			t.Errorf("yearsFor(%s, %d) = %v years — that is the old cap behaviour",
				tc.interval, tc.limit, got)
		}
	}
}

func TestYearsFor(t *testing.T) {
	tests := []struct {
		name     string
		interval marketdata.Interval
		limit    int
		wantMin  float64
	}{
		{name: "small daily request still pulls five years", interval: marketdata.Interval1d, limit: 30, wantMin: 5},
		{name: "deep daily request asks for more", interval: marketdata.Interval1d, limit: 2000, wantMin: 7},
		{name: "weekly defaults to five years", interval: marketdata.Interval1wk, limit: 60, wantMin: 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := yearsFor(tc.interval, tc.limit); got < tc.wantMin {
				t.Errorf("yearsFor(%s, %d) = %v, want at least %v", tc.interval, tc.limit, got, tc.wantMin)
			}
		})
	}
}
