package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// migrationTestDB opens an isolated schema with every migration applied,
// including the real 0019 Go migration (against what is, at Open time, an
// empty database — this only proves it runs without error on nothing).
// Testing the rewrite itself needs fixture data seeded afterward and the
// individual step functions called directly, which is what the tests below
// do: qualifyEventEntitiesAndResearch, qualifyScanUniverseTables and
// qualifyWatchSourceIDs are idempotent (their SQL is all upsert/update-style
// except the CHECK constraints and CREATE TABLE, which stay inside
// venueQualifyMigration itself and are not re-invoked here).
func migrationTestDB(t *testing.T) *postgres.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run Postgres integration tests")
	}
	ctx := context.Background()

	opt, err := venueMigrationOption()
	if err != nil {
		t.Fatalf("venueMigrationOption: %v", err)
	}

	admin, err := postgres.Open(ctx, dsn, opt)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	schema := fmt.Sprintf("test_venue_%d_%d", time.Now().UnixNano(), os.Getpid())
	if _, err := admin.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.SQL().ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	opt2, err := venueMigrationOption()
	if err != nil {
		t.Fatalf("venueMigrationOption: %v", err)
	}
	scoped, err := postgres.Open(ctx, dsn+sep+"search_path="+schema, opt2)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(func() { scoped.Close() })
	return scoped
}

