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
func (fakeRunnerStore) SaveCandles(ctx context.Context, sym marketdata.Symbol, interval marketdata.Interval, source string, bars marketdata.Bars, requestedLimit int) error {
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

// Each run replaces the attention list with what it found unexplained.
func TestRunRaisesAttention(t *testing.T) {
	srv := stubSidecar(t, map[string]Metrics{"AAPL": discoverable("AAPL"), "MSFT": discoverable("MSFT")})
	defer srv.Close()
	r := &Runner{
		Client:   &Client{BaseURL: srv.URL, HTTP: srv.Client()},
		Store:    fakeRunnerStore{},
		Universe: func() []marketdata.Symbol { return []marketdata.Symbol{{Ticker: "AAPL"}, {Ticker: "MSFT"}} },
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.AttentionSymbols(); len(got) != 2 {
		t.Fatalf("attention = %v, want both symbols", got)
	}
}
