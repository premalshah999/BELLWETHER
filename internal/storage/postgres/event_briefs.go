package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// EventBrief reads a stored brief, or an empty string when none exists.
func (d *DB) EventBrief(ctx context.Context, eventID int64) (string, error) {
	var brief string
	err := d.db.QueryRowContext(ctx,
		`SELECT brief FROM event_briefs WHERE event_id = $1`, eventID).Scan(&brief)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("event brief: %w", err)
	}
	return brief, nil
}

// EventBriefs reads many at once, keyed by event id.
//
// One query for a page of the feed rather than one per item: a hundred round
// trips to decorate a hundred headlines would cost more than the headlines.
func (d *DB) EventBriefs(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT event_id, brief FROM event_briefs WHERE event_id = ANY($1)`, int64Array(ids))
	if err != nil {
		return nil, fmt.Errorf("event briefs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id    int64
			brief string
		)
		if err := rows.Scan(&id, &brief); err != nil {
			return nil, fmt.Errorf("event briefs: scan: %w", err)
		}
		out[id] = brief
	}
	return out, rows.Err()
}

// SaveEventBrief stores one, replacing any earlier attempt.
func (d *DB) SaveEventBrief(ctx context.Context, eventID int64, brief, model string) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO event_briefs (event_id, brief, model)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO UPDATE
		SET brief = EXCLUDED.brief, model = EXCLUDED.model, created_at = now()`,
		eventID, brief, model)
	if err != nil {
		return fmt.Errorf("save event brief: %w", err)
	}
	return nil
}
