package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/storage"
)

// EventFilter is the shared filter type. The legacy SQLite store keeps the
// same signature as the Postgres one so the migration command and the tests
// can use either without translating between two shapes.
type EventFilter = storage.EventFilter

// ListEvents returns events matching a filter, most recently discovered first.
//
// Ordering is by discovery rather than by importance so that the default view
// is a timeline of what happened. Importance is a filter, not a sort: an
// operator scanning the day wants it in order, and re-ranking by a score would
// make the same feed look different every time the classifier ran.
func (s *DB) ListEvents(ctx context.Context, f EventFilter) ([]news.Event, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var (
		where []string
		args  []any
	)
	if f.Symbol != "" {
		where = append(where, `EXISTS (SELECT 1 FROM event_entities x WHERE x.event_id = e.id AND x.symbol = ?)`)
		args = append(args, strings.ToUpper(f.Symbol))
	}
	if len(f.Types) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Types)), ",")
		where = append(where, `e.event_type IN (`+ph+`)`)
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.MinImportance > 0 {
		where = append(where, `COALESCE(e.importance, 0) >= ?`)
		args = append(args, f.MinImportance)
	}
	if !f.Since.IsZero() {
		where = append(where, `e.discovered_at >= ?`)
		args = append(args, f.Since.UTC().Unix())
	}
	if f.OfficialOnly {
		where = append(where, `e.official = 1`)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		// LIKE rather than FTS: the corpus is headlines, the queries are
		// short, and an index-free scan over a few tens of thousands of rows
		// is well under the latency budget. Adding an FTS table would mean a
		// second copy of the text to keep in step for no measurable gain at
		// this size.
		where = append(where, `(e.headline LIKE ? COLLATE NOCASE OR e.summary LIKE ? COLLATE NOCASE)`)
		pattern := "%" + q + "%"
		args = append(args, pattern, pattern)
	}

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, `
SELECT e.id, e.fingerprint, e.event_type, e.headline, e.summary,
       e.occurred_at, e.published_at, e.discovered_at, e.confirmed_at, e.updated_at,
       e.importance, e.confidence, e.best_trust, e.source_count, e.official,
       e.classified_at, e.model
FROM events e
`+clause+`
ORDER BY e.discovered_at DESC, e.id DESC
LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list events: %w", err)
	}
	defer rows.Close()

	out, ids, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	if err := s.attachEntities(ctx, out, ids); err != nil {
		return nil, err
	}
	return out, nil
}

func scanEvents(rows *sql.Rows) ([]news.Event, []int64, error) {
	var out []news.Event
	var ids []int64
	for rows.Next() {
		var (
			e                              news.Event
			occurred, published, confirmed sql.NullInt64
			classified                     sql.NullInt64
			discovered, updated            int64
			importance                     sql.NullInt64
			confidence                     sql.NullFloat64
			official                       int
		)
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.Type, &e.Headline, &e.Summary,
			&occurred, &published, &discovered, &confirmed, &updated,
			&importance, &confidence, &e.BestTrust, &e.SourceCount, &official,
			&classified, &e.Model); err != nil {
			return nil, nil, fmt.Errorf("sqlite: scan event: %w", err)
		}
		if occurred.Valid {
			e.OccurredAt = time.Unix(occurred.Int64, 0).UTC()
		}
		if published.Valid {
			e.PublishedAt = time.Unix(published.Int64, 0).UTC()
		}
		if confirmed.Valid {
			e.ConfirmedAt = time.Unix(confirmed.Int64, 0).UTC()
		}
		if classified.Valid {
			e.ClassifiedAt = time.Unix(classified.Int64, 0).UTC()
		}
		if importance.Valid {
			v := int(importance.Int64)
			e.Importance = &v
		}
		if confidence.Valid {
			v := confidence.Float64
			e.Confidence = &v
		}
		e.DiscoveredAt = time.Unix(discovered, 0).UTC()
		e.UpdatedAt = time.Unix(updated, 0).UTC()
		e.Official = official != 0
		out = append(out, e)
		ids = append(ids, e.ID)
	}
	return out, ids, rows.Err()
}

// attachEntities loads every event's companies in one query.
func (s *DB) attachEntities(ctx context.Context, list []news.Event, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT event_id, symbol, relationship, match_confidence, match_method,
       direction, impact_strength, rationale
FROM event_entities
WHERE event_id IN (`+ph+`)
ORDER BY match_confidence DESC, symbol`, args...)
	if err != nil {
		return fmt.Errorf("sqlite: load event entities: %w", err)
	}
	defer rows.Close()

	byEvent := map[int64][]news.EventEntity{}
	for rows.Next() {
		var (
			id        int64
			ent       news.EventEntity
			rel       string
			direction sql.NullString
			impact    sql.NullFloat64
		)
		if err := rows.Scan(&id, &ent.Symbol, &rel, &ent.MatchConfidence,
			&ent.MatchMethod, &direction, &impact, &ent.Rationale); err != nil {
			return fmt.Errorf("sqlite: scan event entity: %w", err)
		}
		ent.Relationship = news.Relationship(rel)
		if direction.Valid {
			ent.Direction = news.Direction(direction.String)
		}
		if impact.Valid {
			ent.ImpactStrength = impact.Float64
		}
		byEvent[id] = append(byEvent[id], ent)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range list {
		list[i].Entities = byEvent[list[i].ID]
	}
	return nil
}

