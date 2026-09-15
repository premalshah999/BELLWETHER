package postgres

import (
	"context"
	"fmt"
	"time"
)

// EventSymbolPair is one company an event named, with the moment this
// system actually knew about the event -- the raw population an event
// study draws its samples from. See internal/eventstudy's package doc for
// why DiscoveredAt, not occurred_at or published_at, is the anchor.
type EventSymbolPair struct {
	Symbol       string
	DiscoveredAt time.Time
}

// EventTypeSymbolPairs returns every (symbol, discovered_at) pair recorded
// against events of one type.
func (d *DB) EventTypeSymbolPairs(ctx context.Context, eventType string) ([]EventSymbolPair, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT ee.symbol, e.discovered_at
		FROM event_entities ee
		JOIN events e ON e.id = ee.event_id
		WHERE e.event_type = $1`, eventType)
	if err != nil {
		return nil, fmt.Errorf("event type symbol pairs: %w", err)
	}
	defer rows.Close()

	var out []EventSymbolPair
	for rows.Next() {
		var p EventSymbolPair
		if err := rows.Scan(&p.Symbol, &p.DiscoveredAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EventTypeCount is one event type's population size, for the picker an
// event study is run from -- built from what the archive actually holds
// rather than the full taxonomy, most of which any given deployment will
// never have seen an example of.
type EventTypeCount struct {
	EventType string `json:"event_type"`
	Count     int    `json:"count"`
}

// EventTypeCounts lists every event type the archive holds at least one
// event under, most populous first.
func (d *DB) EventTypeCounts(ctx context.Context) ([]EventTypeCount, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT event_type, count(*) FROM events
		GROUP BY event_type
		ORDER BY count(*) DESC, event_type`)
	if err != nil {
		return nil, fmt.Errorf("event type counts: %w", err)
	}
	defer rows.Close()

	var out []EventTypeCount
	for rows.Next() {
		var c EventTypeCount
		if err := rows.Scan(&c.EventType, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
