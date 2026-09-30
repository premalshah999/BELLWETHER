package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/storage"
)

// testDB gives the test a migrated schema of its own, so tests run in
// parallel and a failure leaves its rows behind for inspection.
func testDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run Postgres integration tests")
	}
	db, drop, err := OpenScratch(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open scratch schema: %v", err)
	}
	t.Cleanup(drop)
	return db
}

func rawItem(source, hash, title string, discovered time.Time) news.RawItem {
	return news.RawItem{
		SourceID: source, ContentHash: hash, Title: title,
		URL: "https://example.com/" + hash, CanonicalURL: "https://example.com/" + hash,
		Publisher: "Example", PublishedAt: discovered.Add(-2 * time.Minute),
		DiscoveredAt: discovered, FetchedAt: discovered,
	}
}

func TestSaveRawItemsCountsOnlyNewRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	items := []news.RawItem{
		rawItem("src-a", "h1", "First", now),
		rawItem("src-a", "h2", "Second", now),
	}
	added, err := db.SaveRawItems(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}

	// Re-saving the same items plus one new one must count only the new one.
	again := append(items, rawItem("src-a", "h3", "Third", now))
	added, err = db.SaveRawItems(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d on re-save, want 1", added)
	}
	if n, _ := db.CountRawItems(ctx); n != 3 {
		t.Errorf("held %d items, want 3", n)
	}
}

// TestRawItemTimestampsRoundTrip is the three-timestamp discipline as a test.
func TestRawItemTimestampsRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	published := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	discovered := time.Date(2026, 8, 25, 10, 2, 30, 0, time.UTC)

	it := rawItem("src-a", "h1", "Story", discovered)
	it.PublishedAt = published
	if _, err := db.SaveRawItems(ctx, []news.RawItem{it}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListPendingRawItems(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d items", len(got))
	}
	if !got[0].PublishedAt.Equal(published) {
		t.Errorf("PublishedAt = %v, want %v", got[0].PublishedAt, published)
	}
	if !got[0].DiscoveredAt.Equal(discovered) {
		t.Errorf("DiscoveredAt = %v, want %v", got[0].DiscoveredAt, discovered)
	}
	if lat, ok := got[0].Latency(); !ok || lat != 150*time.Second {
		t.Errorf("Latency = %v (%v), want 2m30s", lat, ok)
	}
}

