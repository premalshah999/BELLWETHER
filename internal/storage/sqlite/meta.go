package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

// ListWatchlist returns watchlist entries in operator-defined order. Rows whose
// symbol no longer parses are skipped rather than failing the whole read, so a
// bad row can never blank the left rail.
func (s *DB) ListWatchlist(ctx context.Context) ([]storage.WatchlistEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT symbol, note, position, added_at FROM watchlist ORDER BY position ASC, added_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list watchlist: %w", err)
	}
	defer rows.Close()

	var out []storage.WatchlistEntry
	for rows.Next() {
		var (
			raw     string
			e       storage.WatchlistEntry
			addedAt int64
		)
		if err := rows.Scan(&raw, &e.Note, &e.Position, &addedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan watchlist: %w", err)
		}
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			continue
		}
		e.Symbol = sym
		e.AddedAt = time.Unix(addedAt, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddWatchlist appends a symbol, or updates the note if it is already present.
// New entries land at the end of the current ordering.
func (s *DB) AddWatchlist(ctx context.Context, sym marketdata.Symbol, note string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO watchlist (symbol, note, position, added_at)
VALUES (?, ?, COALESCE((SELECT MAX(position) + 1 FROM watchlist), 0), ?)
ON CONFLICT (symbol) DO UPDATE SET note = excluded.note`,
		sym.String(), note, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("sqlite: add watchlist: %w", err)
	}
	return nil
}

// RemoveWatchlist drops a symbol. Removing an absent symbol is not an error.
func (s *DB) RemoveWatchlist(ctx context.Context, sym marketdata.Symbol) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM watchlist WHERE symbol = ?`, sym.String()); err != nil {
		return fmt.Errorf("sqlite: remove watchlist: %w", err)
	}
	return nil
}

// ConsumeBudget increments a provider's period counter inside a transaction and
// reports whether the caller stayed within limit. The read and the write share
// one transaction so two concurrent fetches cannot both slip past the last
// remaining request.
func (s *DB) ConsumeBudget(ctx context.Context, provider, period string, limit int) (bool, int, error) {
	if limit <= 0 {
		return false, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, fmt.Errorf("sqlite: begin consume budget: %w", err)
	}
	defer tx.Rollback()

	var used int
	err = tx.QueryRowContext(ctx,
		`SELECT used FROM provider_budget WHERE provider = ? AND period = ?`, provider, period).Scan(&used)
	if err != nil && err.Error() != "sql: no rows in result set" {
		return false, 0, fmt.Errorf("sqlite: read budget: %w", err)
	}
	if used >= limit {
		return false, 0, nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_budget (provider, period, used) VALUES (?, ?, 1)
ON CONFLICT (provider, period) DO UPDATE SET used = used + 1`, provider, period); err != nil {
		return false, 0, fmt.Errorf("sqlite: increment budget: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, 0, fmt.Errorf("sqlite: commit budget: %w", err)
	}
	return true, limit - (used + 1), nil
}

// BudgetUsage reports consumption for a period without consuming anything.
func (s *DB) BudgetUsage(ctx context.Context, provider, period string) (int, error) {
	var used int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT used FROM provider_budget WHERE provider = ? AND period = ?), 0)`,
		provider, period).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("sqlite: budget usage: %w", err)
	}
	return used, nil
}

// RecordHealth upserts a dependency's health. LastOKAt and LastErrorAt are
// preserved across updates when the incoming record does not set them, so a
// current failure still shows when the dependency last worked.
func (s *DB) RecordHealth(ctx context.Context, h storage.ProviderHealth) error {
	var okAt, errAt *int64
	if h.LastOKAt != nil {
		v := h.LastOKAt.UTC().Unix()
		okAt = &v
	}
	if h.LastErrorAt != nil {
		v := h.LastErrorAt.UTC().Unix()
		errAt = &v
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO provider_health (provider, kind, status, message, last_ok_at, last_error_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider) DO UPDATE SET
    kind = excluded.kind,
    status = excluded.status,
    message = excluded.message,
    last_ok_at = COALESCE(excluded.last_ok_at, provider_health.last_ok_at),
    last_error_at = COALESCE(excluded.last_error_at, provider_health.last_error_at),
    updated_at = excluded.updated_at`,
		h.Provider, h.Kind, string(h.Status), h.Message, okAt, errAt, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("sqlite: record health: %w", err)
	}
	return nil
}

// ListHealth returns every known dependency's health record.
func (s *DB) ListHealth(ctx context.Context) ([]storage.ProviderHealth, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT provider, kind, status, message, last_ok_at, last_error_at, updated_at
FROM provider_health ORDER BY kind, provider`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list health: %w", err)
	}
	defer rows.Close()

	var out []storage.ProviderHealth
	for rows.Next() {
		var (
			h           storage.ProviderHealth
			status      string
			okAt, errAt *int64
			updatedAt   int64
		)
		if err := rows.Scan(&h.Provider, &h.Kind, &status, &h.Message, &okAt, &errAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan health: %w", err)
		}
		h.Status = storage.HealthStatus(status)
		if okAt != nil {
			t := time.Unix(*okAt, 0).UTC()
			h.LastOKAt = &t
		}
		if errAt != nil {
			t := time.Unix(*errAt, 0).UTC()
			h.LastErrorAt = &t
		}
		h.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// compile-time assertion that the SQLite implementation satisfies every port.
var _ storage.Store = (*DB)(nil)
