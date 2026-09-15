package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
)

// TestEventTypeSymbolPairsAndCounts is the regression for the event study
// engine's raw population query: given events of a known type with entities
// attached, EventTypeSymbolPairs must return exactly the (symbol,
// discovered_at) pairs that type carries -- discovered_at, not occurred_at
// or published_at, because that is the anchor internal/eventstudy's whole
// method depends on -- and EventTypeCounts must total them correctly
// against a different type it must not leak into.
func TestEventTypeSymbolPairsAndCounts(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	discovered := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	e1ID, _, err := db.UpsertEvent(ctx, news.Event{
		Fingerprint: "eventstudy-test-1", Type: "EARNINGS", Headline: "AAPL earnings",
		DiscoveredAt: discovered, UpdatedAt: discovered,
	}, "eventstudy-test-1")
	if err != nil {
		t.Fatalf("upsert event 1: %v", err)
	}
	if err := db.UpsertEventEntities(ctx, e1ID, []news.EventEntity{
		{Symbol: "AAPL", Relationship: news.RelPrimary},
	}); err != nil {
		t.Fatalf("attach entity 1: %v", err)
	}

	discovered2 := discovered.Add(48 * time.Hour)
	e2ID, _, err := db.UpsertEvent(ctx, news.Event{
		Fingerprint: "eventstudy-test-2", Type: "EARNINGS", Headline: "RELIANCE earnings",
		DiscoveredAt: discovered2, UpdatedAt: discovered2,
	}, "eventstudy-test-2")
	if err != nil {
		t.Fatalf("upsert event 2: %v", err)
	}
	if err := db.UpsertEventEntities(ctx, e2ID, []news.EventEntity{
		{Symbol: "RELIANCE.NSE", Relationship: news.RelPrimary},
	}); err != nil {
		t.Fatalf("attach entity 2: %v", err)
	}

	// A different type, which must not leak into the EARNINGS query.
	otherAt := discovered.Add(time.Hour)
	e3ID, _, err := db.UpsertEvent(ctx, news.Event{
		Fingerprint: "eventstudy-test-3", Type: "GUIDANCE", Headline: "AAPL guidance cut",
		DiscoveredAt: otherAt, UpdatedAt: otherAt,
	}, "eventstudy-test-3")
	if err != nil {
		t.Fatalf("upsert event 3: %v", err)
	}
	if err := db.UpsertEventEntities(ctx, e3ID, []news.EventEntity{
		{Symbol: "AAPL", Relationship: news.RelPrimary},
	}); err != nil {
		t.Fatalf("attach entity 3: %v", err)
	}

	pairs, err := db.EventTypeSymbolPairs(ctx, "EARNINGS")
	if err != nil {
		t.Fatalf("EventTypeSymbolPairs: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs, want 2 (GUIDANCE must not leak in); got %+v", len(pairs), pairs)
	}
	bySymbol := map[string]time.Time{}
	for _, p := range pairs {
		bySymbol[p.Symbol] = p.DiscoveredAt
	}
	if got := bySymbol["AAPL"]; !got.Equal(discovered) {
		t.Errorf("AAPL discovered_at = %v, want %v", got, discovered)
	}
	if got := bySymbol["RELIANCE.NSE"]; !got.Equal(discovered2) {
		t.Errorf("RELIANCE.NSE discovered_at = %v, want %v", got, discovered2)
	}

	counts, err := db.EventTypeCounts(ctx)
	if err != nil {
		t.Fatalf("EventTypeCounts: %v", err)
	}
	byType := map[string]int{}
	for _, c := range counts {
		byType[c.EventType] = c.Count
	}
	if byType["EARNINGS"] < 2 {
		t.Errorf("EARNINGS count = %d, want at least 2", byType["EARNINGS"])
	}
	if byType["GUIDANCE"] < 1 {
		t.Errorf("GUIDANCE count = %d, want at least 1", byType["GUIDANCE"])
	}
}

// TestScanSavedCandlesFeedEventStudy is a thin end-to-end smoke test that
// the scanner's series-persistence path (Runner.persistSeries ->
// SaveCandles) and the event study's own read path (LoadCandles) agree on
// the same symbol and interval -- the seam between Phase 5's two halves.
func TestScanSavedCandlesFeedEventStudy(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	sym, err := marketdata.ParseSymbol("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	bars := marketdata.Bars{Candles: []marketdata.Candle{
		{Time: time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), Open: 100, High: 101, Low: 99, Close: 100, Volume: 1000},
		{Time: time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), Open: 100, High: 106, Low: 100, Close: 105, Volume: 2000},
	}}
	if err := db.SaveCandles(ctx, sym, marketdata.Interval1d, "yfinance-scan", bars, len(bars.Candles)); err != nil {
		t.Fatalf("SaveCandles: %v", err)
	}

	series, err := db.LoadCandles(ctx, sym, marketdata.Interval1d, 400)
	if err != nil {
		t.Fatalf("LoadCandles: %v", err)
	}
	if len(series.Candles) != 2 {
		t.Fatalf("got %d candles, want 2", len(series.Candles))
	}
	if series.Candles[0].Close != 100 || series.Candles[1].Close != 105 {
		t.Errorf("candles = %+v, want closes 100 then 105 oldest-first", series.Candles)
	}
}
