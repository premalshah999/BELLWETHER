package postgres

import (
	"context"
	"fmt"
	"time"
)

// Earnings is one past earnings announcement.
type Earnings struct {
	Symbol      string
	AnnouncedAt time.Time
	EPSEstimate *float64
	EPSActual   float64
	SurprisePct *float64
}

// SaveEarnings upserts announcements; a restated figure replaces the old one.
func (d *DB) SaveEarnings(ctx context.Context, rows []Earnings) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("save earnings: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO earnings_history (symbol, announced_at, eps_estimate, eps_actual, surprise_pct)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (symbol, announced_at) DO UPDATE
		SET eps_estimate = EXCLUDED.eps_estimate, eps_actual = EXCLUDED.eps_actual,
		    surprise_pct = EXCLUDED.surprise_pct, fetched_at = now()`)
	if err != nil {
		return 0, fmt.Errorf("save earnings: prepare: %w", err)
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Symbol, r.AnnouncedAt, r.EPSEstimate, r.EPSActual, r.SurprisePct); err != nil {
			return 0, fmt.Errorf("save earnings %s: %w", r.Symbol, err)
		}
	}
	return len(rows), tx.Commit()
}

// EarningsHistory returns every stored announcement with a known surprise,
// oldest first.
func (d *DB) EarningsHistory(ctx context.Context) ([]Earnings, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT symbol, announced_at, eps_estimate, eps_actual, surprise_pct
		FROM earnings_history WHERE surprise_pct IS NOT NULL
		ORDER BY announced_at`)
	if err != nil {
		return nil, fmt.Errorf("earnings history: %w", err)
	}
	defer rows.Close()
	var out []Earnings
	for rows.Next() {
		var e Earnings
		if err := rows.Scan(&e.Symbol, &e.AnnouncedAt, &e.EPSEstimate, &e.EPSActual, &e.SurprisePct); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SymbolEarnings returns one symbol's announcements, newest first.
func (d *DB) SymbolEarnings(ctx context.Context, symbol string, limit int) ([]Earnings, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT symbol, announced_at, eps_estimate, eps_actual, surprise_pct
		FROM earnings_history WHERE symbol = $1
		ORDER BY announced_at DESC LIMIT $2`, symbol, limit)
	if err != nil {
		return nil, fmt.Errorf("symbol earnings: %w", err)
	}
	defer rows.Close()
	var out []Earnings
	for rows.Next() {
		var e Earnings
		if err := rows.Scan(&e.Symbol, &e.AnnouncedAt, &e.EPSEstimate, &e.EPSActual, &e.SurprisePct); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EarningsCount is how many announcements with a known surprise are stored.
func (d *DB) EarningsCount(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM earnings_history WHERE surprise_pct IS NOT NULL`).Scan(&n)
	return n, err
}
