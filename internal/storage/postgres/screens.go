package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/screens"
	"github.com/tradesys/dashboard/internal/storage"
)

// Screen is a saved screen as stored.
type Screen struct {
	ID          int64              `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Definition  screens.Definition `json:"definition"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	LastRunAt   *time.Time         `json:"last_run_at,omitempty"`
}

// ScreenRow is one instrument that matched.
//
// Industry comes from the constituent file and is frequently blank for
// recently listed names, so it is a plain string rather than a pointer: the
// interface shows nothing either way, and a nil check at every call site buys
// no information.
type ScreenRow struct {
	Symbol         string  `json:"symbol"`
	Industry       string  `json:"industry"`
	Close          float64 `json:"close"`
	Return1D       float64 `json:"return_1d"`
	Return5D       float64 `json:"return_5d"`
	ReturnZ        float64 `json:"return_z"`
	Volume         float64 `json:"volume"`
	VolumeRatio    float64 `json:"volume_ratio"`
	VolumeZ        float64 `json:"volume_z"`
	GapPercent     float64 `json:"gap_percent"`
	PctFrom52WHigh float64 `json:"pct_from_52w_high"`
	PctFrom52WLow  float64 `json:"pct_from_52w_low"`
	Bars           int     `json:"bars"`
	// Signals is what the built-in scanner made of the same instrument, so a
	// screen can show where its results overlap the standing scan.
	Signals []string `json:"signals"`
}

// ScreenResult is one run.
type ScreenResult struct {
	Rows []ScreenRow `json:"rows"`
	// Universe is how many instruments the newest scan measured, so a screen
	// that returns four rows can say four out of what.
	Universe int       `json:"universe"`
	ScanAsOf time.Time `json:"scan_as_of"`
	Elapsed  string    `json:"elapsed"`
}

// RunScreen evaluates a definition against the newest scan.
//
// Against the stored sweep rather than by calling the price service: a screen
// is a question about a moment that has already been measured, and re-fetching
// 750 instruments to answer it would take a hundred seconds and give a
// different answer than the Scanner page is showing.
func (d *DB) RunScreen(ctx context.Context, def screens.Definition) (ScreenResult, error) {
	start := time.Now()
	var out ScreenResult

	err := d.db.QueryRowContext(ctx, `
		SELECT s.as_of, count(m.symbol)
		FROM scans s LEFT JOIN scan_metrics m ON m.scan_id = s.id
		WHERE s.id = (SELECT id FROM scans ORDER BY scanned_at DESC LIMIT 1)
		GROUP BY s.as_of`).Scan(&out.ScanAsOf, &out.Universe)
	if errors.Is(err, sql.ErrNoRows) {
		// No scan has run yet. An empty result with a zero universe is the
		// honest answer and lets the interface say so; an error would read as
		// though the screen itself were broken.
		out.Rows = []ScreenRow{}
		out.Elapsed = time.Since(start).Round(time.Millisecond).String()
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("run screen: scan: %w", err)
	}

	q, args := def.SQL()
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return out, fmt.Errorf("run screen: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out.Rows = []ScreenRow{}
	for rows.Next() {
		var (
			r        ScreenRow
			industry sql.NullString
			signals  stringArray
		)
		if err := rows.Scan(&r.Symbol, &industry,
			&r.Close, &r.Return1D, &r.Return5D, &r.ReturnZ,
			&r.Volume, &r.VolumeRatio, &r.VolumeZ, &r.GapPercent,
			&r.PctFrom52WHigh, &r.PctFrom52WLow, &r.Bars, &signals); err != nil {
			return out, fmt.Errorf("run screen: scan row: %w", err)
		}
		r.Industry = industry.String
		r.Signals = signals
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("run screen: rows: %w", err)
	}
	out.Elapsed = time.Since(start).Round(time.Millisecond).String()
	return out, nil
}

// Screens lists every saved screen, newest first.
func (d *DB) Screens(ctx context.Context) ([]Screen, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, name, description, definition, created_at, updated_at, last_run_at
		FROM screens ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("screens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Screen{}
	for rows.Next() {
		s, err := scanScreen(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CreateScreen saves a new screen.
func (d *DB) CreateScreen(ctx context.Context, name, description string, def screens.Definition) (Screen, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return Screen{}, fmt.Errorf("create screen: encode: %w", err)
	}
	row := d.db.QueryRowContext(ctx, `
		INSERT INTO screens (name, description, definition)
		VALUES ($1, $2, $3)
		RETURNING id, name, description, definition, created_at, updated_at, last_run_at`,
		name, description, raw)
	s, err := scanScreen(row)
	if err != nil {
		return Screen{}, fmt.Errorf("create screen: %w", err)
	}
	return s, nil
}

// UpdateScreen replaces one.
func (d *DB) UpdateScreen(ctx context.Context, id int64, name, description string, def screens.Definition) (Screen, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return Screen{}, fmt.Errorf("update screen: encode: %w", err)
	}
	row := d.db.QueryRowContext(ctx, `
		UPDATE screens SET name = $2, description = $3, definition = $4, updated_at = now()
		WHERE id = $1
		RETURNING id, name, description, definition, created_at, updated_at, last_run_at`,
		id, name, description, raw)
	s, err := scanScreen(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Screen{}, storage.ErrNotFound
	}
	return s, err
}

// DeleteScreen removes one.
func (d *DB) DeleteScreen(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM screens WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete screen: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func scanScreen(r rowScanner) (Screen, error) {
	var (
		s   Screen
		raw []byte
		run sql.NullTime
	)
	if err := r.Scan(&s.ID, &s.Name, &s.Description, &raw,
		&s.CreatedAt, &s.UpdatedAt, &run); err != nil {
		return Screen{}, err
	}
	if err := json.Unmarshal(raw, &s.Definition); err != nil {
		return Screen{}, fmt.Errorf("screen %d: decode definition: %w", s.ID, err)
	}
	if run.Valid {
		s.LastRunAt = &run.Time
	}
	return s, nil
}