// GetEvent returns one event with all its evidence attached.
func (s *DB) GetEvent(ctx context.Context, id int64) (news.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.id, e.fingerprint, e.event_type, e.headline, e.summary,
       e.occurred_at, e.published_at, e.discovered_at, e.confirmed_at, e.updated_at,
       e.importance, e.confidence, e.best_trust, e.source_count, e.official,
       e.classified_at, e.model
FROM events e WHERE e.id = ?`, id)
	if err != nil {
		return news.Event{}, fmt.Errorf("sqlite: get event: %w", err)
	}
	list, ids, err := scanEvents(rows)
	rows.Close()
	if err != nil {
		return news.Event{}, err
	}
	if len(list) == 0 {
		return news.Event{}, sql.ErrNoRows
	}
	if err := s.attachEntities(ctx, list, ids); err != nil {
		return news.Event{}, err
	}

	ev, err := s.db.QueryContext(ctx, `
SELECT ri.id, ri.source_id, ri.content_hash, ri.url, ri.canonical_url, ri.title,
       ri.description, ri.publisher, ri.occurred_at, ri.published_at,
       ri.discovered_at, ri.fetched_at
FROM event_evidence ee
JOIN raw_items ri ON ri.id = ee.raw_item_id
WHERE ee.event_id = ?
ORDER BY ee.trust DESC, ri.discovered_at ASC`, id)
	if err != nil {
		return news.Event{}, fmt.Errorf("sqlite: load evidence: %w", err)
	}
	defer ev.Close()
	evidence, err := scanRawItems(ev)
	if err != nil {
		return news.Event{}, err
	}
	list[0].Evidence = evidence
	return list[0], nil
}

// EventFacts returns the structured values extracted for an event.
func (s *DB) EventFacts(ctx context.Context, eventID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM event_facts WHERE event_id = ? ORDER BY key`, eventID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: event facts: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// CountEvents reports how many events are held.
func (s *DB) CountEvents(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite: count events: %w", err)
	}
	return n, nil
}

// ListUnclassifiedEvents returns events the model has not yet processed,
// most important first so a limited token budget is spent where it counts.
func (s *DB) ListUnclassifiedEvents(ctx context.Context, limit int) ([]news.Event, error) {
	if limit <= 0 {
		limit = 40
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT e.id, e.fingerprint, e.event_type, e.headline, e.summary,
       e.occurred_at, e.published_at, e.discovered_at, e.confirmed_at, e.updated_at,
       e.importance, e.confidence, e.best_trust, e.source_count, e.official,
       e.classified_at, e.model
FROM events e
WHERE e.classified_at IS NULL
ORDER BY COALESCE(e.importance, 0) DESC, e.best_trust DESC, e.discovered_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list unclassified events: %w", err)
	}
	defer rows.Close()
	out, ids, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	if err := s.attachEntities(ctx, out, ids); err != nil {
		return nil, err
	}
	return out, nil
}
