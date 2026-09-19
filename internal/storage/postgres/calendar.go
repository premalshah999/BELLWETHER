package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// CalendarEntry is one symbol's next scheduled corporate events.
type CalendarEntry struct {
	Symbol         marketdata.Symbol `json:"-"`
	EarningsDate   *time.Time        `json:"earnings_date,omitempty"`
	ExDividendDate *time.Time        `json:"ex_dividend_date,omitempty"`
	DividendDate   *time.Time        `json:"dividend_date,omitempty"`
	EPSLow         *float64          `json:"eps_low,omitempty"`
	EPSHigh        *float64          `json:"eps_high,omitempty"`
	EPSAverage     *float64          `json:"eps_average,omitempty"`
	RefreshedAt    time.Time         `json:"refreshed_at"`
}

// SaveCalendar replaces the calendar for the symbols given.
//
// Upsert rather than delete-and-insert: a refresh that fails halfway should
// leave yesterday's dates in place, not an empty calendar. A date that has
// genuinely gone away is overwritten with NULL by the same statement.
func (d *DB) SaveCalendar(ctx context.Context, entries []CalendarEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("postgres: save calendar: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
        INSERT INTO catalyst_calendar
            (symbol, earnings_date, ex_dividend_date, dividend_date,
             eps_low, eps_high, eps_average, refreshed_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, now())
        ON CONFLICT (symbol) DO UPDATE SET
            earnings_date    = EXCLUDED.earnings_date,
            ex_dividend_date = EXCLUDED.ex_dividend_date,
            dividend_date    = EXCLUDED.dividend_date,
            eps_low          = EXCLUDED.eps_low,
            eps_high         = EXCLUDED.eps_high,
            eps_average      = EXCLUDED.eps_average,
            refreshed_at     = now()`)
	if err != nil {
		return 0, fmt.Errorf("postgres: prepare calendar upsert: %w", err)
	}
	defer stmt.Close()

	n := 0
	for _, e := range entries {
		if e.EarningsDate == nil && e.ExDividendDate == nil && e.DividendDate == nil {
			continue // the CHECK would reject it; skipping says so in Go too
		}
		if _, err := stmt.ExecContext(ctx, e.Symbol.String(),
			e.EarningsDate, e.ExDividendDate, e.DividendDate,
			e.EPSLow, e.EPSHigh, e.EPSAverage); err != nil {
			return 0, fmt.Errorf("postgres: upsert calendar %s: %w", e.Symbol, err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("postgres: commit calendar: %w", err)
	}
	return n, nil
}

// PruneCalendar drops rows whose every date has passed.
//
// Nothing else removes them: the refresh only rewrites symbols it was asked
// about, so a delisted name would otherwise keep a stale date forever.
func (d *DB) PruneCalendar(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
        DELETE FROM catalyst_calendar
        WHERE COALESCE(earnings_date,    '-infinity'::date) < CURRENT_DATE
          AND COALESCE(ex_dividend_date, '-infinity'::date) < CURRENT_DATE
          AND COALESCE(dividend_date,    '-infinity'::date) < CURRENT_DATE`)
	if err != nil {
		return 0, fmt.Errorf("postgres: prune calendar: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpcomingCatalyst is a calendar row joined to what the archive knows about
// the company it belongs to.
type UpcomingCatalyst struct {
	CalendarEntry
	CanonicalSymbol string `json:"symbol"`
	Industry        string `json:"industry,omitempty"`
	Venue           string `json:"venue,omitempty"`
	// DaysAway is counted from the query's own date, so a client does not
	// have to agree with the server about what "today" is.
	EarningsInDays *int `json:"earnings_in_days,omitempty"`

	// NextKind and NextDate name the soonest of this row's dates.
	//
	// Without them a horizon reads as wrong when it is not: a row can enter
	// a fourteen-day window on its ex-dividend date while its earnings date
	// is two months out, and a list that shows only the earnings date then
	// looks like it ignored the filter. Naming the date that actually
	// qualified is the difference between a list you trust and one you check.
	NextKind string     `json:"next_kind,omitempty"`
	NextDate *time.Time `json:"next_date,omitempty"`
	NextDays *int       `json:"next_in_days,omitempty"`
}

// UpcomingCatalysts returns scheduled events inside a horizon, soonest first.
//
// Restricted to symbols when one is given, which is how the watchlist view
// asks for "what is coming up for what I hold".
func (d *DB) UpcomingCatalysts(ctx context.Context, within time.Duration, symbols []string, limit int) ([]UpcomingCatalyst, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	horizon := int(within / (24 * time.Hour))
	if horizon <= 0 {
		horizon = 30
	}

	rows, err := d.db.QueryContext(ctx, `
        SELECT c.symbol, c.earnings_date, c.ex_dividend_date, c.dividend_date,
               c.eps_low, c.eps_high, c.eps_average, c.refreshed_at,
               COALESCE(ic.industry, ''), COALESCE(ic.venue, '')
        FROM catalyst_calendar c
        LEFT JOIN index_constituents ic ON ic.symbol = c.symbol
        WHERE (
            (c.earnings_date    IS NOT NULL AND c.earnings_date    BETWEEN CURRENT_DATE AND CURRENT_DATE + $1::int)
         OR (c.ex_dividend_date IS NOT NULL AND c.ex_dividend_date BETWEEN CURRENT_DATE AND CURRENT_DATE + $1::int)
         OR (c.dividend_date    IS NOT NULL AND c.dividend_date    BETWEEN CURRENT_DATE AND CURRENT_DATE + $1::int)
        )
          AND ($2::text[] IS NULL OR c.symbol = ANY($2::text[]))
        ORDER BY LEAST(
            COALESCE(c.earnings_date,    'infinity'::date),
            COALESCE(c.ex_dividend_date, 'infinity'::date),
            COALESCE(c.dividend_date,    'infinity'::date)
        ) ASC, c.symbol ASC
        LIMIT $3`, horizon, nullableArray(symbols), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: upcoming catalysts: %w", err)
	}
	defer rows.Close()

	var out []UpcomingCatalyst
	now := time.Now().UTC()
	for rows.Next() {
		var (
			c              UpcomingCatalyst
			earn, exd, div sql.NullTime
			low, high, avg sql.NullFloat64
		)
		if err := rows.Scan(&c.CanonicalSymbol, &earn, &exd, &div,
			&low, &high, &avg, &c.RefreshedAt, &c.Industry, &c.Venue); err != nil {
			return nil, fmt.Errorf("postgres: scan catalyst: %w", err)
		}
		if earn.Valid {
			t := earn.Time
			c.EarningsDate = &t
			days := int(t.Sub(now).Hours() / 24)
			if days < 0 {
				days = 0
			}
			c.EarningsInDays = &days
		}
		if exd.Valid {
			t := exd.Time
			c.ExDividendDate = &t
		}
		if div.Valid {
			t := div.Time
			c.DividendDate = &t
		}
		if low.Valid {
			c.EPSLow = &low.Float64
		}
		if high.Valid {
			c.EPSHigh = &high.Float64
		}
		if avg.Valid {
			c.EPSAverage = &avg.Float64
		}

		// Derived here from the values just scanned rather than as a second
		// SQL expression, so it cannot disagree with the ORDER BY above.
		for _, cand := range []struct {
			kind string
			at   *time.Time
		}{
			{"earnings", c.EarningsDate},
			{"ex-dividend", c.ExDividendDate},
			{"dividend", c.DividendDate},
		} {
			if cand.at == nil {
				continue
			}
			if c.NextDate == nil || cand.at.Before(*c.NextDate) {
				c.NextKind, c.NextDate = cand.kind, cand.at
			}
		}
		if c.NextDate != nil {
			days := int(c.NextDate.Sub(now).Hours() / 24)
			if days < 0 {
				days = 0
			}
			c.NextDays = &days
		}

		out = append(out, c)
	}
	return out, rows.Err()
}

// nullableArray renders an empty filter as SQL NULL, so one query serves both
// "these symbols" and "everything" without building the statement twice.
func nullableArray(v []string) any {
	if len(v) == 0 {
		return nil
	}
	return stringArray(v)
}
