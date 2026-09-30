package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// archivePair opens a primary and an archive against two real databases.
//
// Two databases, not two schemas: the point of the design is that the archive
// is a separate server, and a test sharing one connection would not exercise
// the part that can actually break -- copying rows between two handles rather
// than with one INSERT ... SELECT.
func archivePair(t *testing.T) (*Archive, context.Context) {
	t.Helper()
	coldDSN := os.Getenv("TEST_ARCHIVE_DATABASE_URL")
	if coldDSN == "" {
		t.Skip("set TEST_ARCHIVE_DATABASE_URL to run news archive tests")
	}
	hot := testDB(t)
	ctx := context.Background()

	cold, err := Open(ctx, coldDSN)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	t.Cleanup(func() { cold.Close() })

	// A clean slate on the archive side. The primary is a throwaway schema
	// already; the archive database is shared between runs.
	for _, table := range append([]string{"events", "raw_items"}, eventChildTables...) {
		if _, err := cold.db.ExecContext(ctx, `DELETE FROM `+pq(table)); err != nil {
			t.Fatalf("clear archive %s: %v", table, err)
		}
	}

	return &Archive{hot: hot, cold: cold, window: 30 * 24 * time.Hour, log: hot.log}, ctx
}

// seedEvent writes one event with a full set of dependents, so the test can
// tell whether a rollover moved the whole graph or only part of it.
func seedEvent(t *testing.T, db *DB, ctx context.Context, fingerprint string, age time.Time) int64 {
	t.Helper()
	n, err := db.SaveRawItems(ctx, []news.RawItem{{
		SourceID: "sec-8k", ContentHash: fingerprint + "-hash",
		Title: "seed " + fingerprint, URL: "https://example.com/" + fingerprint,
		PublishedAt: age, DiscoveredAt: age, FetchedAt: age,
	}})
	if err != nil || n != 1 {
		t.Fatalf("SaveRawItems(%s) = %d, %v", fingerprint, n, err)
	}
	var rawID int64
	if err := db.db.QueryRowContext(ctx,
		`SELECT id FROM raw_items WHERE content_hash = $1`, fingerprint+"-hash").Scan(&rawID); err != nil {
		t.Fatalf("find raw item: %v", err)
	}

	id, _, err := db.UpsertEvent(ctx, news.Event{
		Fingerprint: fingerprint, Type: "EARNINGS",
		Headline:    "seed headline " + fingerprint,
		PublishedAt: age, DiscoveredAt: age, UpdatedAt: age, SourceCount: 1,
	}, "")
	if err != nil {
		t.Fatalf("UpsertEvent(%s): %v", fingerprint, err)
	}
	if err := db.AttachEvidence(ctx, id, rawID, "sec-8k", 100, age); err != nil {
		t.Fatalf("AttachEvidence: %v", err)
	}
	if err := db.UpsertEventEntities(ctx, id, []news.EventEntity{
		{Symbol: "AAPL", Relationship: "primary", MatchConfidence: 0.99, MatchMethod: "test"},
	}); err != nil {
		t.Fatalf("UpsertEventEntities: %v", err)
	}
	if err := db.SaveEventFacts(ctx, id, map[string]string{"SEC_ITEMS": "2.02"}); err != nil {
		t.Fatalf("SaveEventFacts: %v", err)
	}
	return id
}

