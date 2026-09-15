package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

// MaxWatchlists caps how many lists may exist.
//
// Enforced here rather than in the schema so the limit can be explained to a
// person instead of surfacing as a constraint violation. Each list is polled
// live during market hours, so the ceiling is about upstream load, not storage.
const MaxWatchlists = 5

// Watchlist is a named set of instruments.
type Watchlist struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Position int             `json:"position"`
	Items    []WatchlistItem `json:"items,omitempty"`
	Count    int             `json:"count"`
}

// WatchlistItem is one instrument on a list.
type WatchlistItem struct {
	Symbol string `json:"symbol"`
	Note   string `json:"note,omitempty"`
}

// Watchlists returns every list with its instrument count.
func (d *DB) Watchlists(ctx context.Context) ([]Watchlist, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT w.id, w.name, w.position, count(i.symbol)
		FROM watchlists w
		LEFT JOIN watchlist_items i ON i.watchlist_id = w.id
		GROUP BY w.id, w.name, w.position
		ORDER BY w.position, w.id`)
	if err != nil {
		return nil, fmt.Errorf("watchlists: %w", err)
	}
	defer rows.Close()

	var out []Watchlist
	for rows.Next() {
		var w Watchlist
		if err := rows.Scan(&w.ID, &w.Name, &w.Position, &w.Count); err != nil {
			return nil, fmt.Errorf("watchlists: scan: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WatchlistSymbols returns the instruments on one list, in order.
func (d *DB) WatchlistSymbols(ctx context.Context, id int64) ([]marketdata.Symbol, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT symbol FROM watchlist_items WHERE watchlist_id = $1 ORDER BY position, symbol`, id)
	if err != nil {
		return nil, fmt.Errorf("watchlist symbols: %w", err)
	}
	defer rows.Close()

	var out []marketdata.Symbol
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		// A symbol that no longer parses is skipped rather than failing the
		// list: one bad row should not empty somebody's watchlist.
		if sym, err := marketdata.ParseSymbol(raw); err == nil {
			out = append(out, sym)
		}
	}
	return out, rows.Err()
}

// AllWatchedSymbols is the union across every list, deduplicated.
//
// What the price stream and the ingestion engine want: an instrument on three
// lists is still one instrument to poll.
func (d *DB) AllWatchedSymbols(ctx context.Context) ([]marketdata.Symbol, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT DISTINCT symbol FROM watchlist_items ORDER BY symbol`)
	if err != nil {
		return nil, fmt.Errorf("all watched symbols: %w", err)
	}
	defer rows.Close()

	var out []marketdata.Symbol
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if sym, err := marketdata.ParseSymbol(raw); err == nil {
			out = append(out, sym)
		}
	}
	return out, rows.Err()
}

// CreateWatchlist adds a list, refusing to exceed the cap.
func (d *DB) CreateWatchlist(ctx context.Context, name string) (Watchlist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Watchlist{}, fmt.Errorf("a watchlist needs a name")
	}

	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM watchlists`).Scan(&n); err != nil {
		return Watchlist{}, fmt.Errorf("create watchlist: %w", err)
	}
	if n >= MaxWatchlists {
		return Watchlist{}, fmt.Errorf(
			"there are already %d watchlists, which is the limit; rename or delete one first", n)
	}

	var w Watchlist
	err := d.db.QueryRowContext(ctx, `
		INSERT INTO watchlists (name, position)
		VALUES ($1, COALESCE((SELECT max(position) + 1 FROM watchlists), 0))
		RETURNING id, name, position`, name).Scan(&w.ID, &w.Name, &w.Position)
	if err != nil {
		return Watchlist{}, fmt.Errorf("create watchlist: %w", err)
	}
	return w, nil
}

// RenameWatchlist changes a list's name.
func (d *DB) RenameWatchlist(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("a watchlist needs a name")
	}
	res, err := d.db.ExecContext(ctx, `UPDATE watchlists SET name = $2 WHERE id = $1`, id, name)
	if err != nil {
		return fmt.Errorf("rename watchlist: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no watchlist with id %d", id)
	}
	return nil
}

// DeleteWatchlist removes a list and its items.
//
// The last list cannot be deleted: an application with nowhere to put an
// instrument has no usable state, and "delete everything then wonder why
// nothing works" is not a state worth allowing.
func (d *DB) DeleteWatchlist(ctx context.Context, id int64) error {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM watchlists`).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return fmt.Errorf("this is the only watchlist; rename it or empty it instead")
	}
	res, err := d.db.ExecContext(ctx, `DELETE FROM watchlists WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete watchlist: %w", err)
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return fmt.Errorf("no watchlist with id %d", id)
	}
	return nil
}

