package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// TestSourceLatencyLeaderboardRanksByWinsNotVolume is the regression for the
// whole point of this query: a source's rank comes from how often it beat
// every other source to a story both carried, not from how much it
// publishes overall -- a prolific source that is reliably second should not
// outrank a source that is reliably first.
func TestSourceLatencyLeaderboardRanksByWinsNotVolume(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	t.Cleanup(func() {
		_, _ = db.db.ExecContext(ctx, `DELETE FROM events WHERE fingerprint LIKE 'latency-test-%'`)
		_, _ = db.db.ExecContext(ctx, `DELETE FROM raw_items WHERE source_id IN ('latency-x-src', 'latency-y-src')`)
	})

	base := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	mkEvent := func(fingerprint string, discoveredAt time.Time) int64 {
		id, _, err := db.UpsertEvent(ctx, news.Event{
			Fingerprint: fingerprint, Type: "ORDER_WIN", Headline: "test event " + fingerprint,
			DiscoveredAt: discoveredAt, UpdatedAt: discoveredAt, SourceCount: 2,
		}, fingerprint)
		if err != nil {
			t.Fatalf("upsert event %s: %v", fingerprint, err)
		}
		return id
	}
	seq := 0
	newRawItem := func(sourceID string) int64 {
		seq++
		var id int64
		// content_hash need only be unique per source_id, so a per-call
		// sequence number is enough to keep each row distinct.
		row := db.db.QueryRowContext(ctx, `
			INSERT INTO raw_items (source_id, content_hash, url, title, discovered_at, fetched_at)
			VALUES ($1, $2, $3, 'test', $4, $4)
			RETURNING id`,
			sourceID, fmt.Sprintf("latency-test-hash-%d", seq), fmt.Sprintf("https://example.com/latency-test/%d", seq), base)
		if err := row.Scan(&id); err != nil {
			t.Fatalf("insert raw item: %v", err)
		}
		return id
	}
	attach := func(eventID int64, sourceID string, rawItemID int64, at time.Time) {
		if err := db.AttachEvidence(ctx, eventID, rawItemID, sourceID, 80, at); err != nil {
			t.Fatalf("attach evidence: %v", err)
		}
	}

	// Event A: latency-x-src wins by 60s.
	a := mkEvent("latency-test-a", base)
	attach(a, "latency-x-src", newRawItem("latency-x-src"), base)
	attach(a, "latency-y-src", newRawItem("latency-y-src"), base.Add(60*time.Second))

	// Event B: latency-x-src wins by 30s.
	b := mkEvent("latency-test-b", base.Add(time.Hour))
	attach(b, "latency-x-src", newRawItem("latency-x-src"), base.Add(time.Hour))
	attach(b, "latency-y-src", newRawItem("latency-y-src"), base.Add(time.Hour+30*time.Second))

	// Event C: latency-y-src wins by 90s.
	c := mkEvent("latency-test-c", base.Add(2*time.Hour))
	attach(c, "latency-y-src", newRawItem("latency-y-src"), base.Add(2*time.Hour))
	attach(c, "latency-x-src", newRawItem("latency-x-src"), base.Add(2*time.Hour+90*time.Second))

	board, err := db.SourceLatencyLeaderboard(ctx, base.Add(-time.Hour), 1)
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}

	byID := map[string]SourceLatency{}
	for _, l := range board {
		byID[l.SourceID] = l
	}
	x, ok := byID["latency-x-src"]
	if !ok {
		t.Fatal("missing latency-x-src")
	}
	y, ok := byID["latency-y-src"]
	if !ok {
		t.Fatal("missing latency-y-src")
	}

	if x.Participated != 3 || x.TimesFirst != 2 {
		t.Errorf("x: participated=%d timesFirst=%d, want 3/2", x.Participated, x.TimesFirst)
	}
	if y.Participated != 3 || y.TimesFirst != 1 {
		t.Errorf("y: participated=%d timesFirst=%d, want 3/1", y.Participated, y.TimesFirst)
	}
	if x.AvgLeadSeconds == nil || *x.AvgLeadSeconds < 44 || *x.AvgLeadSeconds > 46 {
		t.Errorf("x avg lead = %v, want ~45s ((60+30)/2)", x.AvgLeadSeconds)
	}
	if y.AvgLeadSeconds == nil || *y.AvgLeadSeconds < 89 || *y.AvgLeadSeconds > 91 {
		t.Errorf("y avg lead = %v, want ~90s", y.AvgLeadSeconds)
	}

	// x won more often, so it must rank first despite equal participation.
	if board[0].SourceID != "latency-x-src" {
		t.Errorf("board[0] = %s, want latency-x-src to rank first on wins", board[0].SourceID)
	}

	// The since filter must actually exclude events before it.
	empty, err := db.SourceLatencyLeaderboard(ctx, base.Add(3*time.Hour), 1)
	if err != nil {
		t.Fatalf("leaderboard (future since): %v", err)
	}
	for _, l := range empty {
		if l.SourceID == "latency-x-src" || l.SourceID == "latency-y-src" {
			t.Errorf("since=base+3h should exclude these test events, got %+v", l)
		}
	}
}

