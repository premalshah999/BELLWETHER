package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	// A file in the test's temp dir exercises the same code path as
	// production, including WAL and the migration runner.
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	for i := 0; i < 3; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		db.Close()
	}
}

func TestCandleRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sym := marketdata.MustParseSymbol("RELIANCE.BSE")

	base := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	in := []marketdata.Candle{
		{Time: base, Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 100},
		{Time: base.Add(24 * time.Hour), Open: 1.5, High: 2.5, Low: 1.4, Close: 2.4, Volume: 200},
		{Time: base.Add(48 * time.Hour), Open: 2.4, High: 3, Low: 2.2, Close: 2.9, Volume: 300},
	}
	if err := db.SaveCandles(ctx, sym, marketdata.Interval1d, "yahoo", marketdata.Bars{Candles: in}, len(in)); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := db.LoadCandles(ctx, sym, marketdata.Interval1d, 100)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Candles) != 3 {
		t.Fatalf("got %d candles, want 3", len(got.Candles))
	}
	if got.Source != "yahoo" {
		t.Errorf("source = %q, want yahoo", got.Source)
	}
	if got.FetchedAt.IsZero() {
		t.Error("fetchedAt is zero")
	}
	for i := range in {
		if !got.Candles[i].Time.Equal(in[i].Time) {
			t.Errorf("candle %d time = %v, want %v", i, got.Candles[i].Time, in[i].Time)
		}
		if got.Candles[i].Close != in[i].Close {
			t.Errorf("candle %d close = %v, want %v", i, got.Candles[i].Close, in[i].Close)
		}
	}
}

func TestSaveCandlesUpsertsInProgressBar(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sym := marketdata.MustParseSymbol("AAPL")
	at := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)

	if err := db.SaveCandles(ctx, sym, marketdata.Interval1d, "yahoo",
		marketdata.Bars{Candles: []marketdata.Candle{{Time: at, Open: 1, High: 2, Low: 1, Close: 1.5, Volume: 10}}}, 1); err != nil {
		t.Fatal(err)
	}
	// The current session's bar keeps moving until the close; re-saving must
	// overwrite it rather than create a second row for the same timestamp.
	if err := db.SaveCandles(ctx, sym, marketdata.Interval1d, "alphavantage",
		marketdata.Bars{Candles: []marketdata.Candle{{Time: at, Open: 1, High: 3, Low: 1, Close: 2.8, Volume: 99}}}, 1); err != nil {
		t.Fatal(err)
	}

	got, err := db.LoadCandles(ctx, sym, marketdata.Interval1d, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candles) != 1 {
		t.Fatalf("got %d candles, want 1", len(got.Candles))
	}
	if got.Candles[0].Close != 2.8 || got.Candles[0].Volume != 99 {
		t.Errorf("candle = %+v, want the updated values", got.Candles[0])
	}
	if got.Source != "alphavantage" {
		t.Errorf("source = %q, want alphavantage", got.Source)
	}
}

func TestLoadCandlesLimitTakesNewest(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sym := marketdata.MustParseSymbol("AAPL")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var in []marketdata.Candle
	for i := 0; i < 50; i++ {
		in = append(in, marketdata.Candle{
			Time: base.Add(time.Duration(i) * 24 * time.Hour),
			Open: 1, High: 1, Low: 1, Close: float64(i), Volume: 1,
		})
	}
	if err := db.SaveCandles(ctx, sym, marketdata.Interval1d, "yahoo", marketdata.Bars{Candles: in}, len(in)); err != nil {
		t.Fatal(err)
	}

	got, err := db.LoadCandles(ctx, sym, marketdata.Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candles) != 10 {
		t.Fatalf("got %d candles, want 10", len(got.Candles))
	}
	// The window must be the 10 most recent, still ordered oldest-first.
	if got.Candles[0].Close != 40 || got.Candles[9].Close != 49 {
		t.Errorf("window = [%v..%v], want [40..49]", got.Candles[0].Close, got.Candles[9].Close)
	}
}

func TestLoadCandlesEmpty(t *testing.T) {
	db := newTestDB(t)
	got, err := db.LoadCandles(context.Background(), marketdata.MustParseSymbol("NOSUCH"), marketdata.Interval1d, 10)
	if err != nil {
		t.Fatalf("a cache miss must not be an error: %v", err)
	}
	if len(got.Candles) != 0 || !got.FetchedAt.IsZero() {
		t.Errorf("got %+v, want a zero result", got)
	}
}

func TestQuoteRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sym := marketdata.MustParseSymbol("AAPL")

	q := marketdata.Quote{
		Symbol: sym, Price: 226.79, PrevClose: 220.15, Change: 6.64,
		ChangePercent: 3.0161, DayHigh: 227.5, DayLow: 224.33,
		Volume: 41258300, Currency: "USD",
		AsOf: time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC),
	}
	if err := db.SaveQuote(ctx, "yahoo", q); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadQuote(ctx, sym)
	if err != nil {
		t.Fatal(err)
	}
	if got.Quote.Price != q.Price || got.Quote.PrevClose != q.PrevClose {
		t.Errorf("quote = %+v, want %+v", got.Quote, q)
	}
	if !got.Quote.AsOf.Equal(q.AsOf) {
		t.Errorf("asOf = %v, want %v", got.Quote.AsOf, q.AsOf)
	}
	if got.Quote.Symbol != sym {
		t.Errorf("symbol = %v, want %v", got.Quote.Symbol, sym)
	}
	if got.Source != "yahoo" {
		t.Errorf("source = %q, want yahoo", got.Source)
	}
}

func TestQuoteMissIsNotAnError(t *testing.T) {
	db := newTestDB(t)
	got, err := db.LoadQuote(context.Background(), marketdata.MustParseSymbol("NOSUCH"))
	if err != nil {
		t.Fatalf("a cache miss must not be an error: %v", err)
	}
	if !got.FetchedAt.IsZero() {
		t.Errorf("got %+v, want a zero result", got)
	}
}

func TestConsumeBudget(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		calls      int
		wantAllows int
	}{
		{name: "under the limit", limit: 5, calls: 3, wantAllows: 3},
		{name: "exactly the limit", limit: 5, calls: 5, wantAllows: 5},
		{name: "over the limit", limit: 5, calls: 9, wantAllows: 5},
		{name: "zero limit allows nothing", limit: 0, calls: 3, wantAllows: 0},
		{name: "negative limit allows nothing", limit: -1, calls: 3, wantAllows: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			var allowed int
			for i := 0; i < tc.calls; i++ {
				ok, _, err := db.ConsumeBudget(ctx, "av", "2026-08-24", tc.limit)
				if err != nil {
					t.Fatal(err)
				}
				if ok {
					allowed++
				}
			}
			if allowed != tc.wantAllows {
				t.Errorf("allowed = %d, want %d", allowed, tc.wantAllows)
			}
			used, err := db.BudgetUsage(ctx, "av", "2026-08-24")
			if err != nil {
				t.Fatal(err)
			}
			if used != tc.wantAllows {
				t.Errorf("recorded usage = %d, want %d", used, tc.wantAllows)
			}
		})
	}
}

func TestConsumeBudgetIsAtomicUnderConcurrency(t *testing.T) {
	// The read-then-write must be one transaction, or two goroutines racing
	// for the last request both slip through and blow the daily quota.
	db := newTestDB(t)
	ctx := context.Background()
	const limit = 10
	const goroutines = 40

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := db.ConsumeBudget(ctx, "av", "2026-08-24", limit)
			if err != nil {
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed > limit {
		t.Errorf("allowed = %d, want at most %d — the counter overran its limit", allowed, limit)
	}
	used, err := db.BudgetUsage(ctx, "av", "2026-08-24")
	if err != nil {
		t.Fatal(err)
	}
	if used != allowed {
		t.Errorf("recorded usage %d disagrees with %d granted permits", used, allowed)
	}
}

func TestBudgetPeriodsAreIndependent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		db.ConsumeBudget(ctx, "av", "2026-08-24", 3)
	}
	ok, _, err := db.ConsumeBudget(ctx, "av", "2026-08-24", 3)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("day 1 should be exhausted")
	}
	ok, remaining, err := db.ConsumeBudget(ctx, "av", "2026-08-25", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("day 2 should have a fresh allowance")
	}
	if remaining != 2 {
		t.Errorf("remaining = %d, want 2", remaining)
	}
}

