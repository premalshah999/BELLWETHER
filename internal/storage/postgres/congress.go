package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/tradesys/dashboard/internal/congress"
)

// ExistingCongressFilingDocIDs returns every doc_id already stored for a
// given year, so the daily sync can skip re-fetching and re-parsing a PDF
// it has already read. Loaded in one query per year rather than checked one
// doc_id at a time: a year's index is at most a couple thousand entries, and
// checking each individually against the sync's own list would be one round
// trip per filing for no benefit.
func (d *DB) ExistingCongressFilingDocIDs(ctx context.Context, year int) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT doc_id FROM congress_filings WHERE year = $1`, year)
	if err != nil {
		return nil, fmt.Errorf("existing congress doc ids: %w", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SaveCongressFiling records one PTR filing. Filings are immutable once
// read -- the Clerk does not amend a posted PDF -- so a doc_id already on
// file is left alone rather than overwritten.
func (d *DB) SaveCongressFiling(ctx context.Context, f congress.StoredFiling) error {
	var earliest any
	if !f.EarliestTransactionDate.IsZero() {
		earliest = f.EarliestTransactionDate
	}
	var delay any
	if f.DisclosureDelayDays != nil {
		delay = *f.DisclosureDelayDays
	}
	// stringArray.Value renders a nil slice as SQL NULL, which the column's
	// NOT NULL rejects outright -- and a filing that named no tickers at
	// all (extraction failed, or the PDF matched none) is a real case, not
	// an edge case, so it has to save as an empty array rather than error.
	symbols, unresolved := f.Symbols, f.UnresolvedTickers
	if symbols == nil {
		symbols = []string{}
	}
	if unresolved == nil {
		unresolved = []string{}
	}

	_, err := d.db.ExecContext(ctx, `
		INSERT INTO congress_filings
			(doc_id, chamber, last_name, first_name, state_district, filing_type, filing_date, year,
			 symbols, unresolved_tickers, earliest_transaction_date, disclosure_delay_days, discovered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (doc_id) DO NOTHING`,
		f.DocID, f.Chamber, f.Last, f.First, f.StateDistrict, f.FilingType, f.FilingDate, f.Year,
		stringArray(symbols), stringArray(unresolved), earliest, delay, f.DiscoveredAt,
	)
	if err != nil {
		return fmt.Errorf("save congress filing %s: %w", f.DocID, err)
	}
	return nil
}

// CongressFilingFilter narrows a filing listing. Symbol matches against the
// resolved array with Postgres's own containment operator; Limit defaults
// to 100 and is capped at 500 so the page this serves cannot request an
// unbounded scan.
type CongressFilingFilter struct {
	Symbol string
	Limit  int
}

// ListCongressFilings returns filings newest-filed-first.
func (d *DB) ListCongressFilings(ctx context.Context, f CongressFilingFilter) ([]congress.StoredFiling, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	query := `SELECT doc_id, chamber, last_name, first_name, state_district, filing_type, filing_date, year,
	                 symbols, unresolved_tickers, earliest_transaction_date, disclosure_delay_days, discovered_at
	          FROM congress_filings`
	args := []any{}
	if f.Symbol != "" {
		query += ` WHERE symbols @> ARRAY[$1]::TEXT[]`
		args = append(args, f.Symbol)
	}
	query += fmt.Sprintf(` ORDER BY filing_date DESC, doc_id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list congress filings: %w", err)
	}
	defer rows.Close()

	var out []congress.StoredFiling
	for rows.Next() {
		var (
			row      congress.StoredFiling
			earliest sql.NullTime
			delay    sql.NullInt64
		)
		if err := rows.Scan(
			&row.DocID, &row.Chamber, &row.Last, &row.First, &row.StateDistrict, &row.FilingType, &row.FilingDate, &row.Year,
			(*stringArray)(&row.Symbols), (*stringArray)(&row.UnresolvedTickers), &earliest, &delay, &row.DiscoveredAt,
		); err != nil {
			return nil, err
		}
		if earliest.Valid {
			row.EarliestTransactionDate = earliest.Time
		}
		if delay.Valid {
			d := int(delay.Int64)
			row.DisclosureDelayDays = &d
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
