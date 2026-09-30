package postgres

import (
	"context"
	"fmt"
)

// ConstituentListing is one scan-universe member and its GICS sector.
type ConstituentListing struct {
	Symbol   string
	Industry string
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
			`INSERT INTO index_constituents (symbol, industry, venue, taxonomy) VALUES ($1, $2, 'US', 'gics')
			 ON CONFLICT (symbol) DO UPDATE SET industry = EXCLUDED.industry, updated_at = now()`,
			l.Symbol, l.Industry); err != nil {
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

// SectorsFor reports each symbol's sector in the spelling events carry
// ("US: Energy"), so the two compare directly. Symbols outside the scan
// universe are simply absent.
func (d *DB) SectorsFor(ctx context.Context, symbols []string) (map[string]string, error) {
	if len(symbols) == 0 {
		return map[string]string{}, nil
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT symbol, industry FROM index_constituents
		 WHERE symbol = ANY($1::text[]) AND industry IS NOT NULL AND industry <> ''`,
		symbols)
	if err != nil {
		return nil, fmt.Errorf("sectors for: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string, len(symbols))
	for rows.Next() {
		var symbol, industry string
		if err := rows.Scan(&symbol, &industry); err != nil {
			return nil, fmt.Errorf("sectors for: scan: %w", err)
		}
		out[symbol] = "US: " + industry
	}
	return out, rows.Err()
}
