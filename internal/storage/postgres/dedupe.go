package postgres

import (
	"context"
	"fmt"
	"time"
)

// RecentEventForDedupe is one event as the duplicate merger sees it.
type RecentEventForDedupe struct {
	ID           int64
	Headline     string
	TitleKey     string
	Publisher    string
	DiscoveredAt time.Time
	BestTrust    int
	Symbols      []string
}

// RecentEventsForDedupe lists events discovered since a time, with the
// publisher of their best evidence and the companies they name.
func (d *DB) RecentEventsForDedupe(ctx context.Context, since time.Time) ([]RecentEventForDedupe, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT e.id, e.headline, e.title_key, e.discovered_at, e.best_trust,
       COALESCE((SELECT ri.publisher FROM event_evidence ee JOIN raw_items ri ON ri.id = ee.raw_item_id
                 WHERE ee.event_id = e.id ORDER BY ee.trust DESC, ri.discovered_at LIMIT 1), ''),
       COALESCE((SELECT array_agg(x.symbol ORDER BY x.symbol) FROM event_entities x WHERE x.event_id = e.id), '{}')
FROM events e
WHERE e.discovered_at >= $1
ORDER BY e.discovered_at, e.id`, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("postgres: recent events for dedupe: %w", err)
	}
	defer rows.Close()
	var out []RecentEventForDedupe
	for rows.Next() {
		var r RecentEventForDedupe
		if err := rows.Scan(&r.ID, &r.Headline, &r.TitleKey, &r.DiscoveredAt, &r.BestTrust,
			&r.Publisher, (*stringArray)(&r.Symbols)); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RetitleEvent sets an event's headline and title key.
func (d *DB) RetitleEvent(ctx context.Context, id int64, headline, titleKey string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE events SET headline = $2, title_key = $3 WHERE id = $1`, id, headline, titleKey)
	return err
}

// MergeEvents folds duplicate events into a keeper: their evidence, companies,
// sectors, facts and any brief move to the keeper, and the duplicates are
// deleted. One transaction, so a failure leaves nothing half-moved.
func (d *DB) MergeEvents(ctx context.Context, keeper int64, dups []int64, at time.Time) error {
	if len(dups) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ids := int64Array(dups)
	steps := []string{
		`INSERT INTO event_evidence (event_id, raw_item_id, source_id, trust, added_at)
         SELECT $1, raw_item_id, source_id, trust, added_at FROM event_evidence WHERE event_id = ANY($2::bigint[])
         ON CONFLICT DO NOTHING`,
		`INSERT INTO event_entities SELECT $1, x.symbol, x.relationship, x.match_confidence, x.match_method,
                x.direction, x.impact_strength, x.rationale
         FROM event_entities x WHERE x.event_id = ANY($2::bigint[]) ON CONFLICT DO NOTHING`,
		`INSERT INTO event_sectors (event_id, sector) SELECT $1, sector FROM event_sectors
         WHERE event_id = ANY($2::bigint[]) ON CONFLICT DO NOTHING`,
		`INSERT INTO event_facts SELECT $1, f.key, f.value, f.num FROM event_facts f
         WHERE f.event_id = ANY($2::bigint[]) ON CONFLICT DO NOTHING`,
		`INSERT INTO event_briefs SELECT $1, b.brief, b.model, b.tokens, b.created_at FROM event_briefs b
         WHERE b.event_id = ANY($2::bigint[]) ORDER BY b.created_at LIMIT 1 ON CONFLICT DO NOTHING`,
		`DELETE FROM events WHERE id = ANY($2::bigint[]) AND id <> $1`,
	}
	for _, q := range steps {
		if _, err := tx.ExecContext(ctx, q, keeper, ids); err != nil {
			return fmt.Errorf("postgres: merge events into %d: %w", keeper, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return d.TouchEvent(ctx, keeper, at)
}
