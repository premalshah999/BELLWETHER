package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/scanner"
)

// SaveScan records one scan and its findings.
//
// Written in a single transaction because a scan with half its findings would
// misrepresent the market at that moment, and the whole point of keeping
// history is to be able to trust what a past scan says.
func (d *DB) SaveScan(ctx context.Context, res scanner.Result) (int64, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("save scan: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var scanID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO scans (as_of, universe, scanned, failed, elapsed)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		res.AsOf, res.Universe, res.Scanned, res.Failed, res.Elapsed,
	).Scan(&scanID)
	if err != nil {
		return 0, fmt.Errorf("save scan: insert: %w", err)
	}

	for _, f := range res.Findings {
		signals := make([]string, 0, len(f.Signals))
		for _, sig := range f.Signals {
			signals = append(signals, string(sig))
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO scan_findings (
				scan_id, symbol, signals, score,
				close_price, return_1d, return_5d, return_z,
				volume, volume_ratio, volume_z, gap_percent,
				pct_from_52w_high, pct_from_52w_low, bars, explained, explained_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,
			        CASE WHEN $16::BOOLEAN IS NULL THEN NULL ELSE now() END)`,
			scanID, f.Symbol, stringArray(signals), f.Score,
			f.Close, f.Return1D, f.Return5D, f.ReturnZ,
			f.Volume, f.VolumeRatio, f.VolumeZ, f.GapPercent,
			f.PctFrom52WHigh, f.PctFrom52WLow, f.Bars, f.Explained,
		)
		if err != nil {
			return 0, fmt.Errorf("save scan: finding %s: %w", f.Symbol, err)
		}
	}
	if err := insertScanMetrics(ctx, tx, scanID, res.All); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("save scan: commit: %w", err)
	}
	return scanID, nil
}

// metricsPerInsert is how many instruments go into one INSERT.
//
// At 13 columns each this is 1,300 parameters per statement, comfortably under
// Postgres' 65,535 limit, and it turns a 750-round-trip loop into six.
const metricsPerInsert = 100

// insertScanMetrics writes the full sweep.
//
// Separate from the findings loop above because the two have genuinely
// different shapes: findings are few and carry signals and a score, metrics
// are the whole universe and carry neither.
func insertScanMetrics(ctx context.Context, tx *sql.Tx, scanID int64, all []scanner.Metrics) error {
	const cols = 13
	for start := 0; start < len(all); start += metricsPerInsert {
		end := start + metricsPerInsert
		if end > len(all) {
			end = len(all)
		}
		batch := all[start:end]

		var (
			b    strings.Builder
			args = make([]any, 0, len(batch)*cols)
		)
		b.WriteString(`INSERT INTO scan_metrics (
			scan_id, symbol, close_price, return_1d, return_5d, return_z,
			volume, volume_ratio, volume_z, gap_percent,
			pct_from_52w_high, pct_from_52w_low, bars) VALUES `)
		for i, m := range batch {
			if i > 0 {
				b.WriteString(",")
			}
			n := i * cols
			fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10, n+11, n+12, n+13)
			args = append(args,
				scanID, m.Symbol, m.Close, m.Return1D, m.Return5D, m.ReturnZ,
				m.Volume, m.VolumeRatio, m.VolumeZ, m.GapPercent,
				m.PctFrom52WHigh, m.PctFrom52WLow, m.Bars)
		}
		// A scan can measure the same ticker twice if the constituent file
		// lists it under two series. Last write wins rather than aborting a
		// 750-instrument transaction over a duplicate row.
		b.WriteString(" ON CONFLICT (scan_id, symbol) DO NOTHING")

		if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
			return fmt.Errorf("save scan: metrics %d-%d: %w", start, end, err)
		}
	}
	return nil
}

// StoredFinding is a finding as it came back from the archive.
type StoredFinding struct {
	ID        int64     `json:"id"`
	ScanID    int64     `json:"scan_id"`
	AsOf      time.Time `json:"as_of"`
	ScannedAt time.Time `json:"scanned_at"`
	scanner.Finding
	Explained   *bool      `json:"explained,omitempty"`
	ExplainedAt *time.Time `json:"explained_at,omitempty"`
}

// LatestScan returns the most recent scan's findings.
func (d *DB) LatestScan(ctx context.Context, limit int) ([]StoredFinding, error) {
	if limit <= 0 {
		limit = 40
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT f.id, f.scan_id, s.as_of, s.scanned_at, f.symbol, f.signals, f.score,
		       f.close_price, f.return_1d, f.return_5d, f.return_z,
		       f.volume, f.volume_ratio, f.volume_z, f.gap_percent,
		       f.pct_from_52w_high, f.pct_from_52w_low, f.bars,
		       f.explained, f.explained_at
		FROM scan_findings f
		JOIN scans s ON s.id = f.scan_id
		WHERE f.scan_id = (SELECT id FROM scans ORDER BY scanned_at DESC LIMIT 1)
		ORDER BY f.score DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("latest scan: %w", err)
	}
	defer rows.Close()
	return scanFindingRows(rows)
}

// SymbolScanHistory returns what the scanner has said about one instrument.
//
// This is the query that makes the archive worth keeping: it answers whether
// the volume showed up before the announcement did.
func (d *DB) SymbolScanHistory(ctx context.Context, symbol string, limit int) ([]StoredFinding, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT f.id, f.scan_id, s.as_of, s.scanned_at, f.symbol, f.signals, f.score,
		       f.close_price, f.return_1d, f.return_5d, f.return_z,
		       f.volume, f.volume_ratio, f.volume_z, f.gap_percent,
		       f.pct_from_52w_high, f.pct_from_52w_low, f.bars,
		       f.explained, f.explained_at
		FROM scan_findings f
		JOIN scans s ON s.id = f.scan_id
		WHERE f.symbol = $1
		ORDER BY f.id DESC
		LIMIT $2`, symbol, limit)
	if err != nil {
		return nil, fmt.Errorf("symbol scan history: %w", err)
	}
	defer rows.Close()
	return scanFindingRows(rows)
}

func scanFindingRows(rows *sql.Rows) ([]StoredFinding, error) {
	var out []StoredFinding
	for rows.Next() {
		var f StoredFinding
		var signals stringArray
		var explained sql.NullBool
		var explainedAt sql.NullTime
		if err := rows.Scan(
			&f.ID, &f.ScanID, &f.AsOf, &f.ScannedAt, &f.Symbol, &signals, &f.Score,
			&f.Close, &f.Return1D, &f.Return5D, &f.ReturnZ,
			&f.Volume, &f.VolumeRatio, &f.VolumeZ, &f.GapPercent,
			&f.PctFrom52WHigh, &f.PctFrom52WLow, &f.Bars,
			&explained, &explainedAt,
		); err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		for _, s := range signals {
			f.Signals = append(f.Signals, scanner.Signal(s))
		}
		if explained.Valid {
			v := explained.Bool
			f.Explained = &v
		}
		if explainedAt.Valid {
			t := explainedAt.Time
			f.ExplainedAt = &t
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ExplainsMove reports whether the archive already accounts for unusual
// trading in a symbol.
//
// The bar is deliberately higher than "an article mentions this company".
// Routine coverage exists for every large name every day and would mark
// everything explained, which would silently disable the scanner's most
// useful output. What counts is a material event: an exchange filing, or
// something the classifier rated as consequential.
func (d *DB) ExplainsMove(ctx context.Context, symbol string, since time.Time) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1
			FROM events e
			JOIN event_entities en ON en.event_id = e.id
			WHERE en.symbol = $1
			  AND e.discovered_at >= $2
			  AND (e.official OR e.importance >= 6)
		)`
	var ok bool
	if err := d.db.QueryRowContext(ctx, q, symbol, since).Scan(&ok); err != nil {
		return false, fmt.Errorf("explains move: %w", err)
	}
	return ok, nil
}
