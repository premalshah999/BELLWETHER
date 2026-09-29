package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// rolloverBatch is how many events move per transaction.
//
// Small enough that a failed batch is cheap to retry and the primary is never
// locked for long, large enough that moving a month of news is not a million
// round trips to a managed endpoint that charges for them.
const rolloverBatch = 500

// copyableColumns reads a table's real column list from the database,
// excluding generated columns.
//
// Derived rather than written down, because writing it down was wrong. The
// first version of this file hardcoded five column lists and four were
// incorrect -- event_evidence's timestamp is added_at rather than
// first_seen_at, event_entities has direction rather than impact_direction and
// a rationale column that was missed entirely, event_facts has a num column
// that was missed, and event_briefs was in the delete list without being in
// the copy list at all, which would have destroyed every archived brief.
//
// Those are exactly the mistakes a hand-maintained column list invites, and a
// future migration adding a column would have reintroduced them silently. The
// generated columns are excluded because naming one in an INSERT is an error:
// events.search_vector is GENERATED ALWAYS and Postgres computes it on write.
func (a *Archive) copyableColumns(ctx context.Context, table string) ([]string, error) {
	rows, err := a.hot.db.QueryContext(ctx, `
        SELECT column_name
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = $1
          AND is_generated = 'NEVER'
        ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("postgres: read %s columns: %w", table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("postgres: table %s has no columns to copy", table)
	}
	return cols, nil
}

// eventChildTables are the tables whose rows belong to an event and must move
// with it. Order matters on insert: each references events, so events goes
// first; on delete the order is reversed.
//
// event_briefs is here. It is the AI-written summary of an event, and it is the
// most expensive row in the graph to recreate -- it cost a model call.
var eventChildTables = []string{
	"event_evidence",
	"event_entities",
	"event_facts",
	"event_sectors",
	"event_briefs",
}

// Rollover moves aged news from the primary to the archive.
//
// The unit that moves is a whole event: the event row, its evidence, entities,
// facts, sectors and brief, and the raw items its evidence points at. Nothing
// belonging to one event is ever left on the other server, because that is the
// entire property that makes two shards queryable with one piece of SQL.
//
// Both identity columns are GENERATED ALWAYS, so the inserts override the
// system value to keep the ids the children join on.
//
// Idempotent: every insert is ON CONFLICT DO NOTHING and the delete only
// follows a successful copy, so an interrupted run leaves rows on both sides
// and the next run finishes the job rather than duplicating it.
func (a *Archive) Rollover(ctx context.Context, now time.Time) (moved int, err error) {
	cutoff := a.cutoff(now)
	for {
		n, err := a.rolloverOnce(ctx, cutoff)
		if err != nil {
			return moved, err
		}
		if n == 0 {
			break
		}
		moved += n
		if ctx.Err() != nil {
			return moved, ctx.Err()
		}
	}
	if moved > 0 {
		a.log.Info("news rolled over to the archive", "events", moved, "older_than", cutoff)
	}
	return moved, nil
}

func (a *Archive) rolloverOnce(ctx context.Context, cutoff time.Time) (int, error) {
	ids, err := a.agedEventIDs(ctx, cutoff)
	if err != nil || len(ids) == 0 {
		return 0, err
	}

	// The raw items these events were read from. Fetched before anything is
	// deleted, because the evidence rows are what name them.
	rawIDs, err := a.rawItemIDsFor(ctx, ids)
	if err != nil {
		return 0, err
	}

	// Copy first, in dependency order: raw items and events before the rows
	// that reference them.
	if err := a.copyTable(ctx, "raw_items", "id", rawIDs); err != nil {
		return 0, err
	}
	if err := a.copyTable(ctx, "events", "id", ids); err != nil {
		return 0, err
	}
	for _, child := range eventChildTables {
		if err := a.copyTable(ctx, child, "event_id", ids); err != nil {
			return 0, err
		}
	}

	// Only now remove them from the primary, children first.
	if err := a.deleteFromHot(ctx, ids, rawIDs); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// agedEventIDs finds events past the cutoff, measured with the same expression
// the feed measures age with, so "archived" and "outside the window" mean the
// same thing.
func (a *Archive) agedEventIDs(ctx context.Context, cutoff time.Time) ([]int64, error) {
	rows, err := a.hot.db.QueryContext(ctx, `
        SELECT e.id FROM events e
        WHERE `+contentAgeExpr+` < $1
        ORDER BY e.id
        LIMIT $2`, cutoff, rolloverBatch)
	if err != nil {
		return nil, fmt.Errorf("postgres: find aged events: %w", err)
	}
	defer rows.Close()
	return scanIDs(rows)
}

// rawItemIDsFor returns the raw items these events cite and no hot event does.
//
// An event can be merged from items discovered weeks apart, so a raw item may
// support both an aged event and a current one. Copying it is harmless;
// deleting it from the primary while a current event still cites it would
// break that event's evidence, so the second condition is load-bearing.
func (a *Archive) rawItemIDsFor(ctx context.Context, eventIDs []int64) ([]int64, error) {
	rows, err := a.hot.db.QueryContext(ctx, `
        SELECT DISTINCT ev.raw_item_id
        FROM event_evidence ev
        WHERE ev.event_id = ANY($1)`, int64Array(eventIDs))
	if err != nil {
		return nil, fmt.Errorf("postgres: find raw items for aged events: %w", err)
	}
	defer rows.Close()
	return scanIDs(rows)
}

// copyTable reads rows from the primary and inserts them into the archive.
//
// One implementation for every table, over whatever columns the table actually
// has: the values never need interpreting here, only moving.
func (a *Archive) copyTable(ctx context.Context, table, keyCol string, keys []int64) error {
	if len(keys) == 0 {
		return nil
	}
	cols, err := a.copyableColumns(ctx, table)
	if err != nil {
		return err
	}
	quoted := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = pq(c)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	list := strings.Join(quoted, ", ")

	src, err := a.hot.db.QueryContext(ctx,
		`SELECT `+list+` FROM `+pq(table)+` WHERE `+pq(keyCol)+` = ANY($1)`, int64Array(keys))
	if err != nil {
		return fmt.Errorf("postgres: read %s for archive: %w", table, err)
	}
	defer src.Close()

	// OVERRIDING SYSTEM VALUE only where there is an identity column to
	// override; on a child table it is a syntax error.
	override := ""
	if keyCol == "id" {
		override = "OVERRIDING SYSTEM VALUE "
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) %sVALUES (%s) ON CONFLICT DO NOTHING",
		pq(table), list, override, strings.Join(placeholders, ", "))

	tx, err := a.cold.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: begin archive write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, insert)
	if err != nil {
		return fmt.Errorf("postgres: prepare archive insert for %s: %w", table, err)
	}
	defer stmt.Close()

	holders := make([]any, len(cols))
	values := make([]any, len(cols))
	for i := range holders {
		holders[i] = &values[i]
	}
	for src.Next() {
		if err := src.Scan(holders...); err != nil {
			return fmt.Errorf("postgres: scan %s for archive: %w", table, err)
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			return fmt.Errorf("postgres: archive insert into %s: %w", table, err)
		}
	}
	if err := src.Err(); err != nil {
		return fmt.Errorf("postgres: read %s for archive: %w", table, err)
	}
	return tx.Commit()
}

// pq quotes an identifier. These names come from the schema and from the
// constant above rather than from input, but an unquoted identifier built by
// string concatenation is a habit worth not having.
func pq(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// deleteFromHot removes the copied rows, children before parents.
//
// The raw-item delete is conditional: one still cited by an event that stayed
// behind belongs to that event too, and removing it would leave a current
// event with no evidence.
func (a *Archive) deleteFromHot(ctx context.Context, eventIDs, rawIDs []int64) error {
	tx, err := a.hot.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: begin rollover delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ids := int64Array(eventIDs)
	for _, table := range eventChildTables {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE event_id = ANY($1)`, ids); err != nil {
			return fmt.Errorf("postgres: rollover delete from %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("postgres: rollover delete events: %w", err)
	}
	if len(rawIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `
            DELETE FROM raw_items r
            WHERE r.id = ANY($1)
              AND NOT EXISTS (SELECT 1 FROM event_evidence ev WHERE ev.raw_item_id = r.id)`,
			int64Array(rawIDs)); err != nil {
			return fmt.Errorf("postgres: rollover delete raw items: %w", err)
		}
	}
	return tx.Commit()
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReclaimSpace rewrites the news tables so the space a rollover freed is
// returned to the filesystem.
//
// This is separate from Rollover, and deliberately not scheduled, because
// VACUUM FULL takes an ACCESS EXCLUSIVE lock: for its duration the table
// cannot be read or written, so ingestion stalls and the feed 500s. On
// raw_items that is tens of seconds. It is the right thing to run once, with
// somebody watching, not at half past two unattended.
//
// It also needs room for a second copy of each table while it rewrites, which
// on a managed plan that is already near its ceiling is the thing most likely
// to fail. Sizes are logged before and after so the trade is visible.
//
// Without this, a rollover does not shrink the primary -- it stops it growing.
// Postgres marks the freed pages reusable and new rows fill them, so the
// database plateaus rather than shrinking, which is enough for a steady state
// and not enough to get under a limit it has already crossed.
func (a *Archive) ReclaimSpace(ctx context.Context) error {
	tables := append([]string{"raw_items", "events"}, eventChildTables...)
	for _, table := range tables {
		var before, after int64
		row := a.hot.db.QueryRowContext(ctx,
			`SELECT pg_total_relation_size($1)`, table)
		if err := row.Scan(&before); err != nil {
			return fmt.Errorf("postgres: size of %s: %w", table, err)
		}

		// VACUUM cannot run inside a transaction block, so this goes straight
		// at the connection.
		if _, err := a.hot.db.ExecContext(ctx, `VACUUM (FULL, ANALYZE) `+pq(table)); err != nil {
			return fmt.Errorf("postgres: vacuum full %s: %w", table, err)
		}

		if err := a.hot.db.QueryRowContext(ctx,
			`SELECT pg_total_relation_size($1)`, table).Scan(&after); err != nil {
			return fmt.Errorf("postgres: size of %s after vacuum: %w", table, err)
		}
		a.log.Info("reclaimed space",
			"table", table,
			"before_mb", before/(1<<20), "after_mb", after/(1<<20),
			"freed_mb", (before-after)/(1<<20))
	}
	return nil
}
