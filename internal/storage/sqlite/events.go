package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// SaveRawItems stores fetched items, ignoring ones already held.
//
// Uniqueness is (source_id, content_hash). A feed re-polled every minute must
// converge on the same rows rather than growing without bound, while a genuine
// correction — the publisher editing a headline — hashes differently and is
// stored as the separate item it is.
//
// The count of new rows comes from each statement's own RowsAffected, which
// INSERT OR IGNORE reports as 1 for an insert and 0 for a conflict. That is
// the one counting method SQLite makes unambiguous here; asking after the fact
// with changes() would report the same figure for a row inserted and a row
// merely touched.
func (s *DB) SaveRawItems(ctx context.Context, items []news.RawItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlite: begin save raw items: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO raw_items
    (source_id, content_hash, url, canonical_url, title, description, publisher,
     payload, occurred_at, published_at, discovered_at, fetched_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("sqlite: prepare save raw items: %w", err)
	}
	defer stmt.Close()

	added := 0
	for _, it := range items {
		if it.ContentHash == "" || it.SourceID == "" {
			continue
		}
		res, err := stmt.ExecContext(ctx,
			it.SourceID, it.ContentHash, it.URL, it.CanonicalURL, it.Title,
			it.Description, it.Publisher, it.Payload,
			nullableUnix(it.OccurredAt), nullableUnix(it.PublishedAt),
			it.DiscoveredAt.UTC().Unix(), it.FetchedAt.UTC().Unix())
		if err != nil {
			return added, fmt.Errorf("sqlite: save raw item from %s: %w", it.SourceID, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			added++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sqlite: commit save raw items: %w", err)
	}
	return added, nil
}

// ListRawItems returns recently discovered items, newest first.
//
// Ordering is by discovery rather than publication on purpose. Publication
// timestamps are supplied by hundreds of different publishers with clocks and
// conventions we do not control, and sorting a feed by them lets one source
// with a skewed clock dominate the top of the page.
func (s *DB) ListRawItems(ctx context.Context, limit int) ([]news.RawItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, source_id, content_hash, url, canonical_url, title, description,
       publisher, occurred_at, published_at, discovered_at, fetched_at
FROM raw_items
ORDER BY discovered_at DESC, id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list raw items: %w", err)
	}
	defer rows.Close()
	return scanRawItems(rows)
}

// ListRawItemsBySource returns recent items from one source.
func (s *DB) ListRawItemsBySource(ctx context.Context, sourceID string, limit int) ([]news.RawItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, source_id, content_hash, url, canonical_url, title, description,
       publisher, occurred_at, published_at, discovered_at, fetched_at
FROM raw_items
WHERE source_id = ?
ORDER BY discovered_at DESC, id DESC
LIMIT ?`, sourceID, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list raw items for %s: %w", sourceID, err)
	}
	defer rows.Close()
	return scanRawItems(rows)
}

func scanRawItems(rows *sql.Rows) ([]news.RawItem, error) {
	var out []news.RawItem
	for rows.Next() {
		var (
			it                     news.RawItem
			occurred, published    sql.NullInt64
			discovered, fetchedRaw int64
		)
		if err := rows.Scan(&it.ID, &it.SourceID, &it.ContentHash, &it.URL,
			&it.CanonicalURL, &it.Title, &it.Description, &it.Publisher,
			&occurred, &published, &discovered, &fetchedRaw); err != nil {
			return nil, fmt.Errorf("sqlite: scan raw item: %w", err)
		}
		if occurred.Valid {
			it.OccurredAt = time.Unix(occurred.Int64, 0).UTC()
		}
		if published.Valid {
			it.PublishedAt = time.Unix(published.Int64, 0).UTC()
		}
		it.DiscoveredAt = time.Unix(discovered, 0).UTC()
		it.FetchedAt = time.Unix(fetchedRaw, 0).UTC()
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountRawItems reports how many items are held, for the health page.
func (s *DB) CountRawItems(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM raw_items`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlite: count raw items: %w", err)
	}
	return n, nil
}

// PruneRawItems deletes items discovered before the cutoff.
func (s *DB) PruneRawItems(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM raw_items WHERE discovered_at < ?`, before.UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("sqlite: prune raw items: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SaveSourceHealth records the outcome of one fetch.
func (s *DB) SaveSourceHealth(ctx context.Context, h news.SourceHealth) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO source_health
    (source_id, last_attempt_at, last_success_at, last_failure_at, last_error,
     consecutive_failures, total_attempts, total_successes, total_items,
     total_new_items, etag, last_modified)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_id) DO UPDATE SET
    last_attempt_at      = excluded.last_attempt_at,
    last_success_at      = excluded.last_success_at,
    last_failure_at      = excluded.last_failure_at,
    last_error           = excluded.last_error,
    consecutive_failures = excluded.consecutive_failures,
    total_attempts       = excluded.total_attempts,
    total_successes      = excluded.total_successes,
    total_items          = excluded.total_items,
    total_new_items      = excluded.total_new_items,
    etag                 = excluded.etag,
    last_modified        = excluded.last_modified`,
		h.SourceID, nullableUnix(h.LastAttemptAt), nullableUnix(h.LastSuccessAt),
		nullableUnix(h.LastFailureAt), h.LastError, h.ConsecutiveFailures,
		h.TotalAttempts, h.TotalSuccesses, h.TotalItems, h.TotalNewItems,
		h.ETag, h.LastModified)
	if err != nil {
		return fmt.Errorf("sqlite: save source health for %s: %w", h.SourceID, err)
	}
	return nil
}

// LoadSourceHealth returns the stored health of every source.
func (s *DB) LoadSourceHealth(ctx context.Context) ([]news.SourceHealth, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT source_id, last_attempt_at, last_success_at, last_failure_at, last_error,
       consecutive_failures, total_attempts, total_successes, total_items,
       total_new_items, etag, last_modified
FROM source_health
ORDER BY source_id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load source health: %w", err)
	}
	defer rows.Close()

	var out []news.SourceHealth
	for rows.Next() {
		var (
			h                         news.SourceHealth
			attempt, success, failure sql.NullInt64
		)
		if err := rows.Scan(&h.SourceID, &attempt, &success, &failure, &h.LastError,
			&h.ConsecutiveFailures, &h.TotalAttempts, &h.TotalSuccesses,
			&h.TotalItems, &h.TotalNewItems, &h.ETag, &h.LastModified); err != nil {
			return nil, fmt.Errorf("sqlite: scan source health: %w", err)
		}
		if attempt.Valid {
			h.LastAttemptAt = time.Unix(attempt.Int64, 0).UTC()
		}
		if success.Valid {
			h.LastSuccessAt = time.Unix(success.Int64, 0).UTC()
		}
		if failure.Valid {
			h.LastFailureAt = time.Unix(failure.Int64, 0).UTC()
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// nullableUnix renders a zero time as SQL NULL rather than as the Unix epoch.
//
// The distinction is not cosmetic. A publisher that omits a timestamp must be
// recorded as "unknown", because storing 1970 would make an undated item sort
// as the oldest thing in the database and quietly rewrite the ordering that
// every later analysis depends on.
func nullableUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Unix()
}
