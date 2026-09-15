package postgres

import (
	"context"
	"fmt"
)

// ReplaceIndexConstituents sets the instruments that count as index members.
//
// Replaced wholesale inside a transaction rather than upserted, because index
// membership genuinely changes: a company that leaves the index should stop
// being treated as one the moment the list says so, and an upsert would leave
// it behind forever.
func (d *DB) ReplaceIndexConstituents(ctx context.Context, byIndustry map[string]string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace constituents: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM index_constituents`); err != nil {
		return fmt.Errorf("replace constituents: clear: %w", err)
	}
	for symbol, industry := range byIndustry {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO index_constituents (symbol, industry) VALUES ($1, $2)
			 ON CONFLICT (symbol) DO UPDATE SET industry = EXCLUDED.industry, updated_at = now()`,
			symbol, industry); err != nil {
			return fmt.Errorf("replace constituents: %s: %w", symbol, err)
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
