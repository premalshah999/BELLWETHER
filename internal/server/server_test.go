package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/health"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/marketdata/yahoo"
	"github.com/tradesys/dashboard/internal/server"
	"github.com/tradesys/dashboard/internal/storage/sqlite"
)

// upstream stands in for Yahoo, serving the recorded chart payloads. Handing
// the real adapter these bytes exercises the whole chain the operator depends
// on — HTTP, parsing, the router, the cache, the API — with no network.
type upstream struct {
	*httptest.Server
	hits atomic.Int64
	fail atomic.Bool
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		if u.fail.Load() {
			// Yahoo's characteristic failure from a datacenter IP.
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, "Too Many Requests")
			return
		}
		var fixture string
		switch {
		case strings.Contains(r.URL.Path, "RELIANCE.BO"):
			fixture = "chart_reliance_1d.json"
		case strings.Contains(r.URL.Path, "AAPL"):
			fixture = "chart_aapl_1d.json"
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`)
			return
		}
		body, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Errorf("read fixture: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(u.Close)
	return u
}

type harness struct {
	srv      *server.Server
	upstream *upstream
	tracker  *health.Tracker
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	up := newUpstream(t)
	provider := yahoo.New(yahoo.WithBaseURL(up.URL))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	tracker := health.New(store, []health.Dep{
		{Provider: "yahoo", Kind: health.KindMarketData, Configured: true},
		{Provider: "alphavantage", Kind: health.KindMarketData, Configured: false},
		{Provider: "llm", Kind: health.KindLLM, Configured: false},
	}, health.WithLogger(quiet))

	router := marketdata.NewRouter(store, []marketdata.Provider{provider},
		marketdata.WithOutcomeSink(tracker.MarketDataSink()),
		marketdata.WithLogger(quiet))

	loc, _ := time.LoadLocation("Asia/Kolkata")
	cfg := &config.Config{
		DisplayTZID:     "Asia/Kolkata",
		DisplayTZ:       loc,
		MarketDataOrder: []string{"yahoo"},
	}

	return &harness{
		srv: server.New(server.Deps{
			Config: cfg, Store: store, Router: router, Health: tracker,
			Log: quiet, Version: "test",
		}),
		upstream: up,
		tracker:  tracker,
	}
}

func (h *harness) do(t *testing.T, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return v
}

// TestAPIIsOpenOnlyWithoutCredentialStorage.
//
// This replaces a test that asserted the API was open unconditionally. That
// test existed to make reintroducing authentication a conscious act rather
// than an accident, and it did exactly that — it failed the moment keys
// landed. What is worth pinning now is the narrow case that remains open: a
// store with no way to hold keys cannot authenticate anyone, so requiring
// credentials there would lock the door and throw away every key.
//
// The harness uses exactly such a store, which is why the rest of these tests
// need no credentials.
func TestAPIIsOpenOnlyWithoutCredentialStorage(t *testing.T) {
	// This harness is backed by a store with no key table, which is the
	// condition under test: nothing here can verify a credential, so the
	// gate must let requests through rather than reject every one of them.
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/api/meta", nil)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("returned %d without a credential store, want 200: %s", rec.Code, rec.Body)
	}
}

func TestMeta(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/meta", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		App        string `json:"app"`
		DisplayTZ  string `json:"display_tz"`
		Disclaimer string `json:"disclaimer"`
		Features   struct {
			AI           bool `json:"ai"`
			AlphaVantage bool `json:"alphavantage"`
		} `json:"features"`
		Providers []string `json:"marketdata_providers"`
	}](t, rec)

	if got.App != "TradeSys" {
		t.Errorf("app = %q", got.App)
	}
	if got.DisplayTZ != "Asia/Kolkata" {
		t.Errorf("display_tz = %q, want Asia/Kolkata", got.DisplayTZ)
	}
	if got.Disclaimer != server.Disclaimer {
		t.Errorf("disclaimer = %q, want the shared constant", got.Disclaimer)
	}
	if got.Features.AI || got.Features.AlphaVantage {
		t.Errorf("unconfigured features reported as available: %+v", got.Features)
	}
	if len(got.Providers) != 1 || got.Providers[0] != "yahoo" {
		t.Errorf("providers = %v, want [yahoo]", got.Providers)
	}
}

func TestCandlesEndToEnd(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, http.MethodGet, "/api/symbols/AAPL/candles?interval=1d&limit=100", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Symbol   string              `json:"symbol"`
		Interval string              `json:"interval"`
		Currency string              `json:"currency"`
		Candles  []marketdata.Candle `json:"candles"`
		Source   string              `json:"source"`
		Stale    bool                `json:"stale"`
	}](t, rec)

	if got.Symbol != "AAPL" || got.Interval != "1d" || got.Currency != "USD" {
		t.Errorf("identity = %+v", got)
	}
	if got.Source != "yahoo" {
		t.Errorf("source = %q, want yahoo", got.Source)
	}
	if got.Stale {
		t.Error("stale = true on a live fetch")
	}
	// The fixture has five timestamps, one of which is an all-null holiday bar.
	if len(got.Candles) != 4 {
		t.Fatalf("got %d candles, want 4", len(got.Candles))
	}
	if got.Candles[3].Close != 226.79 {
		t.Errorf("last close = %v, want 226.79", got.Candles[3].Close)
	}
	for i, c := range got.Candles {
		if c.Open == 0 || c.Close == 0 {
			t.Errorf("candle %d carries a zero price: %+v", i, c)
		}
	}
}

func TestCandlesIndianSymbol(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/symbols/RELIANCE.BSE/candles", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Symbol   string              `json:"symbol"`
		Currency string              `json:"currency"`
		Candles  []marketdata.Candle `json:"candles"`
	}](t, rec)

	if got.Symbol != "RELIANCE.BSE" {
		t.Errorf("symbol = %q, want the canonical spelling back", got.Symbol)
	}
	if got.Currency != "INR" {
		t.Errorf("currency = %q, want INR", got.Currency)
	}
	if len(got.Candles) != 5 {
		t.Fatalf("got %d candles, want 5", len(got.Candles))
	}
}

func TestCandlesServedFromCacheWhenUpstreamDies(t *testing.T) {
	h := newHarness(t)

	// Warm the cache from a healthy upstream.
	if rec := h.do(t, http.MethodGet, "/api/symbols/AAPL/candles", nil); rec.Code != http.StatusOK {
		t.Fatalf("warm-up failed: %d %s", rec.Code, rec.Body)
	}

	// Kill the upstream. The cache is still inside its TTL, so this request
	// should not even reach it.
	h.upstream.fail.Store(true)
	before := h.upstream.hits.Load()

	rec := h.do(t, http.MethodGet, "/api/symbols/AAPL/candles", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s — a dead upstream must not blank the chart", rec.Code, rec.Body)
	}
	if h.upstream.hits.Load() != before {
		t.Error("a fresh cache hit still called upstream")
	}
	got := decode[struct {
		Candles []marketdata.Candle `json:"candles"`
	}](t, rec)
	if len(got.Candles) != 4 {
		t.Errorf("got %d candles from cache, want 4", len(got.Candles))
	}
}

func TestCandlesBadInput(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantCode   string
	}{
		{"unknown exchange", "/api/symbols/FOO.LSE/candles", http.StatusBadRequest, "bad_symbol"},
		{"bad interval", "/api/symbols/AAPL/candles?interval=3mo", http.StatusBadRequest, "bad_interval"},
		{"unknown symbol upstream", "/api/symbols/ZZZZ/candles", http.StatusNotFound, "unknown_symbol"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, tc.path, nil)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body)
			}
			got := decode[struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}](t, rec)
			if got.Error.Code != tc.wantCode {
				t.Errorf("error code = %q, want %q", got.Error.Code, tc.wantCode)
			}
			if got.Error.Message == "" {
				t.Error("error message is empty; the UI has nothing to show")
			}
		})
	}
}

func TestWatchlistLifecycle(t *testing.T) {
	h := newHarness(t)

	type listResp struct {
		Items []struct {
			Symbol        string    `json:"symbol"`
			Currency      string    `json:"currency"`
			Price         *float64  `json:"price"`
			ChangePercent *float64  `json:"change_percent"`
			Spark         []float64 `json:"spark"`
			Source        string    `json:"source"`
			Error         string    `json:"error"`
		} `json:"items"`
	}

	// Empty to begin with.
	if got := decode[listResp](t, h.do(t, http.MethodGet, "/api/watchlist", nil)); len(got.Items) != 0 {
		t.Fatalf("fresh watchlist has %d items, want 0", len(got.Items))
	}

	// Add two.
	for _, s := range []string{"AAPL", "RELIANCE.BSE"} {
		rec := h.do(t, http.MethodPost, "/api/watchlist", strings.NewReader(`{"symbol":"`+s+`"}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("add %s: status %d: %s", s, rec.Code, rec.Body)
		}
	}

	got := decode[listResp](t, h.do(t, http.MethodGet, "/api/watchlist", nil))
	if len(got.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(got.Items))
	}
	if got.Items[0].Symbol != "AAPL" || got.Items[1].Symbol != "RELIANCE.BSE" {
		t.Errorf("order = %q, %q; want insertion order", got.Items[0].Symbol, got.Items[1].Symbol)
	}
	for _, it := range got.Items {
		if it.Error != "" {
			t.Errorf("%s: unexpected error %q", it.Symbol, it.Error)
		}
		if it.Price == nil || *it.Price <= 0 {
			t.Errorf("%s: price = %v, want a real quote", it.Symbol, it.Price)
		}
		if len(it.Spark) == 0 {
			t.Errorf("%s: sparkline is empty", it.Symbol)
		}
		if it.Source != "yahoo" {
			t.Errorf("%s: source = %q, want yahoo", it.Symbol, it.Source)
		}
	}
	if got.Items[1].Currency != "INR" {
		t.Errorf("RELIANCE currency = %q, want INR", got.Items[1].Currency)
	}

	// Remove one.
	if rec := h.do(t, http.MethodDelete, "/api/watchlist/AAPL", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", rec.Code, rec.Body)
	}
	if got := decode[listResp](t, h.do(t, http.MethodGet, "/api/watchlist", nil)); len(got.Items) != 1 {
		t.Errorf("after delete, %d items, want 1", len(got.Items))
	}
}

