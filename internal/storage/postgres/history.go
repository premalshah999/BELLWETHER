package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// SaveDailyHistory stores deep daily bars for a symbol, keeping only those
// older than the candles table already holds: the two tables meet rather
// than overlap.
func (d *DB) SaveDailyHistory(ctx context.Context, symbol string, bars []marketdata.Candle) (int, error) {
	var first *time.Time
	if err := d.db.QueryRowContext(ctx, `SELECT min(ts) FROM candles WHERE symbol = $1 AND interval = '1d'`, symbol).Scan(&first); err != nil {
		return 0, fmt.Errorf("postgres: history bound: %w", err)
	}
	var ts []time.Time
	var o, h, l, c, v []float64
	for _, b := range bars {
		if first != nil && !b.Time.Before(*first) {
			continue
		}
		if b.Close <= 0 {
			continue
		}
		ts, o, h, l, c, v = append(ts, b.Time), append(o, b.Open), append(h, b.High), append(l, b.Low), append(c, b.Close), append(v, b.Volume)
	}
	if len(ts) == 0 {
		return 0, nil
	}
	res, err := d.db.ExecContext(ctx, `
INSERT INTO daily_history (symbol, ts, open, high, low, close, volume)
SELECT $1, * FROM unnest($2::timestamptz[], $3::real[], $4::real[], $5::real[], $6::real[], $7::real[])
ON CONFLICT DO NOTHING`, symbol, ts, o, h, l, c, v)
	if err != nil {
		return 0, fmt.Errorf("postgres: save history: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// LoadDailyHistory returns a symbol's deep bars, oldest first.
func (d *DB) LoadDailyHistory(ctx context.Context, symbol string) ([]marketdata.Candle, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT ts, open, high, low, close, volume FROM daily_history WHERE symbol = $1 ORDER BY ts`, symbol)
	if err != nil {
		return nil, fmt.Errorf("postgres: load history: %w", err)
	}
	defer rows.Close()
	var out []marketdata.Candle
	for rows.Next() {
		var c marketdata.Candle
		var o, h, l, cl, v float32
		if err := rows.Scan(&c.Time, &o, &h, &l, &cl, &v); err != nil {
			return nil, err
		}
		c.Open, c.High, c.Low, c.Close, c.Volume = float64(o), float64(h), float64(l), float64(cl), float64(v)
		out = append(out, c)
	}
	return out, rows.Err()
}
