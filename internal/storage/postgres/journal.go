package postgres

import (
	"context"
	"fmt"
	"time"
)

// catalystLookbackDays is how far before a trade's entry to look for an
// event that might have prompted it. Five trading days: long enough to
// catch a filing an operator read over a weekend and acted on Monday,
// short enough that reaching further back would start attributing a trade
// to whatever happened to be the last thing published on the name, which
// is not attribution, it is coincidence.
const catalystLookbackDays = 5

// TradeCatalyst is one closed trade with the event, if any, that most
// plausibly prompted it: the latest classified event naming its symbol,
// discovered on or before the entry day within the lookback window.
// Discovered, never published: a trade cannot have been prompted by something
// not yet known. Most trades have none, and that absence is shown as it is.
type TradeCatalyst struct {
	Trade
	EventID           *int64
	EventType         string
	Headline          string
	EventDiscoveredAt *time.Time
	Official          bool
	BestTrust         int
	// DaysBeforeEntry is how many days separated the catalyst's discovery
	// from the trade's entry. Zero means discovered the same day.
	DaysBeforeEntry *float64
}

// TradesWithCatalysts returns every closed trade with its best-candidate
// catalyst attached, most recently closed first.
func (d *DB) TradesWithCatalysts(ctx context.Context, limit int) ([]TradeCatalyst, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT t.id, t.symbol, t.quantity, t.entry_price, t.exit_price, t.opened_at, t.closed_at,
       t.realized_pnl, t.account, t.notes, t.created_at,
       c.event_id, c.event_type, c.headline, c.discovered_at, c.official, c.best_trust, c.days_before
FROM (
    SELECT * FROM trades ORDER BY closed_at DESC, id DESC LIMIT $1
) t
LEFT JOIN LATERAL (
    SELECT ev.id AS event_id, ev.event_type, ev.headline, ev.discovered_at, ev.official, ev.best_trust,
           -- Whole calendar days between the catalyst's discovery and the
           -- entry, both compared as dates: same-day is 0, never negative,
           -- because the query below already guarantees discovery falls on
           -- or before the entry's own day.
           (t.opened_at - ev.discovered_at::date) AS days_before
    FROM event_entities ee
    JOIN events ev ON ev.id = ee.event_id
    WHERE ee.symbol = t.symbol
      AND ev.discovered_at < (t.opened_at + INTERVAL '1 day')
      AND ev.discovered_at >= (t.opened_at - ($2 * INTERVAL '1 day'))
    ORDER BY ev.discovered_at DESC
    LIMIT 1
) c ON true
ORDER BY t.closed_at DESC, t.id DESC`, limit, catalystLookbackDays)
	if err != nil {
		return nil, fmt.Errorf("trades with catalysts: %w", err)
	}
	defer rows.Close()

	var out []TradeCatalyst
	for rows.Next() {
		var tc TradeCatalyst
		var (
			eventID      *int64
			eventType    *string
			headline     *string
			discoveredAt *time.Time
			official     *bool
			bestTrust    *int
			daysBefore   *int
		)
		if err := rows.Scan(
			&tc.ID, &tc.Symbol, &tc.Quantity, &tc.EntryPrice, &tc.ExitPrice, &tc.OpenedAt, &tc.ClosedAt,
			&tc.RealizedPnL, &tc.Account, &tc.Notes, &tc.CreatedAt,
			&eventID, &eventType, &headline, &discoveredAt, &official, &bestTrust, &daysBefore,
		); err != nil {
			return nil, err
		}
		if eventID != nil {
			tc.EventID = eventID
			tc.EventType = *eventType
			tc.Headline = *headline
			tc.EventDiscoveredAt = discoveredAt
			tc.Official = *official
			tc.BestTrust = *bestTrust
			days := float64(*daysBefore)
			tc.DaysBeforeEntry = &days
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}
