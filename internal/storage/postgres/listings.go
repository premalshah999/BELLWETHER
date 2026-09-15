package postgres

import (
	"context"
	"database/sql"
)

// ResolveTicker looks up a bare ticker against the full listed universe and
// reports the single venue it belongs to.
//
// ok is false both when the ticker is unknown and when it is ambiguous (ABB,
// INFY: a different instrument on each venue) -- the caller cannot tell those
// two apart from ok alone, which is deliberate. A caller guessing at either
// one is the mistake this exists to prevent; the two cases the migration's
// own qualify() treats differently (an explicit watchlist add, where a
// US-first default is a reasonable fallback) are not this caller's case.
func (d *DB) ResolveTicker(ctx context.Context, ticker string) (venue string, ok bool, err error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT DISTINCT venue FROM listings WHERE ticker = upper($1)`, ticker)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()

	var venues []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", false, err
		}
		venues = append(venues, v)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(venues) != 1 {
		return "", false, nil
	}
	return venues[0], true, nil
}

// ResolveUSTicker validates a ticker against the US listed universe and
// returns its canonical symbol -- which for US is the ticker itself -- when
// it is a real, active listing.
//
// Unlike ResolveTicker, this does not need to worry about cross-venue
// ambiguity (ABB, INFY): its callers already know the ticker is a US
// instrument by context (a congressional PTR filing, for instance, can name
// nothing else), so the only question worth asking here is real-vs-noise --
// whether a regex match over a noisy source is an actual listed symbol.
func (d *DB) ResolveUSTicker(ctx context.Context, ticker string) (symbol string, ok bool, err error) {
	var sym string
	err = d.db.QueryRowContext(ctx,
		`SELECT symbol FROM listings WHERE ticker = upper($1) AND venue = 'US' AND active LIMIT 1`,
		ticker,
	).Scan(&sym)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return sym, true, nil
}
