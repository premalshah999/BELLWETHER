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

// SectorsFor reports each symbol's industry, already normalised to the same
// spelling InferSectors and SectorsForAgency use ("US: " prefixed for a
// GICS/US row, unprefixed NSE industry name otherwise) -- so a caller can
// compare an event's Sectors directly against what it gets back here without
// separately knowing or re-deriving each symbol's venue.
//
// Symbols absent from index_constituents (a custom watchlist addition
// outside the scan universe) are simply missing from the result rather
// than erroring: an unknown industry is not a fetch failure.
func (d *DB) SectorsFor(ctx context.Context, symbols []string) (map[string]string, error) {
	if len(symbols) == 0 {
		return map[string]string{}, nil
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT symbol, industry, venue FROM index_constituents
		 WHERE symbol = ANY($1::text[]) AND industry IS NOT NULL AND industry <> ''`,
		symbols)
	if err != nil {
		return nil, fmt.Errorf("sectors for: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string, len(symbols))
	for rows.Next() {
		var symbol, industry, venue string
		if err := rows.Scan(&symbol, &industry, &venue); err != nil {
			return nil, fmt.Errorf("sectors for: scan: %w", err)
		}
		if venue == "US" {
			industry = "US: " + industry
		}
		out[symbol] = industry
	}
	return out, rows.Err()
}
