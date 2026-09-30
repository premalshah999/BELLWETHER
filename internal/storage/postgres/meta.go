package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

// ListWatchlist returns watchlist entries in operator-defined order.
//
// Rows whose symbol no longer parses are skipped rather than failing the whole
// read, so one bad row can never blank the left rail.
func (d *DB) ListWatchlist(ctx context.Context) ([]storage.WatchlistEntry, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT symbol, note, position, added_at FROM watchlist ORDER BY position ASC, added_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list watchlist: %w", err)
	}
	defer rows.Close()

	var out []storage.WatchlistEntry
	for rows.Next() {
		var (
			raw string
			e   storage.WatchlistEntry
		)
		if err := rows.Scan(&raw, &e.Note, &e.Position, &e.AddedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan watchlist: %w", err)
		}
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			continue
		}
		e.Symbol = sym
		e.AddedAt = e.AddedAt.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddWatchlist appends a symbol, or updates the note if it is already present.
func (d *DB) AddWatchlist(ctx context.Context, sym marketdata.Symbol, note string) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO watchlist (symbol, note, position, added_at)
VALUES ($1, $2, COALESCE((SELECT MAX(position) + 1 FROM watchlist), 0), $3)
ON CONFLICT (symbol) DO UPDATE SET note = excluded.note`,
		sym.String(), note, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("postgres: add watchlist: %w", err)
	}
	return nil
}

// RemoveWatchlist drops a symbol. Removing an absent symbol is not an error.
func (d *DB) RemoveWatchlist(ctx context.Context, sym marketdata.Symbol) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM watchlist WHERE symbol = $1`, sym.String()); err != nil {
		return fmt.Errorf("postgres: remove watchlist: %w", err)
	}
	return nil
}

// ConsumeBudget claims one unit of a provider's period allowance in a single
// conditional statement, so concurrent fetchers cannot overrun a small daily
// limit. No row back means the limit is reached.
func (d *DB) ConsumeBudget(ctx context.Context, provider, period string, limit int) (bool, int, error) {
	if limit <= 0 {
		return false, 0, nil
	}
	var used int
	err := d.db.QueryRowContext(ctx, `
INSERT INTO provider_budget (provider, period, used, updated_at)
VALUES ($1, $2, 1, now())
ON CONFLICT (provider, period) DO UPDATE
    SET used = provider_budget.used + 1, updated_at = now()
    WHERE provider_budget.used < $3
RETURNING used`, provider, period, limit).Scan(&used)

	if errors.Is(err, sql.ErrNoRows) {
		// The WHERE clause on the conflict path suppressed the update: the
		// allowance is spent.
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("postgres: consume budget: %w", err)
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	return true, remaining, nil
}

// BudgetUsage reports consumption for a period without consuming anything.
func (d *DB) BudgetUsage(ctx context.Context, provider, period string) (int, error) {
	var used int
	err := d.db.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT used FROM provider_budget WHERE provider = $1 AND period = $2), 0)`,
		provider, period).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("postgres: budget usage: %w", err)
	}
	return used, nil
}

// RecordHealth upserts a dependency's health.
//
// LastOKAt and LastErrorAt are preserved across updates when the incoming
// record does not set them, so a current failure still shows when the
// dependency last worked.
func (d *DB) RecordHealth(ctx context.Context, h storage.ProviderHealth) error {
	var okAt, errAt any
	if h.LastOKAt != nil {
		okAt = h.LastOKAt.UTC()
	}
	if h.LastErrorAt != nil {
		errAt = h.LastErrorAt.UTC()
	}
	_, err := d.db.ExecContext(ctx, `
INSERT INTO provider_health (provider, kind, status, message, last_ok_at, last_error_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (provider) DO UPDATE SET
    kind = excluded.kind,
    status = excluded.status,
    message = excluded.message,
    last_ok_at = COALESCE(excluded.last_ok_at, provider_health.last_ok_at),
    last_error_at = COALESCE(excluded.last_error_at, provider_health.last_error_at),
    updated_at = excluded.updated_at`,
		h.Provider, h.Kind, string(h.Status), h.Message, okAt, errAt, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("postgres: record health: %w", err)
	}
	return nil
}

// ListHealth returns every known dependency's health record.
func (d *DB) ListHealth(ctx context.Context) ([]storage.ProviderHealth, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT provider, kind, status, message, last_ok_at, last_error_at, updated_at
FROM provider_health ORDER BY kind, provider`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list health: %w", err)
	}
	defer rows.Close()

	var out []storage.ProviderHealth
	for rows.Next() {
		var (
			h           storage.ProviderHealth
			status      string
			okAt, errAt sql.NullTime
		)
		if err := rows.Scan(&h.Provider, &h.Kind, &status, &h.Message, &okAt, &errAt, &h.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan health: %w", err)
		}
		h.Status = storage.HealthStatus(status)
		if okAt.Valid {
			t := okAt.Time.UTC()
			h.LastOKAt = &t
		}
		if errAt.Valid {
			t := errAt.Time.UTC()
			h.LastErrorAt = &t
		}
		h.UpdatedAt = h.UpdatedAt.UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// WatchedSymbols is the watchlist as parsed symbols.
func (d *DB) WatchedSymbols(ctx context.Context) ([]marketdata.Symbol, error) {
	entries, err := d.ListWatchlist(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]marketdata.Symbol, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Symbol)
	}
	return out, nil
}
