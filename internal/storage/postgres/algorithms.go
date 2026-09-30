package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/storage"
)

// ErrNotFound is returned when an addressed row does not exist.
// ErrNotFound wraps the shared sentinel so callers can match either.
var ErrNotFound = fmt.Errorf("postgres: %w", storage.ErrNotFound)

// ListAlgorithms returns every stored algorithm, newest first.
func (d *DB) ListAlgorithms(ctx context.Context) ([]*algo.Algorithm, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, definition, enabled, created_at, updated_at
FROM algorithms ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list algorithms: %w", err)
	}
	defer rows.Close()

	var out []*algo.Algorithm
	for rows.Next() {
		a, err := scanAlgorithm(rows)
		if err != nil {
			return nil, err
		}
		// A definition that no longer decodes is skipped rather than failing
		// the list: one algorithm saved by an older schema must not make the
		// whole page unreachable.
		if a == nil {
			continue
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanAlgorithm(r rowScanner) (*algo.Algorithm, error) {
	var (
		id                   int64
		definition           []byte
		enabled              bool
		createdAt, updatedAt time.Time
	)
	if err := r.Scan(&id, &definition, &enabled, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("postgres: scan algorithm: %w", err)
	}
	var a algo.Algorithm
	if err := json.Unmarshal(definition, &a); err != nil {
		return nil, nil
	}
	a.ID = id
	a.Enabled = enabled
	a.CreatedAt = createdAt.UTC()
	a.UpdatedAt = updatedAt.UTC()
	return &a, nil
}

// CreateAlgorithm stores a new algorithm and returns its id.
//
// The rule tree is stored as JSONB rather than text, so the definition is
// validated as JSON by the database and can be queried into — "which
// algorithms reference RSI" becomes an indexed question rather than a scan
// through decoded strings.
func (d *DB) CreateAlgorithm(ctx context.Context, a *algo.Algorithm) (int64, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	definition, err := json.Marshal(a)
	if err != nil {
		return 0, fmt.Errorf("postgres: encode algorithm: %w", err)
	}
	var (
		id  int64
		now time.Time
	)
	err = d.db.QueryRowContext(ctx, `
INSERT INTO algorithms (name, symbols, watchlist_ids, definition, enabled, interval, cooldown_hours, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now(), now())
RETURNING id, created_at`,
		a.Name, stringArray(a.Symbols), int64Array(a.WatchlistIDs), definition,
		a.Enabled, a.Interval, a.CooldownHours).Scan(&id, &now)
	if err != nil {
		return 0, fmt.Errorf("postgres: create algorithm: %w", err)
	}
	a.ID = id
	a.CreatedAt = now.UTC()
	a.UpdatedAt = a.CreatedAt
	return id, nil
}

// UpdateAlgorithm replaces a stored algorithm.
func (d *DB) UpdateAlgorithm(ctx context.Context, a *algo.Algorithm) error {
	if err := a.Validate(); err != nil {
		return err
	}
	definition, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("postgres: encode algorithm: %w", err)
	}
	var updated time.Time
	err = d.db.QueryRowContext(ctx, `
UPDATE algorithms
SET name = $1, symbols = $2, watchlist_ids = $3, definition = $4, enabled = $5,
    interval = $6, cooldown_hours = $7, updated_at = now()
WHERE id = $8
RETURNING updated_at`,
		a.Name, stringArray(a.Symbols), int64Array(a.WatchlistIDs), definition,
		a.Enabled, a.Interval, a.CooldownHours, a.ID).Scan(&updated)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: update algorithm: %w", err)
	}
	a.UpdatedAt = updated.UTC()
	return nil
}

// DeleteAlgorithm removes an algorithm.
//
// Its per-symbol cooldown state and evaluation history go with it through the
// foreign key's ON DELETE CASCADE, rather than through a second statement that
// could be skipped if the first failed.
func (d *DB) DeleteAlgorithm(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM algorithms WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete algorithm: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres: delete algorithm rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
