package postgres

import (
	"context"
	"testing"
	"time"
)

// TestClosePositionPartialReducesAndRecordsTrade is the regression for the
// core invariant of the whole positions/trades split: closing part of a
// position must leave the remainder open at the same cost basis, record
// exactly the closed quantity as a trade at that cost basis, and compute
// realized P&L from it -- not from the exit price alone.
func TestClosePositionPartialReducesAndRecordsTrade(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	opened := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	id, err := db.SavePosition(ctx, Position{
		Symbol: "AAPL", Quantity: 100, CostBasis: 150, OpenedAt: opened, Account: "taxable",
	})
	if err != nil {
		t.Fatalf("save position: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.db.ExecContext(ctx, `DELETE FROM positions WHERE id = $1`, id)
		_, _ = db.db.ExecContext(ctx, `DELETE FROM trades WHERE symbol = 'AAPL' AND account = 'taxable'`)
	})

	closed := opened.AddDate(0, 1, 0)
	trade, err := db.ClosePosition(ctx, id, 40, 180, closed, "took some profit")
	if err != nil {
		t.Fatalf("close position: %v", err)
	}
	if trade.Quantity != 40 || trade.EntryPrice != 150 || trade.ExitPrice != 180 {
		t.Errorf("trade = %+v, want qty=40 entry=150 exit=180", trade)
	}
	wantPnL := (180.0 - 150.0) * 40.0
	if trade.RealizedPnL != wantPnL {
		t.Errorf("RealizedPnL = %v, want %v", trade.RealizedPnL, wantPnL)
	}

	pos, ok, err := positionByID(ctx, db, id)
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if !ok {
		t.Fatal("position should still exist after a partial close")
	}
	if pos.Quantity != 60 {
		t.Errorf("remaining quantity = %v, want 60", pos.Quantity)
	}
	if pos.CostBasis != 150 {
		t.Errorf("remaining cost basis = %v, want unchanged at 150", pos.CostBasis)
	}

	// Closing the rest must remove the position entirely.
	if _, err := db.ClosePosition(ctx, id, 60, 190, closed, ""); err != nil {
		t.Fatalf("close remainder: %v", err)
	}
	_, ok, err = positionByID(ctx, db, id)
	if err != nil {
		t.Fatalf("get position after full close: %v", err)
	}
	if ok {
		t.Error("position should be gone after closing the remaining quantity")
	}
}

// TestClosePositionRejectsOverClose guards against a fat-fingered close
// silently realizing a P&L on shares that were never held.
func TestClosePositionRejectsOverClose(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	id, err := db.SavePosition(ctx, Position{
		Symbol: "MSFT", Quantity: 10, CostBasis: 300, OpenedAt: time.Now().UTC(), Account: "test",
	})
	if err != nil {
		t.Fatalf("save position: %v", err)
	}
	t.Cleanup(func() { _, _ = db.db.ExecContext(ctx, `DELETE FROM positions WHERE id = $1`, id) })

	if _, err := db.ClosePosition(ctx, id, 11, 310, time.Now().UTC(), ""); err == nil {
		t.Error("want an error closing more than the position holds")
	}

	// The position must be untouched after a rejected close.
	pos, ok, err := positionByID(ctx, db, id)
	if err != nil || !ok {
		t.Fatalf("get position: ok=%v err=%v", ok, err)
	}
	if pos.Quantity != 10 {
		t.Errorf("quantity = %v after a rejected close, want unchanged 10", pos.Quantity)
	}
}

// positionByID reads one position back through the list the app uses.
func positionByID(ctx context.Context, db *DB, id int64) (Position, bool, error) {
	all, err := db.ListPositions(ctx)
	for _, p := range all {
		if p.ID == id {
			return p, true, err
		}
	}
	return Position{}, false, err
}