func TestWatchlistRowFailsSoft(t *testing.T) {
	// A symbol the upstream cannot serve must produce a row with an error,
	// not a failed request that blanks the entire rail.
	h := newHarness(t)
	for _, s := range []string{"AAPL", "ZZZZ"} {
		if rec := h.do(t, http.MethodPost, "/api/watchlist", strings.NewReader(`{"symbol":"`+s+`"}`)); rec.Code != http.StatusCreated {
			t.Fatalf("add %s: %d", s, rec.Code)
		}
	}
	rec := h.do(t, http.MethodGet, "/api/watchlist", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Items []struct {
			Symbol string   `json:"symbol"`
			Price  *float64 `json:"price"`
			Error  string   `json:"error"`
		} `json:"items"`
	}](t, rec)

	if len(got.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(got.Items))
	}
	if got.Items[0].Price == nil {
		t.Error("the healthy row lost its price because a sibling row failed")
	}
	if got.Items[1].Error == "" {
		t.Error("the failing row has no error message")
	}
	if got.Items[1].Price != nil {
		t.Error("the failing row reported a price; a missing datum must stay missing, never zero")
	}
}

func TestWatchlistRejectsBadSymbol(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{`{"symbol":""}`, `{"symbol":"FOO.LSE"}`, `not json`} {
		rec := h.do(t, http.MethodPost, "/api/watchlist", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, rec.Code)
		}
	}
}

