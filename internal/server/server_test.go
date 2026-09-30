package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/auth"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/health"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/marketdata/yfin"
	"github.com/tradesys/dashboard/internal/server"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// aaplDaily is a sidecar reply: five sessions, one of them a zero-priced
// holiday row the client must drop.
const aaplDaily = `{"symbol":"AAPL","interval":"1d","currency":"USD","candles":[
{"t":"2025-08-14T13:30:00Z","o":221.05,"h":223.88,"l":220.4,"c":223.12,"v":38221500},
{"t":"2025-08-15T13:30:00Z","o":223.4,"h":225.71,"l":222.86,"c":224.55,"v":35110200},
{"t":"2025-08-18T13:30:00Z","o":0,"h":0,"l":0,"c":0,"v":0},
{"t":"2025-08-19T13:30:00Z","o":224.9,"h":226.4,"l":223.75,"c":225.11,"v":29884100},
{"t":"2025-08-21T13:30:00Z","o":225.6,"h":227.5,"l":224.33,"c":226.79,"v":41258300}]}`

// sidecar stands in for the yfinance service, so the whole chain -- HTTP,
// parsing, the router, the cache, the API -- runs with no network.
type sidecar struct {
	*httptest.Server
	hits atomic.Int64
	fail atomic.Bool
}

func newSidecar(t *testing.T) *sidecar {
	t.Helper()
	u := &sidecar{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case u.fail.Load():
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":"YFRateLimitError: Too Many Requests"}`)
		case r.URL.Query().Get("symbol") == "AAPL" || r.URL.Query().Get("symbol") == "MSFT":
			io.WriteString(w, aaplDaily)
		default:
			io.WriteString(w, `{"symbol":"`+r.URL.Query().Get("symbol")+`","interval":"1d","candles":[]}`)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// testStore is a migrated scratch schema in TEST_DATABASE_URL's database.
func testStore(t *testing.T) *postgres.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run database-backed server tests")
	}
	db, drop, err := postgres.OpenScratch(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open scratch schema: %v", err)
	}
	t.Cleanup(drop)
	return db
}

type harness struct {
	srv      *server.Server
	upstream *sidecar
	store    *postgres.DB
}