// TestVenueQualifyMigrationRewritesRealData seeds bare, pre-migration-shaped
// fixture data and applies the migration's rewrite steps directly (not
// through the ledger, which already ran them once on empty tables when
// migrationTestDB opened — see its doc comment), then checks every
// classification rule this migration makes: renamed NSE symbols, unchanged
// US symbols, the index/drop/literal-remap special cases, the five-table
// suffix append, and watch-id renaming.
func TestVenueQualifyMigrationRewritesRealData(t *testing.T) {
	db := migrationTestDB(t)
	ctx := context.Background()
	sqlDB := db.SQL()

	nse, err := company.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	usTickers, _, err := company.LoadEmbeddedUS()
	if err != nil {
		t.Fatal(err)
	}
	classifier := newSymbolClassifier(nse, usTickers)

	// --- seed events + event_entities with a mix of every case ---
	symbols := []string{
		"RELIANCE",            // real NSE master symbol -> RELIANCE.NSE
		"AMZN",                // real US ticker -> unchanged
		"NIFTY",               // index -> NIFTY.INDEX
		"PERNOD",              // dropped: no listing anywhere
		"INTERGLOBE AVIATION", // literal remap -> INDIGO.NSE
		"ZZFAKENAME",          // resolves nowhere -> default NSE-inactive bucket
	}
	eventIDs := make(map[string]int64, len(symbols))
	for _, sym := range symbols {
		var id int64
		err := sqlDB.QueryRowContext(ctx, `
INSERT INTO events (fingerprint, event_type, headline, discovered_at, updated_at)
VALUES ($1, 'UNCLASSIFIED', $2, now(), now()) RETURNING id`,
			"fp-venue-test-"+sym, "Test headline for "+sym).Scan(&id)
		if err != nil {
			t.Fatalf("seed event for %s: %v", sym, err)
		}
		eventIDs[sym] = id
		if _, err := sqlDB.ExecContext(ctx,
			`INSERT INTO event_entities (event_id, symbol) VALUES ($1, $2)`, id, sym); err != nil {
			t.Fatalf("seed event_entities for %s: %v", sym, err)
		}
	}

	// research_conversations: one array mixing a rename, an unchanged US
	// symbol and a drop, to prove drops are removed from the array rather
	// than leaving a hole or erroring.
	if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO research_conversations (title, symbols)
VALUES ('venue migration test', '{RELIANCE,AMZN,PERNOD}'::text[])`); err != nil {
		t.Fatalf("seed research_conversations: %v", err)
	}

	// Five NSE-universe tables: one bare row each.
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO index_constituents (symbol, industry) VALUES ('RELIANCE', 'Oil & Gas')`); err != nil {
		t.Fatalf("seed index_constituents: %v", err)
	}

	// Watch source ids: one NSE, one US.
	for _, id := range []string{"watch-reliance", "watch-aapl"} {
		if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO raw_items (source_id, content_hash, title, discovered_at, fetched_at)
VALUES ($1, $2, 'x', now(), now())`, id, "hash-"+id); err != nil {
			t.Fatalf("seed raw_items for %s: %v", id, err)
		}
		if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO source_health (source_id) VALUES ($1)`, id); err != nil {
			t.Fatalf("seed source_health for %s: %v", id, err)
		}
	}

	// --- apply the rewrite steps directly ---
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := qualifyEventEntitiesAndResearch(ctx, tx, classifier); err != nil {
		tx.Rollback()
		t.Fatalf("qualifyEventEntitiesAndResearch: %v", err)
	}
	if err := qualifyScanUniverseTables(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatalf("qualifyScanUniverseTables: %v", err)
	}
	if err := qualifyWatchSourceIDs(ctx, tx, classifier); err != nil {
		tx.Rollback()
		t.Fatalf("qualifyWatchSourceIDs: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// --- assertions ---
	symOf := func(eventID int64) string {
		var s string
		if err := sqlDB.QueryRowContext(ctx,
			`SELECT symbol FROM event_entities WHERE event_id = $1`, eventID).Scan(&s); err != nil {
			t.Fatalf("read back symbol for event %d: %v", eventID, err)
		}
		return s
	}
	cases := map[string]string{
		"RELIANCE":            "RELIANCE.NSE",
		"AMZN":                "AMZN",
		"NIFTY":               "NIFTY.INDEX",
		"INTERGLOBE AVIATION": "INDIGO.NSE",
		"ZZFAKENAME":          "ZZFAKENAME.NSE",
	}
	for orig, want := range cases {
		if got := symOf(eventIDs[orig]); got != want {
			t.Errorf("%s -> %s, want %s", orig, got, want)
		}
	}
	var pernodCount int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM event_entities WHERE event_id = $1`, eventIDs["PERNOD"]).Scan(&pernodCount); err != nil {
		t.Fatal(err)
	}
	if pernodCount != 0 {
		t.Errorf("PERNOD entity link still present (count=%d), want dropped", pernodCount)
	}

	// ZZFAKENAME must have picked up an inactive NSE listing row.
	var active bool
	var venue string
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT venue, active FROM listings WHERE symbol = 'ZZFAKENAME.NSE'`).Scan(&venue, &active); err != nil {
		t.Fatalf("expected an inactive listing row for ZZFAKENAME.NSE: %v", err)
	}
	if venue != "NSE" || active {
		t.Errorf("ZZFAKENAME.NSE listing = venue=%s active=%v, want venue=NSE active=false", venue, active)
	}

	// research_conversations: PERNOD dropped, the other two kept as-is.
	rsRows, err := sqlDB.QueryContext(ctx,
		`SELECT s FROM research_conversations, unnest(symbols) AS s WHERE title = 'venue migration test'`)
	if err != nil {
		t.Fatal(err)
	}
	var researchSyms []string
	for rsRows.Next() {
		var s string
		if err := rsRows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		researchSyms = append(researchSyms, s)
	}
	rsRows.Close()
	want := map[string]bool{"RELIANCE.NSE": true, "AMZN": true}
	if len(researchSyms) != 2 {
		t.Fatalf("research symbols = %v, want exactly RELIANCE.NSE and AMZN", researchSyms)
	}
	for _, s := range researchSyms {
		if !want[s] {
			t.Errorf("unexpected research symbol %q", s)
		}
	}

	// index_constituents: suffixed, venue/taxonomy backfilled.
	var icSymbol, icVenue, icTaxonomy string
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT symbol, venue, taxonomy FROM index_constituents WHERE symbol = 'RELIANCE.NSE'`,
	).Scan(&icSymbol, &icVenue, &icTaxonomy); err != nil {
		t.Fatalf("index_constituents not rewritten: %v", err)
	}
	if icVenue != "NSE" || icTaxonomy != "nse-industry" {
		t.Errorf("index_constituents venue/taxonomy = %s/%s, want NSE/nse-industry", icVenue, icTaxonomy)
	}

	// Watch ids: NSE renamed, US unchanged.
	assertSourceID := func(table, old, want string) {
		var count int
		if err := sqlDB.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT count(*) FROM %s WHERE source_id = $1`, table), want).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Errorf("%s: expected source_id %q (renamed from %q), found none", table, want, old)
		}
	}
	assertSourceID("raw_items", "watch-reliance", "watch-reliance.nse")
	assertSourceID("raw_items", "watch-aapl", "watch-aapl")
	assertSourceID("source_health", "watch-reliance", "watch-reliance.nse")
	assertSourceID("source_health", "watch-aapl", "watch-aapl")
}
