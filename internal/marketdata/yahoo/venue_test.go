package yahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// chartWith builds a chart payload with n usable daily bars.
func chartWith(symbol string, n int) []byte {
	ts := make([]int64, n)
	o := make([]float64, n)
	h := make([]float64, n)
	l := make([]float64, n)
	c := make([]float64, n)
	v := make([]float64, n)
	for i := 0; i < n; i++ {
		ts[i] = 1755178200 + int64(i)*86400
		base := 100 + float64(i)
		o[i], h[i], l[i], c[i], v[i] = base, base+1, base-1, base+0.5, 1000
	}
	payload := map[string]any{
		"chart": map[string]any{
			"result": []any{map[string]any{
				"meta": map[string]any{
					"currency": "INR", "symbol": symbol, "regularMarketPrice": 100.5,
				},
				"timestamp": ts,
				"indicators": map[string]any{
					"quote": []any{map[string]any{
						"open": o, "high": h, "low": l, "close": c, "volume": v,
					}},
				},
			}},
			"error": nil,
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

// venueServer serves a configurable number of bars per vendor symbol and
// records which ones were asked for.
type venueServer struct {
	*httptest.Server
	mu      sync.Mutex
	bars    map[string]int
	asked   []string
	missing map[string]bool
}

func newVenueServer(t *testing.T, bars map[string]int) *venueServer {
	t.Helper()
	vs := &venueServer{bars: bars, missing: map[string]bool{}}
	vs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path is /v8/finance/chart/<vendorSymbol>
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		sym := parts[len(parts)-1]

		vs.mu.Lock()
		vs.asked = append(vs.asked, sym)
		n, known := vs.bars[sym]
		vs.mu.Unlock()

		if !known {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(chartWith(sym, n))
	}))
	t.Cleanup(vs.Close)
	return vs
}

func (v *venueServer) askedFor() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.asked...)
}

func TestVenueFallback(t *testing.T) {
	tests := []struct {
		name         string
		symbol       string
		bars         map[string]int
		limit        int
		wantCandles  int
		wantResolved string
		wantAsked    []string
		wantErr      bool
	}{
		{
			name:         "healthy BSE listing is used directly",
			symbol:       "TCS.BSE",
			bars:         map[string]int{"TCS.BO": 240, "TCS.NS": 240},
			limit:        100,
			wantCandles:  100,
			wantResolved: "",
			wantAsked:    []string{"TCS.BO"},
		},
		{
			name:         "a one-bar BSE feed falls back to NSE, labelled",
			symbol:       "RELIANCE.BSE",
			bars:         map[string]int{"RELIANCE.BO": 1, "RELIANCE.NS": 240},
			limit:        100,
			wantCandles:  100,
			wantResolved: "RELIANCE.NSE",
			wantAsked:    []string{"RELIANCE.BO", "RELIANCE.NS"},
		},
		{
			name:         "a missing BSE listing falls back to NSE",
			symbol:       "RELIANCE.BSE",
			bars:         map[string]int{"RELIANCE.NS": 60},
			limit:        100,
			wantCandles:  60,
			wantResolved: "RELIANCE.NSE",
			wantAsked:    []string{"RELIANCE.BO", "RELIANCE.NS"},
		},
		{
			name:         "NSE falls back to BSE symmetrically",
			symbol:       "RELIANCE.NSE",
			bars:         map[string]int{"RELIANCE.BO": 60},
			limit:        100,
			wantCandles:  60,
			wantResolved: "RELIANCE.BSE",
			wantAsked:    []string{"RELIANCE.NS", "RELIANCE.BO"},
		},
		{
			name:         "the better of two thin feeds wins",
			symbol:       "THIN.BSE",
			bars:         map[string]int{"THIN.BO": 1, "THIN.NS": 3},
			limit:        100,
			wantCandles:  3,
			wantResolved: "THIN.NSE",
			wantAsked:    []string{"THIN.BO", "THIN.NS"},
		},
		{
			name:      "both venues missing is an error",
			symbol:    "GONE.BSE",
			bars:      map[string]int{},
			limit:     100,
			wantErr:   true,
			wantAsked: []string{"GONE.BO", "GONE.NS"},
		},
		{
			name:         "US symbols have no sibling venue to try",
			symbol:       "AAPL",
			bars:         map[string]int{"AAPL": 1},
			limit:        100,
			wantCandles:  1,
			wantResolved: "",
			wantAsked:    []string{"AAPL"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newVenueServer(t, tc.bars)
			c := New(WithBaseURL(srv.URL))
			sym := marketdata.MustParseSymbol(tc.symbol)

			bars, err := c.Candles(context.Background(), sym, marketdata.Interval1d, tc.limit)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %d candles", len(bars.Candles))
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(bars.Candles) != tc.wantCandles {
					t.Errorf("got %d candles, want %d", len(bars.Candles), tc.wantCandles)
				}
				if bars.ResolvedSymbol != tc.wantResolved {
					t.Errorf("ResolvedSymbol = %q, want %q", bars.ResolvedSymbol, tc.wantResolved)
				}
			}

			asked := srv.askedFor()
			if len(asked) != len(tc.wantAsked) {
				t.Fatalf("asked upstream for %v, want %v", asked, tc.wantAsked)
			}
			for i, w := range tc.wantAsked {
				if asked[i] != w {
					t.Errorf("request %d was for %q, want %q", i, asked[i], w)
				}
			}
		})
	}
}

func TestVenueFallbackDoesNotSpendAnExtraCallWhenHealthy(t *testing.T) {
	// The sibling venue must only be tried when the first one disappoints.
	srv := newVenueServer(t, map[string]int{"TCS.BO": 240, "TCS.NS": 240})
	c := New(WithBaseURL(srv.URL))

	if _, err := c.Candles(context.Background(), marketdata.MustParseSymbol("TCS.BSE"), marketdata.Interval1d, 200); err != nil {
		t.Fatal(err)
	}
	if got := len(srv.askedFor()); got != 1 {
		t.Errorf("made %d upstream calls, want 1", got)
	}
}

func TestCandidates(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"AAPL", []string{"AAPL"}},
		{"RELIANCE.BSE", []string{"RELIANCE.BSE", "RELIANCE.NSE"}},
		{"RELIANCE.NSE", []string{"RELIANCE.NSE", "RELIANCE.BSE"}},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := candidates(marketdata.MustParseSymbol(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i, w := range tc.want {
				if got[i].String() != w {
					t.Errorf("candidate %d = %s, want %s", i, got[i], w)
				}
			}
		})
	}
}
