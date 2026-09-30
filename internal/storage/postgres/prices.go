package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// LoadCandles returns the most recent limit bars for a symbol, oldest first.
//
// FetchedAt on the result is the oldest fetch time among the returned rows,
// which is the conservative answer to "how stale might this window be?".
func (d *DB) LoadCandles(ctx context.Context, sym marketdata.Symbol, interval marketdata.Interval, limit int) (marketdata.CachedSeries, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT ts, open, high, low, close, volume, source, fetched_at
FROM candles
WHERE symbol = $1 AND interval = $2
ORDER BY ts DESC
LIMIT $3`, sym.String(), string(interval), limit)
	if err != nil {
		return marketdata.CachedSeries{}, fmt.Errorf("postgres: load candles: %w", err)
	}
	defer rows.Close()

	var (
		out      marketdata.CachedSeries
		reversed []marketdata.Candle
		oldest   time.Time
	)
	for rows.Next() {
		var (
			c         marketdata.Candle
			source    string
			fetchedAt time.Time
		)
		if err := rows.Scan(&c.Time, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume,
			&source, &fetchedAt); err != nil {
			return marketdata.CachedSeries{}, fmt.Errorf("postgres: scan candle: %w", err)
		}
		c.Time = c.Time.UTC()
		reversed = append(reversed, c)
		// The newest bar's provenance is the most useful label.
		if out.Source == "" {
			out.Source = source
		}
		if oldest.IsZero() || fetchedAt.Before(oldest) {
			oldest = fetchedAt
		}
	}
	if err := rows.Err(); err != nil {
		return marketdata.CachedSeries{}, fmt.Errorf("postgres: iterate candles: %w", err)
	}
	if len(reversed) == 0 {
		return marketdata.CachedSeries{}, nil
	}

	// The query ordered newest-first so LIMIT took the recent window; flip it
	// back to the oldest-first order every consumer expects.
	out.Candles = make([]marketdata.Candle, len(reversed))
	for i, c := range reversed {
		out.Candles[len(reversed)-1-i] = c
	}
	out.FetchedAt = oldest.UTC()

	if err := d.db.QueryRowContext(ctx, `
SELECT COALESCE((SELECT requested_limit FROM series_coverage WHERE symbol = $1 AND interval = $2), 0)`,
		sym.String(), string(interval)).Scan(&out.RequestedLimit); err != nil {
		return marketdata.CachedSeries{}, fmt.Errorf("postgres: load series coverage: %w", err)
	}
	return out, nil
}

// SaveCandles upserts bars.
//
// Re-fetching a window overwrites the bars it covers, which matters because
// the most recent bar of an in-progress session keeps changing until the close.
func (d *DB) SaveCandles(ctx context.Context, sym marketdata.Symbol, interval marketdata.Interval, source string, bars marketdata.Bars, requestedLimit int) error {
	if len(bars.Candles) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: begin save candles: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO candles (symbol, interval, ts, open, high, low, close, volume, source, fetched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (symbol, interval, ts) DO UPDATE SET
    open = excluded.open, high = excluded.high, low = excluded.low,
    close = excluded.close, volume = excluded.volume,
    source = excluded.source, fetched_at = excluded.fetched_at`)
	if err != nil {
		return fmt.Errorf("postgres: prepare save candles: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, c := range bars.Candles {
		if _, err := stmt.ExecContext(ctx,
			sym.String(), string(interval), c.Time.UTC(),
			c.Open, c.High, c.Low, c.Close, c.Volume, source, now); err != nil {
			return fmt.Errorf("postgres: save candle %s: %w", c.Time, err)
		}
	}

	// Record how deep this fetch reached, keeping the high-water mark: a
	// later shallow request must not shrink what we know we can serve.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO series_coverage (symbol, interval, requested_limit, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (symbol, interval) DO UPDATE SET
    requested_limit = GREATEST(series_coverage.requested_limit, excluded.requested_limit),
    updated_at = excluded.updated_at`,
		sym.String(), string(interval), requestedLimit, now); err != nil {
		return fmt.Errorf("postgres: record series coverage: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres: commit save candles: %w", err)
	}
	return nil
}

// LoadQuote reads the cached quote.
//
// A missing row is not an error; the caller distinguishes "nothing cached" by
// the zero FetchedAt.
func (d *DB) LoadQuote(ctx context.Context, sym marketdata.Symbol) (marketdata.CachedQuote, error) {
	var (
		out       marketdata.CachedQuote
		asOf      sql.NullTime
		fetchedAt time.Time
	)
	err := d.db.QueryRowContext(ctx, `
SELECT price, prev_close, change, change_percent, day_high, day_low, volume,
       currency, as_of, source, fetched_at
FROM quotes WHERE symbol = $1`, sym.String()).Scan(
		&out.Quote.Price, &out.Quote.PrevClose, &out.Quote.Change, &out.Quote.ChangePercent,
		&out.Quote.DayHigh, &out.Quote.DayLow, &out.Quote.Volume, &out.Quote.Currency,
		&asOf, &out.Source, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return marketdata.CachedQuote{}, nil
	}
	if err != nil {
		return marketdata.CachedQuote{}, fmt.Errorf("postgres: load quote: %w", err)
	}
	out.Quote.Symbol = sym
	out.Quote.AsOf = timeOrZero(asOf)
	out.FetchedAt = fetchedAt.UTC()
	return out, nil
}

// SaveQuote upserts the latest quote for a symbol.
func (d *DB) SaveQuote(ctx context.Context, source string, q marketdata.Quote) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO quotes (symbol, price, prev_close, change, change_percent, day_high,
                    day_low, volume, currency, as_of, source, fetched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (symbol) DO UPDATE SET
    price = excluded.price, prev_close = excluded.prev_close, change = excluded.change,
    change_percent = excluded.change_percent, day_high = excluded.day_high,
    day_low = excluded.day_low, volume = excluded.volume, currency = excluded.currency,
    as_of = excluded.as_of, source = excluded.source, fetched_at = excluded.fetched_at`,
		q.Symbol.String(), q.Price, q.PrevClose, q.Change, q.ChangePercent,
		q.DayHigh, q.DayLow, q.Volume, q.Currency,
		nullTime(q.AsOf), source, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("postgres: save quote: %w", err)
	}
	return nil
}