func count(t *testing.T, db *DB, ctx context.Context, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// The whole property the design rests on: an event and everything hanging off
// it are always on the same server. If a rollover left the evidence behind,
// the feed's lateral join would return an event with no link, and the
// IndexOnly filter would stop recognising it.
func TestRolloverMovesTheWholeEvent(t *testing.T) {
	a, ctx := archivePair(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	oldID := seedEvent(t, a.hot, ctx, "arch-old", now.AddDate(0, 0, -90))
	newID := seedEvent(t, a.hot, ctx, "arch-new", now.AddDate(0, 0, -2))

	moved, err := a.Rollover(ctx, now)
	if err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved %d events, want only the aged one", moved)
	}

	// Gone from the primary, with every dependent.
	if n := count(t, a.hot, ctx, `SELECT count(*) FROM events WHERE id = $1`, oldID); n != 0 {
		t.Error("the aged event is still on the primary")
	}
	for _, table := range eventChildTables {
		if n := count(t, a.hot, ctx,
			`SELECT count(*) FROM `+pq(table)+` WHERE event_id = $1`, oldID); n != 0 {
			t.Errorf("%s rows for the aged event were left on the primary", table)
		}
	}

	// Present on the archive, with every dependent.
	if n := count(t, a.cold, ctx, `SELECT count(*) FROM events WHERE id = $1`, oldID); n != 1 {
		t.Fatal("the aged event did not reach the archive")
	}
	for _, tc := range []struct {
		table string
		want  int
	}{
		{"event_evidence", 1},
		{"event_entities", 1},
		{"event_facts", 1},
	} {
		if n := count(t, a.cold, ctx,
			`SELECT count(*) FROM `+pq(tc.table)+` WHERE event_id = $1`, oldID); n != tc.want {
			t.Errorf("archive %s = %d rows, want %d", tc.table, n, tc.want)
		}
	}
	// The raw item its evidence points at has to be there too, or the
	// evidence names a row that does not exist.
	if n := count(t, a.cold, ctx, `
        SELECT count(*) FROM event_evidence ev
        JOIN raw_items r ON r.id = ev.raw_item_id
        WHERE ev.event_id = $1`, oldID); n != 1 {
		t.Error("the archived evidence does not join to a raw item")
	}

	// And the recent event is untouched.
	if n := count(t, a.hot, ctx, `SELECT count(*) FROM events WHERE id = $1`, newID); n != 1 {
		t.Error("a recent event was archived")
	}
	if n := count(t, a.cold, ctx, `SELECT count(*) FROM events WHERE id = $1`, newID); n != 0 {
		t.Error("a recent event reached the archive")
	}
}

// An event can be merged from items discovered weeks apart, so one raw item can
// support both an aged event and a current one. Deleting it with the aged event
// would leave the current event's evidence pointing at nothing.
func TestRolloverKeepsRawItemsACurrentEventStillCites(t *testing.T) {
	a, ctx := archivePair(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	oldID := seedEvent(t, a.hot, ctx, "arch-shared", now.AddDate(0, 0, -90))
	var sharedRaw int64
	if err := a.hot.db.QueryRowContext(ctx,
		`SELECT raw_item_id FROM event_evidence WHERE event_id = $1`, oldID).Scan(&sharedRaw); err != nil {
		t.Fatalf("find shared raw item: %v", err)
	}

	// A second, current event citing the same raw item.
	recentID, _, err := a.hot.UpsertEvent(ctx, news.Event{
		Fingerprint: "arch-current", Type: "EARNINGS", Headline: "current",
		PublishedAt: now.AddDate(0, 0, -1), DiscoveredAt: now.AddDate(0, 0, -1),
		UpdatedAt: now.AddDate(0, 0, -1), SourceCount: 1,
	}, "")
	if err != nil {
		t.Fatalf("UpsertEvent: %v", err)
	}
	if err := a.hot.AttachEvidence(ctx, recentID, sharedRaw, "sec-8k", 100, now); err != nil {
		t.Fatalf("AttachEvidence: %v", err)
	}

	if _, err := a.Rollover(ctx, now); err != nil {
		t.Fatalf("Rollover: %v", err)
	}

	if n := count(t, a.hot, ctx, `SELECT count(*) FROM raw_items WHERE id = $1`, sharedRaw); n != 1 {
		t.Error("a raw item still cited by a current event was deleted from the primary")
	}
	if n := count(t, a.hot, ctx, `
        SELECT count(*) FROM event_evidence ev
        JOIN raw_items r ON r.id = ev.raw_item_id
        WHERE ev.event_id = $1`, recentID); n != 1 {
		t.Error("the current event's evidence no longer joins to its raw item")
	}
}

// An interrupted rollover leaves rows on both sides. The next run must finish
// the job rather than duplicate it, so every insert is ON CONFLICT DO NOTHING
// and the delete only follows a successful copy.
func TestRolloverIsIdempotent(t *testing.T) {
	a, ctx := archivePair(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	seedEvent(t, a.hot, ctx, "arch-idem", now.AddDate(0, 0, -90))

	first, err := a.Rollover(ctx, now)
	if err != nil {
		t.Fatalf("first Rollover: %v", err)
	}
	second, err := a.Rollover(ctx, now)
	if err != nil {
		t.Fatalf("second Rollover: %v", err)
	}
	if first != 1 || second != 0 {
		t.Errorf("moved %d then %d, want 1 then 0", first, second)
	}
	if n := count(t, a.cold, ctx, `SELECT count(*) FROM events WHERE fingerprint = 'arch-idem'`); n != 1 {
		t.Errorf("archive holds %d copies of the event, want 1", n)
	}
}

// A read whose window sits inside the hot period must not pay for the archive
// at all -- that is what makes the default feed cheap.
func TestReadsInsideTheWindowDoNotTouchTheArchive(t *testing.T) {
	a, _ := archivePair(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	if a.SpansArchive(EventFilter{Since: now.Add(-72 * time.Hour)}, now) {
		t.Error("a 72-hour window is inside the 30-day hot period; it must not span the archive")
	}
	if !a.SpansArchive(EventFilter{Since: now.AddDate(0, 0, -90)}, now) {
		t.Error("a 90-day window reaches past the cutoff and must span the archive")
	}
	if !a.SpansArchive(EventFilter{}, now) {
		t.Error("an unbounded window reaches back forever and must span the archive")
	}
}

// A read that does span the cutoff has to return one correctly ordered page
// drawn from both servers.
func TestFanOutMergesBothShardsInOrder(t *testing.T) {
	a, ctx := archivePair(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	// Interleaved in time, so a merge that simply concatenated would fail.
	seedEvent(t, a.hot, ctx, "merge-1", now.AddDate(0, 0, -1))
	seedEvent(t, a.hot, ctx, "merge-3", now.AddDate(0, 0, -60))
	seedEvent(t, a.hot, ctx, "merge-2", now.AddDate(0, 0, -2))
	seedEvent(t, a.hot, ctx, "merge-4", now.AddDate(0, 0, -90))

	if _, err := a.Rollover(ctx, now); err != nil {
		t.Fatalf("Rollover: %v", err)
	}

	got, err := a.ListEvents(ctx, EventFilter{
		Since: now.AddDate(0, 0, -120), Limit: 10,
		OrderByContentAge:            true,
		IncludeUnattributedWatchlist: true,
	}, now)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 4 {
		var seen []string
		for _, e := range got {
			seen = append(seen, e.Fingerprint)
		}
		t.Fatalf("got %d events %v, want all four across both shards", len(got), seen)
	}
	for i := 1; i < len(got); i++ {
		if contentAge(got[i]).After(contentAge(got[i-1])) {
			t.Errorf("merged page is not in descending content-age order at %d: %s before %s",
				i, got[i-1].Fingerprint, got[i].Fingerprint)
		}
	}
	if got[0].Fingerprint != "merge-1" || got[3].Fingerprint != "merge-4" {
		t.Errorf("order = %s..%s, want merge-1 first and merge-4 last",
			got[0].Fingerprint, got[3].Fingerprint)
	}
}

// The archive is the less reliable of the two by construction: it is a free
// tier that can be asleep or rate-limited. A page it cannot serve should
// degrade to what the primary holds, which is everything recent, rather than
// failing the whole request.
func TestArchiveFailureDegradesToThePrimary(t *testing.T) {
	a, ctx := archivePair(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	seedEvent(t, a.hot, ctx, "degrade-hot", now.AddDate(0, 0, -1))

	// Close the archive out from under it, which is what an unreachable
	// endpoint looks like to the caller.
	if err := a.cold.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	got, err := a.ListEvents(ctx, EventFilter{
		Since: now.AddDate(0, 0, -120), Limit: 10,
		IncludeUnattributedWatchlist: true,
	}, now)
	if err != nil {
		t.Fatalf("a failing archive must not fail the read: %v", err)
	}
	if len(got) != 1 || got[0].Fingerprint != "degrade-hot" {
		t.Errorf("got %d events, want the one the primary holds", len(got))
	}
}
