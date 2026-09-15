package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/algo"
)

// InsertAlert records a fired alert and stamps its cooldown.
//
// Both happen in one transaction because they are one fact: an alert that
// exists without its cooldown stamp will fire again on the next evaluation,
// which is the failure the cooldown exists to prevent.
func (d *DB) InsertAlert(ctx context.Context, a *alerts.Alert) (int64, error) {
	conditions, err := json.Marshal(a.Conditions)
	if err != nil {
		return 0, fmt.Errorf("postgres: encode alert conditions: %w", err)
	}
	delivery, err := json.Marshal(a.Delivery)
	if err != nil {
		return 0, fmt.Errorf("postgres: encode alert delivery: %w", err)
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("postgres: begin insert alert: %w", err)
	}
	defer tx.Rollback()

	var id int64
	if err := tx.QueryRowContext(ctx, `
INSERT INTO alerts (algorithm_id, algorithm_name, symbol, interval, fired_at,
                    bar_time, price, summary, conditions, ai_context, ai_status, delivery)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id`,
		a.AlgorithmID, a.AlgorithmName, a.Symbol, a.Interval, a.FiredAt.UTC(),
		nullTime(a.BarTime), a.Price, a.Summary, conditions,
		a.AIContext, a.AIStatus, delivery).Scan(&id); err != nil {
		return 0, fmt.Errorf("postgres: insert alert: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO algorithm_symbol_state (algorithm_id, symbol, last_fired_at)
VALUES ($1, $2, $3)
ON CONFLICT (algorithm_id, symbol) DO UPDATE SET last_fired_at = excluded.last_fired_at`,
		a.AlgorithmID, a.Symbol, a.FiredAt.UTC()); err != nil {
		return 0, fmt.Errorf("postgres: stamp cooldown: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("postgres: commit insert alert: %w", err)
	}
	a.ID = id
	return id, nil
}

// ListAlerts returns alerts matching a filter, most recent first.
func (d *DB) ListAlerts(ctx context.Context, f alerts.Filter) ([]alerts.Alert, error) {
	var (
		where []string
		args  []any
	)
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.AlgorithmID != 0 {
		add("algorithm_id = $%d", f.AlgorithmID)
	}
	if f.Symbol != "" {
		add("symbol = $%d", f.Symbol)
	}
	if f.UnreadOnly {
		where = append(where, "read_at IS NULL")
	}
	if !f.Before.IsZero() {
		add("fired_at < $%d", f.Before.UTC())
	}

	query := `
SELECT id, algorithm_id, algorithm_name, symbol, interval, fired_at, bar_time,
       price, summary, conditions, ai_context, ai_status, delivery, read_at
FROM alerts`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY fired_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list alerts: %w", err)
	}
	defer rows.Close()

	var out []alerts.Alert
	for rows.Next() {
		var (
			a                    alerts.Alert
			barTime, readAt      sql.NullTime
			conditions, delivery []byte
		)
		if err := rows.Scan(&a.ID, &a.AlgorithmID, &a.AlgorithmName, &a.Symbol, &a.Interval,
			&a.FiredAt, &barTime, &a.Price, &a.Summary, &conditions,
			&a.AIContext, &a.AIStatus, &delivery, &readAt); err != nil {
			return nil, fmt.Errorf("postgres: scan alert: %w", err)
		}
		a.FiredAt = a.FiredAt.UTC()
		a.BarTime = timeOrZero(barTime)
		if readAt.Valid {
			t := readAt.Time.UTC()
			a.ReadAt = &t
		}
		// A snapshot that no longer decodes degrades to an empty one rather
		// than failing the list: the alert itself is still the record that
		// something fired, and that is the part the operator needs.
		if err := json.Unmarshal(conditions, &a.Conditions); err != nil {
			a.Conditions = []algo.ConditionResult{}
		}
		if err := json.Unmarshal(delivery, &a.Delivery); err != nil {
			a.Delivery = []alerts.DeliveryRecord{}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkAlertRead marks one alert read.
func (d *DB) MarkAlertRead(ctx context.Context, id int64) error {
	var marked bool
	// One statement decides both outcomes: the RETURNING gives a row when the
	// alert exists, whether or not this call was the one that marked it, so
	// "already read" and "no such alert" are distinguished without a second
	// query racing the first.
	err := d.db.QueryRowContext(ctx, `
WITH updated AS (
    UPDATE alerts SET read_at = now() WHERE id = $1 AND read_at IS NULL RETURNING id
)
SELECT EXISTS (SELECT 1 FROM updated) OR EXISTS (SELECT 1 FROM alerts WHERE id = $1)`, id).Scan(&marked)
	if err != nil {
		return fmt.Errorf("postgres: mark alert read: %w", err)
	}
	if !marked {
		return ErrNotFound
	}
	return nil
}

// MarkAllAlertsRead clears the unread badge.
func (d *DB) MarkAllAlertsRead(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE alerts SET read_at = now() WHERE read_at IS NULL`); err != nil {
		return fmt.Errorf("postgres: mark all alerts read: %w", err)
	}
	return nil
}

// UnreadAlertCount backs the badge in the top bar.
func (d *DB) UnreadAlertCount(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT count(*) FROM alerts WHERE read_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: unread alert count: %w", err)
	}
	return n, nil
}

