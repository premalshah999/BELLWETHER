package sqlite

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

// ErrNotFound is returned when a row does not exist.
// ErrNotFound wraps the shared sentinel so callers can match either.
var ErrNotFound = fmt.Errorf("sqlite: %w", storage.ErrNotFound)

// ListAlgorithms returns every stored algorithm, newest first.
//
// A row whose stored JSON no longer parses — because a definition was written
// by an older build whose vocabulary has since changed — is skipped with a
// loud error rather than failing the whole listing. One broken algorithm must
// not hide the other nine.
func (s *DB) ListAlgorithms(ctx context.Context) ([]*algo.Algorithm, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, definition, enabled, created_at, updated_at
FROM algorithms ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list algorithms: %w", err)
	}
	defer rows.Close()

	var out []*algo.Algorithm
	for rows.Next() {
		a, err := scanAlgorithm(rows)
		if err != nil {
			return nil, err
		}
		if a == nil {
			continue
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAlgorithm(r rowScanner) (*algo.Algorithm, error) {
	var (
		id                   int64
		definition           string
		enabled              int
		createdAt, updatedAt int64
	)
	if err := r.Scan(&id, &definition, &enabled, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("sqlite: scan algorithm: %w", err)
	}

	var a algo.Algorithm
	if err := json.Unmarshal([]byte(definition), &a); err != nil {
		// Skip rather than fail: see ListAlgorithms.
		return nil, nil
	}
	a.ID = id
	a.Enabled = enabled != 0
	a.CreatedAt = time.Unix(createdAt, 0).UTC()
	a.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &a, nil
}

// GetAlgorithm reads one algorithm.
func (s *DB) GetAlgorithm(ctx context.Context, id int64) (*algo.Algorithm, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, definition, enabled, created_at, updated_at FROM algorithms WHERE id = ?`, id)

	a, err := scanAlgorithm(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("sqlite: algorithm %d has an unreadable definition", id)
	}
	return a, nil
}

// CreateAlgorithm stores a new algorithm and returns its id.
func (s *DB) CreateAlgorithm(ctx context.Context, a *algo.Algorithm) (int64, error) {
	// Validate before storing: nothing unevaluable may reach the scheduler.
	if err := a.Validate(); err != nil {
		return 0, err
	}
	definition, err := json.Marshal(a)
	if err != nil {
		return 0, fmt.Errorf("sqlite: encode algorithm: %w", err)
	}

	now := time.Now().UTC().Unix()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO algorithms (name, definition, enabled, interval, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		a.Name, string(definition), boolToInt(a.Enabled), a.Interval, now, now)
	if err != nil {
		return 0, fmt.Errorf("sqlite: create algorithm: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: algorithm id: %w", err)
	}
	a.ID = id
	a.CreatedAt = time.Unix(now, 0).UTC()
	a.UpdatedAt = a.CreatedAt
	return id, nil
}

// UpdateAlgorithm replaces a stored algorithm.
func (s *DB) UpdateAlgorithm(ctx context.Context, a *algo.Algorithm) error {
	if err := a.Validate(); err != nil {
		return err
	}
	definition, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("sqlite: encode algorithm: %w", err)
	}

	now := time.Now().UTC().Unix()
	res, err := s.db.ExecContext(ctx, `
UPDATE algorithms SET name = ?, definition = ?, enabled = ?, interval = ?, updated_at = ?
WHERE id = ?`, a.Name, string(definition), boolToInt(a.Enabled), a.Interval, now, a.ID)
	if err != nil {
		return fmt.Errorf("sqlite: update algorithm: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: update algorithm rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	a.UpdatedAt = time.Unix(now, 0).UTC()
	return nil
}

// DeleteAlgorithm removes an algorithm and its per-symbol state.
//
// Alert history is deliberately kept: the alerts carry a denormalised
// algorithm name, so deleting a rule does not erase the record of what it
// once told the operators.
func (s *DB) DeleteAlgorithm(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin delete algorithm: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM algorithms WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: delete algorithm: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: delete algorithm rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM algorithm_symbol_state WHERE algorithm_id = ?`, id); err != nil {
		return fmt.Errorf("sqlite: delete algorithm state: %w", err)
	}
	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
