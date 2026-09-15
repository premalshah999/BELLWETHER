package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/storage"
)

// noopGoMigration satisfies the "0019_venue_qualify.go" entry that
// migrations/go_migrations.manifest requires of every caller of Open. The
// real migration lives in cmd/tradesys (it needs the embedded company
// masters, which this package must not import — see postgres.go's
// WithGoMigration doc), so integration tests here register a stand-in that
// only needs to prove the mechanism itself: ordering, ledger entry, and the
// hard failure when a manifest entry is never registered are covered by
// TestGoMigration below, against a bare connection rather than testDB's
// schema. Tests that need real venue-qualified fixture data build their own
// symbols directly in the canonical form (e.g. "RELIANCE.NSE"), which is
// what every writer produces once the real migration has run in production.
func noopGoMigration(context.Context, *sql.Tx) error { return nil }

// goMigrationNameForTests must match migrations/go_migrations.manifest
// exactly -- that file, not this constant, is the authority on what Open
// requires.
const goMigrationNameForTests = "0019_venue_qualify.go"

// testDB connects to a real Postgres and gives the test its own schema.
//
// Each test runs in an isolated schema rather than a shared one, so tests can
// run in parallel and a failure leaves its data behind for inspection without
// affecting anything else. TEST_DATABASE_URL keeps this out of the default
// build: a unit test suite that needs a database is one that stops running.
func testDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run Postgres integration tests")
	}
	ctx := context.Background()

	admin, err := Open(ctx, dsn, WithGoMigration(goMigrationNameForTests, noopGoMigration))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	schema := fmt.Sprintf("test_%d_%d", time.Now().UnixNano(), os.Getpid())
	if _, err := admin.db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})

	sep := "?"
	if containsRune(dsn, '?') {
		sep = "&"
	}
	scoped, err := Open(ctx, dsn+sep+"search_path="+schema, WithGoMigration(goMigrationNameForTests, noopGoMigration))
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(func() { scoped.Close() })
	return scoped
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
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
	got, err := db.ListRawItemsBySource(ctx, "src-a", 10)
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
	if !got[0].KnowledgeTime().Equal(discovered) {
		t.Error("knowledge time must be discovery, never publication")
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
	got, err := db.ListRawItems(ctx, 10)
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
		"Larsen & Toubro wins metro contract worth 4200 crore",
		"Infosys raises full-year revenue guidance",
		"Reliance declares interim dividend of Rs 10 per share",
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
	sym, _ := marketdata.ParseSymbol("RELIANCE.NSE")

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

// TestGoMigrationRequiredAndAppliedOnce covers the mechanism WithGoMigration
// adds to the migration runner: a manifested Go migration that nobody
// registered fails Open outright (rather than silently starting with a
// half-migrated database), and one that is registered runs exactly once,
// ledgered exactly like an embedded .sql file.
func TestGoMigrationRequiredAndAppliedOnce(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run Postgres integration tests")
	}
	ctx := context.Background()

	admin, err := Open(ctx, dsn, WithGoMigration(goMigrationNameForTests, noopGoMigration))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	schema := fmt.Sprintf("test_gomig_%d_%d", time.Now().UnixNano(), os.Getpid())
	if _, err := admin.db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})

	sep := "?"
	if containsRune(dsn, '?') {
		sep = "&"
	}
	scopedDSN := dsn + sep + "search_path=" + schema

	// Opening a fresh schema without registering the manifested migration
	// must fail outright, before applying anything.
	if _, err := Open(ctx, scopedDSN); err == nil {
		t.Fatal("expected Open to fail when a manifested Go migration is not registered")
	} else if !strings.Contains(err.Error(), goMigrationNameForTests) {
		t.Errorf("error = %v, want it to name the missing migration %q", err, goMigrationNameForTests)
	}

	var calls int
	counting := func(ctx context.Context, tx *sql.Tx) error {
		calls++
		_, err := tx.ExecContext(ctx, `CREATE TABLE go_migration_marker (id INT)`)
		return err
	}

	db, err := Open(ctx, scopedDSN, WithGoMigration(goMigrationNameForTests, counting))
	if err != nil {
		t.Fatalf("open with go migration registered: %v", err)
	}
	defer db.Close()
	if calls != 1 {
		t.Fatalf("go migration ran %d times on first apply, want 1", calls)
	}

	var applied bool
	if err := db.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, goMigrationNameForTests,
	).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Error("go migration was not recorded in schema_migrations")
	}
	var markerExists bool
	if err := db.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'go_migration_marker')`,
	).Scan(&markerExists); err != nil {
		t.Fatal(err)
	}
	if !markerExists {
		t.Error("go migration's own DDL was not applied")
	}

	// Re-opening the same, already-migrated schema must not re-run it.
	db2, err := Open(ctx, scopedDSN, WithGoMigration(goMigrationNameForTests, counting))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if calls != 1 {
		t.Errorf("go migration re-ran on reopen: calls = %d, want 1", calls)
	}
}
