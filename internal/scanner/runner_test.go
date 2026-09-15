package scanner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// fakeRunnerStore is the minimal Store a Runner needs, recording nothing
// beyond what these tests check.
type fakeRunnerStore struct{}

func (fakeRunnerStore) SaveScan(ctx context.Context, res Result) (int64, error) { return 1, nil }
func (fakeRunnerStore) MarkExplained(ctx context.Context, id int64, explained bool) error {
	return nil
}

// stubSidecar serves whatever metrics the test wants back, echoing the
// vendor-suffixed symbols it was asked for so Client.Scan's re-tagging path
// is exercised for real.
func stubSidecar(t *testing.T, findingsBySymbol map[string]Metrics) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Symbols []string `json:"symbols"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		metrics := map[string]Metrics{}
		for _, s := range req.Symbols {
			if m, ok := findingsBySymbol[s]; ok {
				metrics[s] = m
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"metrics": metrics, "failed": []string{}, "as_of": time.Now().UTC().Format(time.RFC3339),
			"elapsed_seconds": 0.1,
		})
	}))
}

// discoverable is a finding that HasDiscoverableCause() accepts, so it is
// eligible to become an attention symbol.
func discoverable(symbol string) Metrics {
	return Metrics{Symbol: symbol, Close: 100, VolumeZ: 6, ReturnZ: 6, VolumeRatio: 8}
}

// TestRunPreservesOtherScopesAttention is the regression for splitting the
// market scan into a separately-timed NSE pass and US pass over one shared
// Runner: a scoped run must not erase what the other venue's scan already
// raised.
func TestRunPreservesOtherScopesAttention(t *testing.T) {
	nseSym, _ := marketdata.ParseSymbol("RELIANCE.NSE")
	usSym, _ := marketdata.ParseSymbol("AAPL")

	srv := stubSidecar(t, map[string]Metrics{
		"RELIANCE.NS": discoverable("RELIANCE.NS"),
		"AAPL":        discoverable("AAPL"),
	})
	defer srv.Close()

	r := &Runner{
		Client:   &Client{BaseURL: srv.URL, HTTP: srv.Client()},
		Store:    fakeRunnerStore{},
		Universe: func() []marketdata.Symbol { return []marketdata.Symbol{nseSym, usSym} },
	}
	nseScope := func(s marketdata.Symbol) bool { return s.IsIndian() }
	usScope := func(s marketdata.Symbol) bool { return !s.IsIndian() }

	if _, err := r.Run(context.Background(), nseScope); err != nil {
		t.Fatalf("nse run: %v", err)
	}
	afterNSE := r.AttentionSymbols()
	if len(afterNSE) != 1 || afterNSE[0] != "RELIANCE.NSE" {
		t.Fatalf("after NSE scan, attention = %v, want [RELIANCE.NSE]", afterNSE)
	}

	if _, err := r.Run(context.Background(), usScope); err != nil {
		t.Fatalf("us run: %v", err)
	}
	afterUS := r.AttentionSymbols()
	want := map[string]bool{"RELIANCE.NSE": true, "AAPL": true}
	if len(afterUS) != 2 {
		t.Fatalf("after US scan, attention = %v, want both RELIANCE.NSE (preserved) and AAPL (new)", afterUS)
	}
	for _, s := range afterUS {
		if !want[s] {
			t.Errorf("unexpected attention symbol %q", s)
		}
	}

	// A third, unscoped run replaces everyone -- the manual-trigger case.
	if _, err := r.Run(context.Background(), nil); err != nil {
		t.Fatalf("unscoped run: %v", err)
	}
	afterAll := r.AttentionSymbols()
	if len(afterAll) != 2 {
		t.Fatalf("after unscoped scan, attention = %v, want both symbols found fresh", afterAll)
	}
}
