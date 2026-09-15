package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/algo"
)

// InsertAlert records a fired alert and stamps the cooldown clock.
//
// Both happen in one transaction. If they could diverge, a crash between them
// would either lose the alert or leave the cooldown unset and re-notify on the
// next tick.
func (s *DB) InsertAlert(ctx context.Context, a *alerts.Alert) (int64, error) {
	conditions, err := json.Marshal(a.Conditions)
	if err != nil {
		return 0, fmt.Errorf("sqlite: encode alert conditions: %w", err)
	}
	delivery, err := json.Marshal(a.Delivery)
	if err != nil {
		return 0, fmt.Errorf("sqlite: encode alert delivery: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlite: begin insert alert: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
INSERT INTO alerts (algorithm_id, algorithm_name, symbol, interval, fired_at, bar_time,
                    price, summary, conditions, ai_context, ai_status, delivery)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.AlgorithmID, a.AlgorithmName, a.Symbol, a.Interval,
		a.FiredAt.UTC().Unix(), a.BarTime.UTC().Unix(), a.Price, a.Summary,
		string(conditions), a.AIContext, a.AIStatus, string(delivery))
	if err != nil {
		return 0, fmt.Errorf("sqlite: insert alert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: alert id: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO algorithm_symbol_state (algorithm_id, symbol, last_fired_at)
VALUES (?, ?, ?)
ON CONFLICT (algorithm_id, symbol) DO UPDATE SET last_fired_at = excluded.last_fired_at`,
		a.AlgorithmID, a.Symbol, a.FiredAt.UTC().Unix()); err != nil {
		return 0, fmt.Errorf("sqlite: stamp cooldown: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sqlite: commit insert alert: %w", err)
	}
	a.ID = id
	return id, nil
}

// ListAlerts returns alerts newest first, subject to a filter.
func (s *DB) ListAlerts(ctx context.Context, f alerts.Filter) ([]alerts.Alert, error) {
	var (
		where []string
		args  []any
	)
	if f.AlgorithmID != 0 {
		where = append(where, "algorithm_id = ?")
		args = append(args, f.AlgorithmID)
	}
	if f.Symbol != "" {
		where = append(where, "symbol = ?")
		args = append(args, f.Symbol)
	}
	if f.UnreadOnly {
		where = append(where, "read_at IS NULL")
	}
	if !f.Before.IsZero() {
		where = append(where, "fired_at < ?")
		args = append(args, f.Before.UTC().Unix())
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
	query += " ORDER BY fired_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list alerts: %w", err)
	}
	defer rows.Close()

	var out []alerts.Alert
	for rows.Next() {
		var (
			a                   alerts.Alert
			firedAt, barTime    int64
			conditions, deliver string
			readAt              *int64
		)
		if err := rows.Scan(&a.ID, &a.AlgorithmID, &a.AlgorithmName, &a.Symbol, &a.Interval,
			&firedAt, &barTime, &a.Price, &a.Summary, &conditions,
			&a.AIContext, &a.AIStatus, &deliver, &readAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan alert: %w", err)
		}
		a.FiredAt = time.Unix(firedAt, 0).UTC()
		a.BarTime = time.Unix(barTime, 0).UTC()
		if readAt != nil {
			t := time.Unix(*readAt, 0).UTC()
			a.ReadAt = &t
		}
		// A snapshot that no longer decodes must not sink the whole feed; the
		// alert's summary text is still useful on its own.
		if err := json.Unmarshal([]byte(conditions), &a.Conditions); err != nil {
			a.Conditions = []algo.ConditionResult{}
		}
		if err := json.Unmarshal([]byte(deliver), &a.Delivery); err != nil {
			a.Delivery = []alerts.DeliveryRecord{}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkAlertRead acknowledges one alert.
func (s *DB) MarkAlertRead(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET read_at = ? WHERE id = ? AND read_at IS NULL`,
		time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("sqlite: mark alert read: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Already read, or gone. Distinguish the two so the API can 404.
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM alerts WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("sqlite: check alert: %w", err)
		}
		if exists == 0 {
			return ErrNotFound
		}
	}
	return nil
}

// MarkAllAlertsRead acknowledges everything currently unread.
func (s *DB) MarkAllAlertsRead(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET read_at = ? WHERE read_at IS NULL`, time.Now().UTC().Unix()); err != nil {
		return fmt.Errorf("sqlite: mark all alerts read: %w", err)
	}
	return nil
}

// UnreadAlertCount powers the badge on the alert rail.
func (s *DB) UnreadAlertCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM alerts WHERE read_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite: unread alert count: %w", err)
	}
	return n, nil
}

// LastFiredAt reports when an algorithm last alerted on a symbol.
func (s *DB) LastFiredAt(ctx context.Context, algorithmID int64, symbol string) (time.Time, bool, error) {
	var at *int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_fired_at FROM algorithm_symbol_state WHERE algorithm_id = ? AND symbol = ?`,
		algorithmID, symbol).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("sqlite: last fired: %w", err)
	}
	if at == nil {
		return time.Time{}, false, nil
	}
	return time.Unix(*at, 0).UTC(), true, nil
}

// RecordEvaluation stores an evaluation outcome, firing or not, so that "why
// didn't this alert?" has an answer.
func (s *DB) RecordEvaluation(ctx context.Context, rec alerts.EvaluationRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO algorithm_symbol_state (algorithm_id, symbol, last_evaluated_at, last_status, last_reason, last_summary)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (algorithm_id, symbol) DO UPDATE SET
    last_evaluated_at = excluded.last_evaluated_at,
    last_status = excluded.last_status,
    last_reason = excluded.last_reason,
    last_summary = excluded.last_summary`,
		rec.AlgorithmID, rec.Symbol, rec.At.UTC().Unix(),
		string(rec.Status), rec.Reason, rec.Summary)
	if err != nil {
		return fmt.Errorf("sqlite: record evaluation: %w", err)
	}
	return nil
}

// LastEvaluations returns the latest outcome per symbol for one algorithm.
func (s *DB) LastEvaluations(ctx context.Context, algorithmID int64) ([]alerts.EvaluationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT symbol, last_evaluated_at, last_status, last_reason, last_summary
FROM algorithm_symbol_state
WHERE algorithm_id = ? AND last_evaluated_at IS NOT NULL
ORDER BY symbol`, algorithmID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: last evaluations: %w", err)
	}
	defer rows.Close()

	var out []alerts.EvaluationRecord
	for rows.Next() {
		var (
			rec    alerts.EvaluationRecord
			at     int64
			status string
		)
		if err := rows.Scan(&rec.Symbol, &at, &status, &rec.Reason, &rec.Summary); err != nil {
			return nil, fmt.Errorf("sqlite: scan evaluation: %w", err)
		}
		rec.AlgorithmID = algorithmID
		rec.At = time.Unix(at, 0).UTC()
		rec.Status = algo.Status(status)
		out = append(out, rec)
	}
	return out, rows.Err()
}