// newHarness serves the API over a fresh database with no keys issued and
// the development opt-in set, so requests need no credentials.
func newHarness(t *testing.T) *harness {
	t.Helper()
	store := testStore(t)
	up := newSidecar(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	tracker := health.New(store, []health.Dep{
		{Provider: yfin.ProviderName, Kind: health.KindMarketData, Configured: true},
		{Provider: "alphavantage", Kind: health.KindMarketData, Configured: false},
		{Provider: "llm", Kind: health.KindLLM, Configured: false},
	}, health.WithLogger(quiet))

	router := marketdata.NewRouter(store, []marketdata.Provider{yfin.New(up.URL)},
		marketdata.WithOutcomeSink(tracker.MarketDataSink()),
		marketdata.WithLogger(quiet))

	loc, _ := time.LoadLocation("America/New_York")
	cfg := &config.Config{
		DisplayTZID:          "America/New_York",
		DisplayTZ:            loc,
		MarketDataOrder:      []string{"yfinance"},
		AllowUnauthenticated: true,
	}
	return &harness{
		srv: server.New(server.Deps{
			Config: cfg, Store: store, Router: router, Health: tracker,
			Log: quiet, Version: "test",
		}),
		upstream: up,
		store:    store,
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
	if got.DisplayTZ != "America/New_York" {
		t.Errorf("display_tz = %q, want America/New_York", got.DisplayTZ)
	}
	if got.Disclaimer != server.Disclaimer {
		t.Errorf("disclaimer = %q, want the shared constant", got.Disclaimer)
	}
	if got.Features.AI || got.Features.AlphaVantage {
		t.Errorf("unconfigured features reported as available: %+v", got.Features)
	}
	if len(got.Providers) != 1 || got.Providers[0] != "yfinance" {
		t.Errorf("providers = %v, want [yfinance]", got.Providers)
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
	if got.Source != "yfinance" {
		t.Errorf("source = %q, want yfinance", got.Source)
	}
	if got.Stale {
		t.Error("stale = true on a live fetch")
	}
	// Five rows, one of them the zero-priced holiday the client drops.
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
	if got := decode[listResp](t, h.do(t, http.MethodGet, "/api/watchlists/1/symbols", nil)); len(got.Items) != 0 {
		t.Fatalf("fresh watchlist has %d items, want 0", len(got.Items))
	}

	// Add two.
	for _, s := range []string{"AAPL", "MSFT"} {
		rec := h.do(t, http.MethodPost, "/api/watchlists/1/symbols", strings.NewReader(`{"symbol":"`+s+`"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("add %s: status %d: %s", s, rec.Code, rec.Body)
		}
	}

	got := decode[listResp](t, h.do(t, http.MethodGet, "/api/watchlists/1/symbols", nil))
	if len(got.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(got.Items))
	}
	if got.Items[0].Symbol != "AAPL" || got.Items[1].Symbol != "MSFT" {
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
		if it.Source != "yfinance" {
			t.Errorf("%s: source = %q, want yfinance", it.Symbol, it.Source)
		}
		if it.Currency != "USD" {
			t.Errorf("%s: currency = %q, want USD", it.Symbol, it.Currency)
		}
	}

	// Remove one.
	if rec := h.do(t, http.MethodDelete, "/api/watchlists/1/symbols/AAPL", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", rec.Code, rec.Body)
	}
	if got := decode[listResp](t, h.do(t, http.MethodGet, "/api/watchlists/1/symbols", nil)); len(got.Items) != 1 {
		t.Errorf("after delete, %d items, want 1", len(got.Items))
	}
}

func TestWatchlistRowFailsSoft(t *testing.T) {
	// A symbol the upstream cannot serve must produce a row with an error,
	// not a failed request that blanks the entire rail.
	h := newHarness(t)
	for _, s := range []string{"AAPL", "ZZZZ"} {
		if rec := h.do(t, http.MethodPost, "/api/watchlists/1/symbols", strings.NewReader(`{"symbol":"`+s+`"}`)); rec.Code != http.StatusOK {
			t.Fatalf("add %s: %d", s, rec.Code)
		}
	}
	rec := h.do(t, http.MethodGet, "/api/watchlists/1/symbols", nil)
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
	for _, body := range []string{`{"symbol":""}`, `not json`} {
		if rec := h.do(t, http.MethodPost, "/api/watchlists/1/symbols", strings.NewReader(body)); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, rec.Code)
		}
	}
	// A symbol that is not a ticker is reported back, not added.
	got := decode[struct {
		Added    []string `json:"added"`
		Rejected []string `json:"rejected"`
	}](t, h.do(t, http.MethodPost, "/api/watchlists/1/symbols", strings.NewReader(`{"symbols":["AAPL","FOO.LSE"]}`)))
	if len(got.Added) != 1 || len(got.Rejected) != 1 || got.Rejected[0] != "FOO.LSE" {
		t.Errorf("added %v rejected %v, want AAPL added and FOO.LSE rejected", got.Added, got.Rejected)
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

	// Drive the sidecar to failure and confirm the dots follow.
	h.upstream.fail.Store(true)
	for i := 0; i < 3; i++ {
		h.do(t, http.MethodGet, "/api/symbols/AAPL/candles?limit="+string(rune('1'+i)), nil)
	}

	got = decode[healthResp](t, h.do(t, http.MethodGet, "/api/health", nil))
	for _, p := range got.Providers {
		if p.Provider != yfin.ProviderName {
			continue
		}
		if p.Status == "ok" {
			t.Errorf("yfinance status = ok after repeated failures")
		}
		if p.Message == "" {
			t.Error("yfinance health carries no explanatory message")
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

// Once a key exists, every route but sign-in, its status and the health check
// refuses a caller without one, and a viewer key cannot write. Walks the real
// routing table, so a route added tomorrow is covered today.
func TestEveryRouteRequiresAKey(t *testing.T) {
	h := newHarness(t)
	issue := func(role auth.Role) string {
		key, err := auth.Generate()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.IssueKey(context.Background(), key, string(role), role, ""); err != nil {
			t.Fatal(err)
		}
		return key.Secret
	}
	viewer := issue(auth.RoleViewer)

	open := map[string]bool{"/api/auth/login": true, "/api/auth/status": true, "/api/health": true}
	readOnlyPosts := map[string]bool{"/api/screens/run": true, "/api/algorithms/preview": true, "/api/algorithms/backtest": true}
	fill := strings.NewReplacer("{symbol}", "AAPL", "{id}", "1", "{cik}", "0001067983", "{mode}", "signals", "{tab}", "overview", "/*", "/")

	routes := 0
	err := chi.Walk(h.srv.Routes(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		path := strings.TrimSuffix(fill.Replace(route), "/")
		if !strings.HasPrefix(path, "/api/") || open[path] || path == "/api/stream" {
			return nil
		}
		routes++
		if rec := h.do(t, method, path, strings.NewReader("{}")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a key: status %d, want 401", method, route, rec.Code)
		}
		if method == http.MethodGet || readOnlyPosts[path] || path == "/api/auth/logout" {
			return nil
		}
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+viewer)
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with a read-only key: status %d, want 403", method, route, rec.Code)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if routes < 50 {
		t.Fatalf("walked %d protected routes; the routing table was not reached", routes)
	}
}