// LastFiredAt reports when an algorithm last fired for a symbol.
func (d *DB) LastFiredAt(ctx context.Context, algorithmID int64, symbol string) (time.Time, bool, error) {
	var at sql.NullTime
	err := d.db.QueryRowContext(ctx,
		`SELECT last_fired_at FROM algorithm_symbol_state WHERE algorithm_id = $1 AND symbol = $2`,
		algorithmID, symbol).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) || !at.Valid {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("postgres: last fired: %w", err)
	}
	return at.Time.UTC(), true, nil
}

// RecordEvaluation stores the outcome of one evaluation.
func (d *DB) RecordEvaluation(ctx context.Context, rec alerts.EvaluationRecord) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO algorithm_symbol_state
    (algorithm_id, symbol, last_evaluated_at, last_status, last_reason, last_summary)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (algorithm_id, symbol) DO UPDATE SET
    last_evaluated_at = excluded.last_evaluated_at,
    last_status       = excluded.last_status,
    last_reason       = excluded.last_reason,
    last_summary      = excluded.last_summary`,
		rec.AlgorithmID, rec.Symbol, rec.At.UTC(), string(rec.Status), rec.Reason, rec.Summary)
	if err != nil {
		return fmt.Errorf("postgres: record evaluation: %w", err)
	}
	return nil
}

// LastEvaluations returns the most recent evaluation per symbol.
func (d *DB) LastEvaluations(ctx context.Context, algorithmID int64) ([]alerts.EvaluationRecord, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT symbol, last_evaluated_at, last_status, last_reason, last_summary
FROM algorithm_symbol_state
WHERE algorithm_id = $1 AND last_evaluated_at IS NOT NULL
ORDER BY symbol`, algorithmID)
	if err != nil {
		return nil, fmt.Errorf("postgres: last evaluations: %w", err)
	}
	defer rows.Close()

	var out []alerts.EvaluationRecord
	for rows.Next() {
		var (
			rec    alerts.EvaluationRecord
			status string
		)
		if err := rows.Scan(&rec.Symbol, &rec.At, &status, &rec.Reason, &rec.Summary); err != nil {
			return nil, fmt.Errorf("postgres: scan evaluation: %w", err)
		}
		rec.AlgorithmID = algorithmID
		rec.At = rec.At.UTC()
		rec.Status = algo.Status(status)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// RecentAlertSummaries feeds the morning brief.
func (d *DB) RecentAlertSummaries(ctx context.Context, since time.Time, limit int) ([]ai.RecentAlert, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT algorithm_name, symbol, summary FROM alerts
WHERE fired_at >= $1 ORDER BY fired_at DESC LIMIT $2`, since.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: recent alert summaries: %w", err)
	}
	defer rows.Close()

	var out []ai.RecentAlert
	for rows.Next() {
		var a ai.RecentAlert
		if err := rows.Scan(&a.AlgorithmName, &a.Symbol, &a.Summary); err != nil {
			return nil, fmt.Errorf("postgres: scan recent alert: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