// AddToWatchlist adds instruments, skipping ones already present.
//
// Returns three lists: what was added, what was already present, and what was
// not a ticker. A bulk paste of forty lines can then report the three that
// were not recognised rather than failing as a whole — a partial success is
// the normal outcome here — and "already on the list" never reads as an error.
func (d *DB) AddToWatchlist(ctx context.Context, id int64, symbols []string) (added, skipped, rejected []string, err error) {
	// Empty rather than nil, because these three are reported to the browser
	// as JSON and a nil slice marshals to null, not []. The interface reads
	// `result.skipped.length` to decide what to say, and null threw there —
	// an add that had worked perfectly looked like a crash.
	added, skipped, rejected = []string{}, []string{}, []string{}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("add to watchlist: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, raw := range symbols {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		raw = d.qualify(ctx, raw)
		sym, perr := marketdata.ParseSymbol(raw)
		if perr != nil {
			rejected = append(rejected, raw)
			continue
		}
		res, xerr := tx.ExecContext(ctx, `
			INSERT INTO watchlist_items (watchlist_id, symbol, position)
			VALUES ($1, $2, COALESCE(
				(SELECT max(position) + 1 FROM watchlist_items WHERE watchlist_id = $1), 0))
			ON CONFLICT (watchlist_id, symbol) DO NOTHING`, id, sym.String())
		if xerr != nil {
			return nil, nil, nil, fmt.Errorf("add %s: %w", raw, xerr)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added = append(added, sym.String())
		} else {
			// Already on the list. Not an error, and not silently counted as
			// added either.
			skipped = append(skipped, sym.String())
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, fmt.Errorf("add to watchlist: commit: %w", err)
	}
	return added, skipped, rejected, nil
}

// RemoveFromWatchlist drops one instrument from one list.
func (d *DB) RemoveFromWatchlist(ctx context.Context, id int64, symbol string) error {
	_, err := d.db.ExecContext(ctx,
		`DELETE FROM watchlist_items WHERE watchlist_id = $1 AND symbol = $2`, id, symbol)
	if err != nil {
		return fmt.Errorf("remove from watchlist: %w", err)
	}
	return nil
}

// CopyWatchlist copies every instrument from one list to another.
//
// Additive rather than replacing: copying a research list onto a positions
// list should extend it, and anyone who wanted a replacement can empty the
// destination first — which is recoverable, where an unexpected wipe is not.
func (d *DB) CopyWatchlist(ctx context.Context, from, to int64) (copied int, err error) {
	if from == to {
		return 0, fmt.Errorf("cannot copy a watchlist onto itself")
	}
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO watchlist_items (watchlist_id, symbol, note, position)
		SELECT $2, symbol, note,
		       COALESCE((SELECT max(position) + 1 FROM watchlist_items WHERE watchlist_id = $2), 0)
		         + row_number() OVER (ORDER BY position, symbol) - 1
		FROM watchlist_items WHERE watchlist_id = $1
		ON CONFLICT (watchlist_id, symbol) DO NOTHING`, from, to)
	if err != nil {
		return 0, fmt.Errorf("copy watchlist: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// WatchlistEntries is one list's members with their notes.
//
// WatchlistSymbols returns bare symbols, which is all the alert engine needs.
// The rail also shows the note under each ticker, and fetching it separately
// per row would turn one query into twenty.
func (d *DB) WatchlistEntries(ctx context.Context, id int64) ([]storage.WatchlistEntry, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT symbol, note, position, added_at
		FROM watchlist_items
		WHERE watchlist_id = $1
		ORDER BY position ASC, added_at ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: watchlist entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []storage.WatchlistEntry{}
	for rows.Next() {
		var (
			raw string
			e   storage.WatchlistEntry
		)
		if err := rows.Scan(&raw, &e.Note, &e.Position, &e.AddedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan watchlist entry: %w", err)
		}
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			// A row that predates ticker validation. Skipping it keeps the
			// rail readable; the operator can delete it by hand.
			continue
		}
		e.Symbol = sym
		out = append(out, e)
	}
	return out, rows.Err()
}

// qualify gives a bare ticker the exchange it almost certainly meant.
//
// ParseSymbol reads an unsuffixed symbol as US, which is the right default
// for the parser and the wrong one here. Bulk paste exists because operators
// arrive with a spreadsheet column of NSE tickers, and those columns do not
// carry suffixes: pasting "TCS" next to an existing "TCS.NSE" put the same
// company on the list twice, once as an Indian listing and once as a US one
// that does not exist.
//
// The index constituents are the authority, so this only ever promotes a
// ticker the NSE actually lists. AAPL, MSFT and the rest are not constituents
// and stay US, which is what an operator typing them means.
func (d *DB) qualify(ctx context.Context, raw string) string {
	if strings.Contains(raw, ".") {
		return raw
	}
	ticker := strings.ToUpper(strings.TrimSpace(raw))

	// listings is the full referenceable universe, both venues, not just the
	// ~750/504-name scan subset -- a ticker worth watching is not necessarily
	// one the scanner covers.
	var onNSE, onUS bool
	err := d.db.QueryRowContext(ctx, `
		SELECT
		    EXISTS (SELECT 1 FROM listings WHERE symbol = $1 || '.NSE'),
		    EXISTS (SELECT 1 FROM listings WHERE symbol = $1 AND venue = 'US')`,
		ticker).Scan(&onNSE, &onUS)
	if err != nil {
		// A lookup failure falls back to the parser's own default rather than
		// rejecting the symbol: being wrong about the exchange is recoverable,
		// refusing to add anything is not.
		return raw
	}
	switch {
	case onNSE && !onUS:
		return ticker + ".NSE"
	case onUS && !onNSE:
		return ticker
	case onNSE && onUS:
		// A genuine collision (ABB, INFY): the same ticker names two
		// different instruments, one per venue. There is no way to guess
		// which one an operator meant from the bare string alone -- ideally
		// this would be reported back as ambiguous and let them pick, but
		// AddToWatchlist's three-way added/skipped/rejected contract has no
		// fourth bucket for it yet. Left bare (US) rather than defaulting to
		// NSE, matching this build's US-first default; add the venue suffix
		// explicitly (INFY.NSE) to reach the other one.
		return ticker
	default:
		// Neither master recognizes it. Left bare rather than rejected --
		// ParseSymbol's own default (US) is a reasonable guess, and an
		// operator watching something not yet in either reference file
		// should not be blocked by that gap.
		return raw
	}
}
