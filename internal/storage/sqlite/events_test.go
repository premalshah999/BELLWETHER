package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

func rawItem(source, hash, title string, published, discovered time.Time) news.RawItem {
	return news.RawItem{
		SourceID: source, ContentHash: hash,
		URL: "https://example.com/" + hash, CanonicalURL: "https://example.com/" + hash,
		Title: title, Publisher: "Example",
		PublishedAt: published, DiscoveredAt: discovered, FetchedAt: discovered,
	}
}

func TestSaveRawItemsCountsOnlyNewRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	items := []news.RawItem{
		rawItem("src-a", "h1", "First", now.Add(-time.Hour), now),
		rawItem("src-a", "h2", "Second", now.Add(-time.Hour), now),
	}
	added, err := db.SaveRawItems(ctx, items)
	if err != nil {
		t.Fatalf("SaveRawItems: %v", err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}

	// Re-saving the same items, plus one new one. Only the new one counts:
	// SQLite reports a changed row for an insert and an update alike, so this
	// is the assertion that keeps that ambiguity from creeping back in.
	again := append(items, rawItem("src-a", "h3", "Third", now, now))
	added, err = db.SaveRawItems(ctx, again)
	if err != nil {
		t.Fatalf("SaveRawItems: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d on re-save, want 1", added)
	}

	total, err := db.CountRawItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("total items = %d, want 3", total)
	}
}

// TestRawItemsSameHashDifferentSources checks that uniqueness is scoped per
// source. Two publishers carrying identical text are two pieces of evidence,
// and collapsing them would destroy the corroboration count.
func TestRawItemsSameHashDifferentSources(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	added, err := db.SaveRawItems(ctx, []news.RawItem{
		rawItem("src-a", "same", "Identical headline", now, now),
		rawItem("src-b", "same", "Identical headline", now, now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2 — one per source", added)
	}
}

// TestRawItemMissingPublishedStaysNull is the guard against an undated item
// sorting as the oldest thing in the database.
func TestRawItemMissingPublishedStaysNull(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		rawItem("src-a", "undated", "No timestamp", time.Time{}, now),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListRawItems(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if !got[0].PublishedAt.IsZero() {
		t.Errorf("PublishedAt = %v, want the zero time rather than the epoch", got[0].PublishedAt)
	}
	if got[0].DiscoveredAt.IsZero() {
		t.Error("DiscoveredAt must always survive the round trip")
	}
}

func TestRawItemTimestampsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	published := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	discovered := time.Date(2026, 8, 25, 10, 2, 30, 0, time.UTC)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		rawItem("src-a", "h1", "Story", published, discovered),
	}); err != nil {
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
	// The whole point of keeping them apart: latency is measurable.
	lat, ok := got[0].Latency()
	if !ok || lat != 150*time.Second {
		t.Errorf("Latency = %v (%v), want 2m30s", lat, ok)
	}
	// And knowledge time is discovery, never publication.
	if !got[0].KnowledgeTime().Equal(discovered) {
		t.Errorf("KnowledgeTime = %v, want the discovery time", got[0].KnowledgeTime())
	}
}

func TestSourceHealthRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	h := news.SourceHealth{
		SourceID: "nse-announcements", LastAttemptAt: now, LastSuccessAt: now,
		ConsecutiveFailures: 0, TotalAttempts: 10, TotalSuccesses: 9,
		TotalItems: 400, TotalNewItems: 37, ETag: `"abc"`, LastModified: "Mon, 25 Aug 2026 12:00:00 GMT",
	}
	if err := db.SaveSourceHealth(ctx, h); err != nil {
		t.Fatalf("SaveSourceHealth: %v", err)
	}
	// Saving again must update in place rather than duplicate.
	h.TotalAttempts = 11
	h.ConsecutiveFailures = 2
	h.LastError = "502 Bad Gateway"
	h.LastFailureAt = now.Add(time.Minute)
	if err := db.SaveSourceHealth(ctx, h); err != nil {
		t.Fatal(err)
	}

	all, err := db.LoadSourceHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d health rows, want 1", len(all))
	}
	got := all[0]
	if got.TotalAttempts != 11 || got.ConsecutiveFailures != 2 {
		t.Errorf("health = %+v, want the updated values", got)
	}
	if got.ETag != `"abc"` {
		t.Errorf("ETag = %q, want it preserved across the update", got.ETag)
	}
	if got.Healthy() {
		t.Error("a source with consecutive failures must not report healthy")
	}
	if !got.LastFailureAt.Equal(now.Add(time.Minute)) {
		t.Errorf("LastFailureAt = %v", got.LastFailureAt)
	}
}

func TestPruneRawItems(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		rawItem("src-a", "old", "Old", now.Add(-72*time.Hour), now.Add(-72*time.Hour)),
		rawItem("src-a", "new", "New", now, now),
	}); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.PruneRawItems(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	remaining, _ := db.CountRawItems(ctx)
	if remaining != 1 {
		t.Errorf("remaining = %d, want 1", remaining)
	}
}
