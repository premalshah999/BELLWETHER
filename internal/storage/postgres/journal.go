package postgres

import (
	"context"
	"database/sql"
	"errors"
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

// RecordTrade stores a closed trade entered by hand: one bought and sold
// outside this app, or before it existed.
func (d *DB) RecordTrade(ctx context.Context, t Trade) (Trade, error) {
	t.RealizedPnL = (t.ExitPrice - t.EntryPrice) * t.Quantity
	err := d.db.QueryRowContext(ctx, `
		INSERT INTO trades (symbol, quantity, entry_price, exit_price, opened_at, closed_at, realized_pnl, account, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at`,
		t.Symbol, t.Quantity, t.EntryPrice, t.ExitPrice, t.OpenedAt, t.ClosedAt, t.RealizedPnL, t.Account, t.Notes,
	).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		return Trade{}, fmt.Errorf("record trade: %w", err)
	}
	return t, nil
}

// DeleteTrade removes one closed trade from the journal.
func (d *DB) DeleteTrade(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM trades WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete trade: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LatestCatalyst finds the catalyst for one entry, the same way
// TradesWithCatalysts does, on this database alone. The journal asks the news
// archive with it for trades whose catalyst has aged off the primary.
func (d *DB) LatestCatalyst(ctx context.Context, symbol string, openedAt time.Time) (*TradeCatalyst, error) {
	var (
		tc   TradeCatalyst
		id   int64
		at   time.Time
		days int
	)
	err := d.db.QueryRowContext(ctx, `
SELECT ev.id, ev.event_type, ev.headline, ev.discovered_at, ev.official, ev.best_trust,
       ($2::date - ev.discovered_at::date)
FROM event_entities ee
JOIN events ev ON ev.id = ee.event_id
WHERE ee.symbol = $1
  AND ev.discovered_at < ($2::date + INTERVAL '1 day')
  AND ev.discovered_at >= ($2::date - ($3 * INTERVAL '1 day'))
ORDER BY ev.discovered_at DESC
LIMIT 1`, symbol, openedAt, catalystLookbackDays).Scan(&id, &tc.EventType, &tc.Headline, &at, &tc.Official, &tc.BestTrust, &days)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest catalyst: %w", err)
	}
	d2 := float64(days)
	tc.EventID, tc.EventDiscoveredAt, tc.DaysBeforeEntry = &id, &at, &d2
	return &tc, nil
}