// TestUndatedItemStaysNull guards against an undated row sorting as the
// oldest thing in the database.
func TestUndatedItemStaysNull(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	it := rawItem("src-a", "undated", "No timestamp", now)
	it.PublishedAt = time.Time{}
	if _, err := db.SaveRawItems(ctx, []news.RawItem{it}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListPendingRawItems(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].PublishedAt.IsZero() {
		t.Errorf("PublishedAt = %v, want the zero time rather than the epoch", got[0].PublishedAt)
	}
}

// TestSchemaRejectsSubstancelessItem checks that the constraint is real.
func TestSchemaRejectsSubstancelessItem(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	// SaveRawItems filters these itself; this reaches past it to prove the
	// database would refuse the row even if the filter were removed.
	_, err := db.db.ExecContext(ctx, `
INSERT INTO raw_items (source_id, content_hash, discovered_at, fetched_at)
VALUES ('s', 'h', now(), now())`)
	if err == nil {
		t.Fatal("expected the has-substance constraint to reject an empty item")
	}
}

// TestFullTextSearch covers the index that replaced a LIKE scan.
func TestFullTextSearch(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i, headline := range []string{
		"Fluor wins metro contract worth $4.2 billion",
		"Intel raises full-year revenue guidance",
		"Exxon declares quarterly dividend of $0.99 per share",
	} {
		imp := 6
		if _, _, err := db.UpsertEvent(ctx, news.Event{
			Fingerprint:  fmt.Sprintf("fp-%d", i),
			Type:         "UNCLASSIFIED",
			Headline:     headline,
			DiscoveredAt: now,
			UpdatedAt:    now,
			Importance:   &imp,
		}, events.TitleKey(headline)); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		query string
		want  int
	}{
		{"dividend", 1},
		{"contract", 1},
		{"guidance", 1},
		// Stemming: "wins" and "win" reach the same lexeme.
		{"win", 1},
		// A phrase query must not match the words scattered apart.
		{`"metro contract"`, 1},
		{`"dividend metro"`, 0},
		{"nonexistent", 0},
	}
	for _, tc := range cases {
		got, err := db.ListEvents(ctx, storage.EventFilter{Query: tc.query, Limit: 50, IncludeUnattributedWatchlist: true})
		if err != nil {
			t.Fatalf("query %q: %v", tc.query, err)
		}
		if len(got) != tc.want {
			t.Errorf("query %q returned %d, want %d", tc.query, len(got), tc.want)
		}
	}

	// Malformed input must not error. This comes straight from a search box.
	for _, bad := range []string{`"unclosed`, `AND OR`, `!!!`, `\`} {
		if _, err := db.ListEvents(ctx, storage.EventFilter{Query: bad, IncludeUnattributedWatchlist: true}); err != nil {
			t.Errorf("query %q errored: %v", bad, err)
		}
	}
}

// TestConsumeBudgetIsAtomic is the reason the budget check became one
// statement: several fetchers race for the last Alpha Vantage request.
func TestConsumeBudgetIsAtomic(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	const limit = 25

	results := make(chan bool, 60)
	for i := 0; i < 60; i++ {
		go func() {
			ok, _, err := db.ConsumeBudget(ctx, "alphavantage", "2026-08-25", limit)
			if err != nil {
				results <- false
				return
			}
			results <- ok
		}()
	}
	granted := 0
	for i := 0; i < 60; i++ {
		if <-results {
			granted++
		}
	}
	if granted != limit {
		t.Errorf("granted %d requests against a limit of %d — the budget is not atomic", granted, limit)
	}
	used, err := db.BudgetUsage(ctx, "alphavantage", "2026-08-25")
	if err != nil {
		t.Fatal(err)
	}
	if used != limit {
		t.Errorf("recorded usage = %d, want %d", used, limit)
	}
}

// TestCandleConstraintRejectsIncoherentBar checks the schema catches what the
// application previously had to trust providers about.
func TestCandleConstraintRejectsIncoherentBar(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	sym, _ := marketdata.ParseSymbol("XOM")

	err := db.SaveCandles(ctx, sym, marketdata.Interval("1d"), "test", marketdata.Bars{
		Candles: []marketdata.Candle{
			{Time: time.Now().UTC(), Open: 10, High: 5, Low: 20, Close: 10, Volume: 1},
		},
	}, 100)
	if err == nil {
		t.Fatal("expected a bar whose high is below its low to be rejected")
	}
}

func TestEventEnumRejectsInvalidDirection(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	id, _, err := db.UpsertEvent(ctx, news.Event{
		Fingerprint: "fp-enum", Type: "ORDER_WIN", Headline: "Something",
		DiscoveredAt: now, UpdatedAt: now,
	}, "k")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.db.ExecContext(ctx, `
INSERT INTO event_entities (event_id, symbol, direction) VALUES ($1, 'LT', 'bullish')`, id)
	if err == nil {
		t.Fatal("expected the direction enum to reject 'bullish'")
	}
}

func TestUpsertEventIsIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	e := news.Event{
		Fingerprint: "fp-same", Type: "ORDER_WIN", Headline: "One event",
		DiscoveredAt: now, UpdatedAt: now,
	}
	id1, created1, err := db.UpsertEvent(ctx, e, "k")
	if err != nil {
		t.Fatal(err)
	}
	id2, created2, err := db.UpsertEvent(ctx, e, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !created1 {
		t.Error("first upsert should report created")
	}
	if created2 {
		t.Error("second upsert must not report created")
	}
	if id1 != id2 {
		t.Errorf("ids differ: %d vs %d — the fingerprint did not converge", id1, id2)
	}
	if n, _ := db.CountEvents(ctx); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}
