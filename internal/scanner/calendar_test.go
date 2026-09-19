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

func nse(t string) marketdata.Symbol {
	return marketdata.Symbol{Ticker: t, Exchange: marketdata.ExchangeNSE}
}
func us(t string) marketdata.Symbol {
	return marketdata.Symbol{Ticker: t, Exchange: marketdata.ExchangeUS}
}

// The vendor suffix is this package's business and nobody else's: symbols go
// out as RELIANCE.NS and must come back as RELIANCE.NSE, or every downstream
// join is against a symbol the rest of the schema has never heard of.
func TestCalendarRetagsVendorSymbols(t *testing.T) {
	var got struct {
		Symbols []string `json:"symbols"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"calendar": map[string]any{
				"AAPL":        map[string]any{"earnings_date": "2026-10-29", "eps_average": 1.98},
				"RELIANCE.NS": map[string]any{"earnings_date": "2026-10-16"},
			},
			"failed": []string{}, "elapsed_seconds": 0.5,
		})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	res, err := c.Calendar(context.Background(), []marketdata.Symbol{us("AAPL"), nse("RELIANCE")})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}

	if want := []string{"AAPL", "RELIANCE.NS"}; !sameSet(got.Symbols, want) {
		t.Errorf("sent %v, want %v (vendor spelling on the wire)", got.Symbols, want)
	}
	byCanonical := map[string]CalendarEntry{}
	for _, e := range res.Entries {
		byCanonical[e.Symbol.String()] = e
	}
	for _, want := range []string{"AAPL", "RELIANCE.NSE"} {
		if _, ok := byCanonical[want]; !ok {
			t.Errorf("missing %q in results; got %v", want, keysOf(byCanonical))
		}
	}
	if e := byCanonical["AAPL"]; e.EarningsDate == nil ||
		!e.EarningsDate.Equal(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("AAPL earnings date = %v, want 2026-10-29", e.EarningsDate)
	}
}

// A row with no upcoming date is not a calendar entry. Storing one would put
// a symbol on the "what is coming up" page with nothing coming up.
func TestCalendarDropsEntriesWithNoDates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"calendar": map[string]any{
				"AAPL": map[string]any{"eps_average": 1.98},                 // estimates but no date
				"MSFT": map[string]any{"earnings_date": "", "eps_low": 1.0}, // empty date
				"NVDA": map[string]any{"earnings_date": "2026-11-17"},
			},
			"failed": []string{},
		})
	}))
	defer srv.Close()

	res, err := (&Client{BaseURL: srv.URL}).Calendar(context.Background(),
		[]marketdata.Symbol{us("AAPL"), us("MSFT"), us("NVDA")})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Symbol.Ticker != "NVDA" {
		t.Fatalf("want only NVDA, got %v", entryTickers(res.Entries))
	}
}

// One malformed date must not cost the batch: the other symbols in the same
// response are still good, and a daily refresh that throws away 1,500 rows
// because of one bad string is worse than a missing field.
func TestCalendarSurvivesAMalformedDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"calendar": map[string]any{
				"AAPL": map[string]any{"earnings_date": "not-a-date", "ex_dividend_date": "2026-11-07"},
				"NVDA": map[string]any{"earnings_date": "2026-11-17"},
			},
			"failed": []string{},
		})
	}))
	defer srv.Close()

	res, err := (&Client{BaseURL: srv.URL}).Calendar(context.Background(),
		[]marketdata.Symbol{us("AAPL"), us("NVDA")})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("want both symbols kept, got %v", entryTickers(res.Entries))
	}
	for _, e := range res.Entries {
		if e.Symbol.Ticker == "AAPL" {
			if e.EarningsDate != nil {
				t.Error("an unparseable earnings date must read as absent, not as a wrong date")
			}
			if e.ExDividendDate == nil {
				t.Error("the good date on the same row must survive")
			}
		}
	}
}

func TestCalendarReportsFailuresAsSymbols(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"calendar": map[string]any{"AAPL": map[string]any{"earnings_date": "2026-10-29"}},
			"failed":   []string{"RELIANCE.NS"},
		})
	}))
	defer srv.Close()

	res, err := (&Client{BaseURL: srv.URL}).Calendar(context.Background(),
		[]marketdata.Symbol{us("AAPL"), nse("RELIANCE")})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if len(res.Failed) != 1 || res.Failed[0].String() != "RELIANCE.NSE" {
		t.Fatalf("failed = %v, want [RELIANCE.NSE] in canonical form", res.Failed)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func entryTickers(entries []CalendarEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Symbol.String())
	}
	return out
}
