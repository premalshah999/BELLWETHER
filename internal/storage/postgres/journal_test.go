package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// TestTradesWithCatalystsPicksClosestPrecedingEvent is the regression for
// the whole point of the query: attribution must pick the most recent
// event discovered on or before the entry date, must never attribute a
// trade to something discovered after it was entered (that trade could not
// have been prompted by knowledge that did not exist yet), and must not
// reach further back than the lookback window even when an older event
// exists.
func TestTradesWithCatalystsPicksClosestPrecedingEvent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	t.Cleanup(func() {
		_, _ = db.db.ExecContext(ctx, `DELETE FROM trades WHERE symbol LIKE 'JRNL%'`)
		_, _ = db.db.ExecContext(ctx, `DELETE FROM events WHERE fingerprint LIKE 'journal-test-%'`)
	})

	mkEvent := func(fingerprint, symbol string, discoveredAt time.Time, official bool) {
		id, _, err := db.UpsertEvent(ctx, news.Event{
			Fingerprint: fingerprint, Type: "ORDER_WIN", Headline: "headline " + fingerprint,
			DiscoveredAt: discoveredAt, UpdatedAt: discoveredAt, SourceCount: 1, Official: official,
		}, fingerprint)
		if err != nil {
			t.Fatalf("upsert event %s: %v", fingerprint, err)
		}
		if err := db.UpsertEventEntities(ctx, id, []news.EventEntity{{Symbol: symbol, Relationship: news.RelPrimary}}); err != nil {
			t.Fatalf("attach entity: %v", err)
		}
	}

	opened := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	// Case 1 (JRNLA): two candidate events before entry -- the closer one
	// (2 days before) must win over the further one (4 days before).
	mkEvent("journal-test-a-far", "JRNLA", opened.AddDate(0, 0, -4), false)
	mkEvent("journal-test-a-close", "JRNLA", opened.AddDate(0, 0, -2), true)
	// A same-symbol event discovered AFTER entry must never be picked --
	// this trade could not have been prompted by knowledge from the future.
	mkEvent("journal-test-a-future", "JRNLA", opened.AddDate(0, 0, 1), true)

	// Case 2 (JRNLB): only an event outside the lookback window (10 days
	// before, window is 5) -- must attribute to nothing, not the stale event.
	mkEvent("journal-test-b-stale", "JRNLB", opened.AddDate(0, 0, -10), true)

	// Case 3 (JRNLC): no event at all.

	var tradeIDs []int64
	for _, symbol := range []string{"JRNLA", "JRNLB", "JRNLC"} {
		var id int64
		err := db.db.QueryRowContext(ctx, `
			INSERT INTO trades (symbol, quantity, entry_price, exit_price, opened_at, closed_at, realized_pnl, account, notes)
			VALUES ($1, 10, 100, 110, $2, $3, 100, 'journal-test', '')
			RETURNING id`,
			symbol, opened, opened.AddDate(0, 0, 3)).Scan(&id)
		if err != nil {
			t.Fatalf("insert trade %s: %v", symbol, err)
		}
		tradeIDs = append(tradeIDs, id)
	}
	t.Cleanup(func() {
		for _, id := range tradeIDs {
			_, _ = db.db.ExecContext(ctx, `DELETE FROM trades WHERE id = $1`, id)
		}
	})

	all, err := db.TradesWithCatalysts(ctx, 500)
	if err != nil {
		t.Fatalf("trades with catalysts: %v", err)
	}
	byID := map[int64]TradeCatalyst{}
	for _, tc := range all {
		byID[tc.ID] = tc
	}

	a, ok := byID[tradeIDs[0]]
	if !ok {
		t.Fatal("missing JRNLA trade")
	}
	if a.EventID == nil {
		t.Fatal("JRNLA should have a catalyst")
	}
	if a.Headline != "headline journal-test-a-close" {
		t.Errorf("JRNLA catalyst = %q, want the closer event (2 days before), not the farther or future one", a.Headline)
	}
	if !a.Official {
		t.Error("JRNLA's chosen catalyst should be the official one")
	}
	if a.DaysBeforeEntry == nil || *a.DaysBeforeEntry != 2 {
		t.Errorf("JRNLA days before entry = %v, want 2", a.DaysBeforeEntry)
	}

	b, ok := byID[tradeIDs[1]]
	if !ok {
		t.Fatal("missing JRNLB trade")
	}
	if b.EventID != nil {
		t.Errorf("JRNLB should have no catalyst (only event is outside the %dd window), got %q", catalystLookbackDays, b.Headline)
	}

	c, ok := byID[tradeIDs[2]]
	if !ok {
		t.Fatal("missing JRNLC trade")
	}
	if c.EventID != nil {
		t.Errorf("JRNLC should have no catalyst at all, got %q", c.Headline)
	}
}
