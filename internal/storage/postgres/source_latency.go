package postgres

import (
	"context"
	"fmt"
	"time"
)

// SourceLatency is one source's record on multi-source events: how often it
// was first to carry a story later clustered into one event, and by how much.
// "First" is when this system attached its evidence, not the publisher's
// claimed time, so a back-dated feed does not look fast.
type SourceLatency struct {
	SourceID string
	// Participated is how many multi-source events this source contributed
	// evidence to at all -- the denominator a win rate needs to mean
	// anything. A source with one win out of one appearance is not "100%
	// reliable"; it has a sample size of one.
	Participated int
	TimesFirst   int
	// AvgLeadSeconds is, on the events this source won, how far ahead of the
	// next source it was on average. Nil when the source has never won.
	AvgLeadSeconds *float64
}

// SourceLatencyLeaderboard ranks sources by how often they were first to
// carry a story that at least one other source also carried, restricted to
// events discovered since the given time and to sources with at least
// minParticipation appearances (fewer than that is a sample too small for a
// win rate to describe anything).
func (d *DB) SourceLatencyLeaderboard(ctx context.Context, since time.Time, minParticipation int) ([]SourceLatency, error) {
	if minParticipation <= 0 {
		minParticipation = 1
	}
	rows, err := d.db.QueryContext(ctx, `
WITH ranked AS (
    SELECT
        ev.event_id,
        ev.source_id,
        ev.added_at,
        row_number() OVER (PARTITION BY ev.event_id ORDER BY ev.added_at ASC, ev.raw_item_id ASC) AS rnk,
        lead(ev.added_at) OVER (PARTITION BY ev.event_id ORDER BY ev.added_at ASC, ev.raw_item_id ASC) AS next_added_at
    FROM event_evidence ev
    JOIN events e ON e.id = ev.event_id
    WHERE e.source_count > 1 AND e.discovered_at >= $1
)
SELECT
    source_id,
    count(DISTINCT event_id) AS participated,
    count(*) FILTER (WHERE rnk = 1) AS times_first,
    avg(EXTRACT(EPOCH FROM (next_added_at - added_at))) FILTER (WHERE rnk = 1) AS avg_lead_seconds
FROM ranked
GROUP BY source_id
HAVING count(DISTINCT event_id) >= $2
ORDER BY times_first DESC, participated DESC`,
		since.UTC(), minParticipation)
	if err != nil {
		return nil, fmt.Errorf("source latency leaderboard: %w", err)
	}
	defer rows.Close()

	var out []SourceLatency
	for rows.Next() {
		var l SourceLatency
		var avgLead *float64
		if err := rows.Scan(&l.SourceID, &l.Participated, &l.TimesFirst, &avgLead); err != nil {
			return nil, err
		}
		l.AvgLeadSeconds = avgLead
		out = append(out, l)
	}
	return out, rows.Err()
}