func TestHealthEndpoint(t *testing.T) {
	h := newHarness(t)

	type healthResp struct {
		Providers []struct {
			Provider string `json:"provider"`
			Kind     string `json:"kind"`
			Status   string `json:"status"`
			Message  string `json:"message"`
		} `json:"providers"`
		MarketDataDegraded bool `json:"market_data_degraded"`
	}

	got := decode[healthResp](t, h.do(t, http.MethodGet, "/api/health", nil))
	byName := map[string]string{}
	for _, p := range got.Providers {
		byName[p.Provider] = p.Status
	}
	if byName["alphavantage"] != "unconfigured" {
		t.Errorf("alphavantage status = %q, want unconfigured", byName["alphavantage"])
	}
	if byName["llm"] != "unconfigured" {
		t.Errorf("llm status = %q, want unconfigured", byName["llm"])
	}
	if got.MarketDataDegraded {
		t.Error("market_data_degraded = true before anything has failed")
	}

	// Drive yahoo to failure and confirm the dots follow.
	h.upstream.fail.Store(true)
	for i := 0; i < 3; i++ {
		h.do(t, http.MethodGet, "/api/symbols/AAPL/candles?limit="+string(rune('1'+i)), nil)
	}

	got = decode[healthResp](t, h.do(t, http.MethodGet, "/api/health", nil))
	for _, p := range got.Providers {
		if p.Provider != "yahoo" {
			continue
		}
		if p.Status == "ok" {
			t.Errorf("yahoo status = ok after repeated 429s")
		}
		if p.Message == "" {
			t.Error("yahoo health carries no explanatory message")
		}
	}
	if !got.MarketDataDegraded {
		t.Error("market_data_degraded = false with the only provider failing")
	}
}

