package postgres

import "context"

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
