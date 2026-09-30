package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Position is one open holding: how much of something, and what it cost.
//
// CostBasis is per share/unit, average-cost -- not FIFO lot tracking. That
// is a real simplification: closing part of a position built from several
// buys at different prices returns the position's single blended cost, not
// the specific lot a broker's tax accounting might choose. Good enough for
// "what am I exposed to and roughly what did it cost me"; not a substitute
// for a broker's own cost-basis reporting at tax time.
type Position struct {
	ID        int64
	Symbol    string
	Quantity  float64
	CostBasis float64
	OpenedAt  time.Time
	Account   string
	Notes     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Trade is one closed position (or the closed portion of one): what it
// actually returned. Immutable once written -- see the migration's own doc
// for why closing a position appends a trade rather than editing one.
type Trade struct {
	ID          int64     `json:"id"`
	Symbol      string    `json:"symbol"`
	Quantity    float64   `json:"quantity"`
	EntryPrice  float64   `json:"entry_price"`
	ExitPrice   float64   `json:"exit_price"`
	OpenedAt    time.Time `json:"opened_at"`
	ClosedAt    time.Time `json:"closed_at"`
	RealizedPnL float64   `json:"realized_pnl"`
	Account     string    `json:"account,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// SavePosition inserts a new open position.
func (d *DB) SavePosition(ctx context.Context, p Position) (int64, error) {
	var id int64
	err := d.db.QueryRowContext(ctx, `
		INSERT INTO positions (symbol, quantity, cost_basis, opened_at, account, notes)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		p.Symbol, p.Quantity, p.CostBasis, p.OpenedAt, p.Account, p.Notes,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save position: %w", err)
	}
	return id, nil
}

// ListPositions returns every open position, newest first.
func (d *DB) ListPositions(ctx context.Context) ([]Position, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, symbol, quantity, cost_basis, opened_at, account, notes, created_at, updated_at
		FROM positions ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list positions: %w", err)
	}
	defer rows.Close()

	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.Symbol, &p.Quantity, &p.CostBasis,
			&p.OpenedAt, &p.Account, &p.Notes, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePosition removes a position outright, with no trade recorded -- for
// correcting a mis-entered position, not for exiting a real one.
func (d *DB) DeletePosition(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM positions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete position: %w", err)
	}
	return nil
}

// ClosePosition reduces (or fully closes) a position by quantity at
// exitPrice on closedAt, and records the closed portion as a trade at the
// position's own average cost. Quantity greater than the position's own is
// rejected rather than silently clamped -- a fat-fingered close should fail
// loudly, not quietly realize a P&L on shares that were never held.
//
// The whole operation is one transaction: a trade must never exist without
// the position it came from having actually been reduced, and vice versa.
func (d *DB) ClosePosition(ctx context.Context, id int64, quantity, exitPrice float64, closedAt time.Time, notes string) (Trade, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Trade{}, fmt.Errorf("close position: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var p Position
	err = tx.QueryRowContext(ctx, `
		SELECT id, symbol, quantity, cost_basis, opened_at, account
		FROM positions WHERE id = $1 FOR UPDATE`, id,
	).Scan(&p.ID, &p.Symbol, &p.Quantity, &p.CostBasis, &p.OpenedAt, &p.Account)
	if err == sql.ErrNoRows {
		return Trade{}, fmt.Errorf("close position: no position %d", id)
	}
	if err != nil {
		return Trade{}, fmt.Errorf("close position: load: %w", err)
	}
	if quantity <= 0 {
		return Trade{}, fmt.Errorf("close position: quantity must be positive")
	}
	if quantity > p.Quantity+1e-9 {
		return Trade{}, fmt.Errorf("close position: closing %.4f exceeds the %.4f held", quantity, p.Quantity)
	}

	t := Trade{
		Symbol: p.Symbol, Quantity: quantity,
		EntryPrice: p.CostBasis, ExitPrice: exitPrice,
		OpenedAt: p.OpenedAt, ClosedAt: closedAt,
		RealizedPnL: (exitPrice - p.CostBasis) * quantity,
		Account:     p.Account, Notes: notes,
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO trades (symbol, quantity, entry_price, exit_price, opened_at, closed_at, realized_pnl, account, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at`,
		t.Symbol, t.Quantity, t.EntryPrice, t.ExitPrice, t.OpenedAt, t.ClosedAt, t.RealizedPnL, t.Account, t.Notes,
	).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		return Trade{}, fmt.Errorf("close position: record trade: %w", err)
	}

	remaining := p.Quantity - quantity
	if remaining <= 1e-9 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM positions WHERE id = $1`, id); err != nil {
			return Trade{}, fmt.Errorf("close position: remove: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE positions SET quantity = $2, updated_at = now() WHERE id = $1`,
			id, remaining); err != nil {
			return Trade{}, fmt.Errorf("close position: reduce: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Trade{}, fmt.Errorf("close position: commit: %w", err)
	}
	return t, nil
}