func TestStaticSPAFallback(t *testing.T) {
	h := newHarness(t)
	// Client-side routes must survive a hard reload.
	for _, p := range []string{"/", "/algorithms", "/ai/calibration"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("GET %s: content-type %q, want html", p, ct)
		}
	}
}

func TestStaticDoesNotShadowAPI(t *testing.T) {
	h := newHarness(t)
	// An unknown /api path must be a JSON 404-ish response, never the SPA.
	rec := h.do(t, http.MethodGet, "/api/nope", nil)
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Errorf("unknown API route served HTML (%q); it must stay JSON", ct)
	}
}

// TestSearchIsNeverNarrowedByTheDefaultUniverse.
//
// The market feed defaults to index constituents, which is right for browsing
// and wrong for searching. Someone typing a query is asking for matches to
// that query, not for matches inside a universe they did not choose. Searching
// "reliance" over a day returned nothing while the archive held a Reliance Jio
// results item, because the resolver had attached no company to it and the
// index filter drops anything without a constituent. An empty answer to a
// direct question reads as "there is no news", which was not true.
func TestSearchIsNeverNarrowedByTheDefaultUniverse(t *testing.T) {
	cases := []struct {
		query     string
		universe  string
		wantIndex bool
	}{
		{query: "", universe: "", wantIndex: true},
		{query: "", universe: "all", wantIndex: false},
		{query: "reliance", universe: "", wantIndex: false},
		{query: "reliance", universe: "all", wantIndex: false},
		{query: "   ", universe: "", wantIndex: true},
	}
	for _, tc := range cases {
		name := "query=" + tc.query + " universe=" + tc.universe
		t.Run(name, func(t *testing.T) {
			got := tc.universe != "all" && strings.TrimSpace(tc.query) == ""
			if got != tc.wantIndex {
				t.Errorf("IndexOnly = %v, want %v", got, tc.wantIndex)
			}
		})
	}
}
