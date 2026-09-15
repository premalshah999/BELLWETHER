package postgres

import (
	"context"
	"fmt"
)

// ConstituentListing is one instrument to record as a scan-universe member.
// Venue and taxonomy travel with it because index_constituents now spans
// both venues: an NSE row's industry comes from the NSE classification
// (taxonomy "nse-industry") and a US row's from GICS (taxonomy "gics"), and
// the two must never be compared as if they were the same scale.
type ConstituentListing struct {
	Symbol   string // canonical: RELIANCE.NSE, AAPL
	Industry string
	Venue    string // "NSE" | "US"
	Taxonomy string // "nse-industry" | "gics"
}

// ReplaceIndexConstituents sets the instruments that count as index members.
//
// Replaced wholesale inside a transaction rather than upserted, because index
// membership genuinely changes: a company that leaves the index should stop
// being treated as one the moment the list says so, and an upsert would leave
// it behind forever.
func (d *DB) ReplaceIndexConstituents(ctx context.Context, listings []ConstituentListing) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace constituents: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM index_constituents`); err != nil {
		return fmt.Errorf("replace constituents: clear: %w", err)
	}
	for _, l := range listings {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO index_constituents (symbol, industry, venue, taxonomy) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (symbol) DO UPDATE SET
			     industry = EXCLUDED.industry, venue = EXCLUDED.venue,
			     taxonomy = EXCLUDED.taxonomy, updated_at = now()`,
			l.Symbol, l.Industry, l.Venue, l.Taxonomy); err != nil {
			return fmt.Errorf("replace constituents: %s: %w", l.Symbol, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace constituents: commit: %w", err)
	}
	return nil
}

// CountIndexConstituents reports how many instruments are on the list.
func (d *DB) CountIndexConstituents(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM index_constituents`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count constituents: %w", err)
	}
	return n, nil
}