func TestWatchlist(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, s := range []string{"RELIANCE.BSE", "AAPL", "TCS.BSE"} {
		if err := db.AddWatchlist(ctx, marketdata.MustParseSymbol(s), ""); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListWatchlist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	// Insertion order is the operator's order.
	want := []string{"RELIANCE.BSE", "AAPL", "TCS.BSE"}
	for i, w := range want {
		if got[i].Symbol.String() != w {
			t.Errorf("entry %d = %s, want %s", i, got[i].Symbol, w)
		}
	}

	t.Run("re-adding updates the note without duplicating", func(t *testing.T) {
		if err := db.AddWatchlist(ctx, marketdata.MustParseSymbol("AAPL"), "core holding"); err != nil {
			t.Fatal(err)
		}
		got, err := db.ListWatchlist(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d entries, want 3", len(got))
		}
		if got[1].Note != "core holding" {
			t.Errorf("note = %q, want %q", got[1].Note, "core holding")
		}
	})

	t.Run("remove", func(t *testing.T) {
		if err := db.RemoveWatchlist(ctx, marketdata.MustParseSymbol("AAPL")); err != nil {
			t.Fatal(err)
		}
		got, _ := db.ListWatchlist(ctx)
		if len(got) != 2 {
			t.Errorf("got %d entries, want 2", len(got))
		}
	})

	t.Run("removing an absent symbol is not an error", func(t *testing.T) {
		if err := db.RemoveWatchlist(ctx, marketdata.MustParseSymbol("NOSUCH")); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestHealthPreservesLastKnownTimes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	okAt := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	if err := db.RecordHealth(ctx, storage.ProviderHealth{
		Provider: "yahoo", Kind: "marketdata", Status: storage.HealthOK, LastOKAt: &okAt,
	}); err != nil {
		t.Fatal(err)
	}
	// A subsequent failure must not erase when the provider last worked.
	errAt := okAt.Add(time.Hour)
	if err := db.RecordHealth(ctx, storage.ProviderHealth{
		Provider: "yahoo", Kind: "marketdata", Status: storage.HealthDegraded,
		Message: "rate limited", LastErrorAt: &errAt,
	}); err != nil {
		t.Fatal(err)
	}

	list, err := db.ListHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d records, want 1", len(list))
	}
	h := list[0]
	if h.Status != storage.HealthDegraded {
		t.Errorf("status = %q, want degraded", h.Status)
	}
	if h.Message != "rate limited" {
		t.Errorf("message = %q, want %q", h.Message, "rate limited")
	}
	if h.LastOKAt == nil || !h.LastOKAt.Equal(okAt) {
		t.Errorf("lastOKAt = %v, want it preserved as %v", h.LastOKAt, okAt)
	}
	if h.LastErrorAt == nil || !h.LastErrorAt.Equal(errAt) {
		t.Errorf("lastErrorAt = %v, want %v", h.LastErrorAt, errAt)
	}
}

func TestResolvedSymbolSurvivesTheCache(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sym := marketdata.MustParseSymbol("RELIANCE.BSE")
	at := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)

	err := db.SaveCandles(ctx, sym, marketdata.Interval1d, "yahoo", marketdata.Bars{
		Candles:        []marketdata.Candle{{Time: at, Open: 1, High: 2, Low: 1, Close: 1.5, Volume: 10}},
		ResolvedSymbol: "RELIANCE.NSE",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadCandles(ctx, sym, marketdata.Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResolvedSymbol != "RELIANCE.NSE" {
		t.Errorf("ResolvedSymbol = %q, want it round-tripped", got.ResolvedSymbol)
	}
}

func TestSeriesCoverageKeepsHighWaterMark(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sym := marketdata.MustParseSymbol("AAPL")
	at := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	bar := marketdata.Bars{Candles: []marketdata.Candle{
		{Time: at, Open: 1, High: 2, Low: 1, Close: 1.5, Volume: 10},
	}}

	if err := db.SaveCandles(ctx, sym, marketdata.Interval1d, "yahoo", bar, 400); err != nil {
		t.Fatal(err)
	}
	// A later shallow fetch must not lower what we know we can serve.
	if err := db.SaveCandles(ctx, sym, marketdata.Interval1d, "yahoo", bar, 30); err != nil {
		t.Fatal(err)
	}

	got, err := db.LoadCandles(ctx, sym, marketdata.Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestedLimit != 400 {
		t.Errorf("RequestedLimit = %d, want 400 (high-water mark)", got.RequestedLimit)
	}
}
