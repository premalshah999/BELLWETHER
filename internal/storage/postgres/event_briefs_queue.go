package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// UnbriefedEvents returns important recent events that have no brief yet.
//
// Most-important-first and newest-first within that, so a capped run spends
// its budget on what matters rather than on whatever happened to arrive last.
// Bounded by age as well as by count: briefing a week-old filing nobody opened
// is spending on an answer to a question that stopped being asked.
func (d *DB) UnbriefedEvents(ctx context.Context, minImportance, limit int, within time.Duration) ([]news.Event, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT e.id, e.headline, COALESCE(e.summary, ''), COALESCE(e.event_type, ''),
		       e.official, e.published_at, e.discovered_at
		FROM events e
		LEFT JOIN event_briefs b ON b.event_id = e.id
		WHERE b.event_id IS NULL
		  AND e.importance >= $1
		  AND e.discovered_at > now() - $2::interval
		ORDER BY e.importance DESC, e.discovered_at DESC
		LIMIT $3`,
		minImportance, fmt.Sprintf("%d seconds", int(within.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("unbriefed events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []news.Event{}
	for rows.Next() {
		var (
			e         news.Event
			pub, disc sql.NullTime
		)
		if err := rows.Scan(&e.ID, &e.Headline, &e.Summary, &e.Type, &e.Official, &pub, &disc); err != nil {
			return nil, fmt.Errorf("unbriefed events: scan: %w", err)
		}
		e.PublishedAt = timeOrZero(pub)
		e.DiscoveredAt = timeOrZero(disc)
		out = append(out, e)
	}
	return out, rows.Err()
}
