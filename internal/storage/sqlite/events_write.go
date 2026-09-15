package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/news"
)

// ListPendingRawItems returns items that have not yet been through the event
// pipeline, oldest first.
//
// Order matters here in a way it does not for display. Events are built by
// accumulating evidence, and processing a follow-up report before the filing
// it follows up would create the report as its own event and leave the filing
// to merge into it — inverting which source the event is anchored on.
func (s *DB) ListPendingRawItems(ctx context.Context, limit int) ([]news.RawItem, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, source_id, content_hash, url, canonical_url, title, description,
       publisher, occurred_at, published_at, discovered_at, fetched_at
FROM raw_items
WHERE processed_at IS NULL
ORDER BY discovered_at ASC, id ASC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list pending raw items: %w", err)
	}
	defer rows.Close()
	return scanRawItems(rows)
}

// MarkRawItemsProcessed records that items have been through the pipeline.
func (s *DB) MarkRawItemsProcessed(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin mark processed: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `UPDATE raw_items SET processed_at = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare mark processed: %w", err)
	}
	defer stmt.Close()

	ts := at.UTC().Unix()
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, ts, id); err != nil {
			return fmt.Errorf("sqlite: mark raw item %d processed: %w", id, err)
		}
	}
	return tx.Commit()
}

// ClusterCandidates returns recent events a new item might belong to.
//
// The two kinds of candidate are fetched separately, and that separation is
// load-bearing rather than tidiness. A single query with
// "shares a symbol OR has no symbols" sounds equivalent, but during a backfill
// the entity-less arm matches hundreds of events that all share one discovery
// second, and they consume the whole result budget before any symbol match is
// reached. The observed effect was two identical NSE filings for one order —
// the same event published as XBRL and as PDF — becoming two events, because
// the first had been crowded out of the second's candidate window.
//
// Fetching each arm with its own budget makes the outcome independent of how
// much unrelated traffic arrived in between.
func (s *DB) ClusterCandidates(ctx context.Context, symbols []string, titleKey string, since time.Time, limit int) ([]events.Candidate, error) {
	if limit <= 0 {
		limit = 200
	}
	sinceUnix := since.UTC().Unix()

	const selectCols = `
SELECT e.id, e.event_type, e.headline, e.title_key, e.discovered_at, e.official
FROM events e `

	seen := map[int64]bool{}
	var out []events.Candidate

	collect := func(query string, args ...any) error {
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("sqlite: cluster candidates: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				c          events.Candidate
				typ        string
				discovered int64
				official   int
			)
			if err := rows.Scan(&c.ID, &typ, &c.Headline, &c.TitleKey, &discovered, &official); err != nil {
				return fmt.Errorf("sqlite: scan cluster candidate: %w", err)
			}
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			c.Type = events.Type(typ)
			c.DiscoveredAt = time.Unix(discovered, 0).UTC()
			c.Official = official != 0
			out = append(out, c)
		}
		return rows.Err()
	}

	// Arm one: events already attached to one of this item's companies.
	if len(symbols) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(symbols)), ",")
		args := []any{sinceUnix}
		for _, sym := range symbols {
			args = append(args, sym)
		}
		args = append(args, limit)
		if err := collect(selectCols+`
WHERE e.discovered_at >= ?
  AND EXISTS (SELECT 1 FROM event_entities x WHERE x.event_id = e.id AND x.symbol IN (`+ph+`))
ORDER BY e.discovered_at DESC, e.id DESC
LIMIT ?`, args...); err != nil {
			return nil, err
		}
	}

	// Arm two: an exact headline-token match. This is an index lookup, so it
	// stays cheap however large the archive grows, and it is the only route
	// by which syndicated copy with no resolvable company collapses into one
	// story instead of one row per outlet.
	if titleKey != "" {
		if err := collect(selectCols+`
WHERE e.discovered_at >= ? AND e.title_key = ?
ORDER BY e.discovered_at DESC, e.id DESC
LIMIT 50`, sinceUnix, titleKey); err != nil {
			return nil, err
		}
	}

	if len(out) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	symsByEvent, err := s.symbolsForEvents(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Symbols = symsByEvent[out[i].ID]
	}
	return out, nil
}

func (s *DB) symbolsForEvents(ctx context.Context, ids []int64) (map[int64][]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, symbol FROM event_entities WHERE event_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load event symbols: %w", err)
	}
	defer rows.Close()

	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var sym string
		if err := rows.Scan(&id, &sym); err != nil {
			return nil, err
		}
		out[id] = append(out[id], sym)
	}
	return out, rows.Err()
}

// UpsertEvent creates an event, or returns the existing one with that
// fingerprint.
//
// The fingerprint is what makes reprocessing safe: replaying the same raw item
// after a parser fix converges on the same event instead of forking it.
func (s *DB) UpsertEvent(ctx context.Context, e news.Event, titleKey string) (int64, bool, error) {
	var (
		id       int64
		existing bool
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM events WHERE fingerprint = ?`, e.Fingerprint).Scan(&id)
	switch {
	case err == nil:
		existing = true
	case err != sql.ErrNoRows:
		return 0, false, fmt.Errorf("sqlite: look up event fingerprint: %w", err)
	}
	if existing {
		return id, false, nil
	}

	res, err := s.db.ExecContext(ctx, `
INSERT INTO events
    (fingerprint, event_type, headline, summary, occurred_at, published_at,
     discovered_at, confirmed_at, updated_at, importance, confidence,
     best_trust, source_count, official, title_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Fingerprint, e.Type, e.Headline, e.Summary,
		nullableUnix(e.OccurredAt), nullableUnix(e.PublishedAt),
		e.DiscoveredAt.UTC().Unix(), nullableUnix(e.ConfirmedAt),
		e.UpdatedAt.UTC().Unix(), nullableInt(e.Importance), nullableFloat(e.Confidence),
		e.BestTrust, e.SourceCount, boolToInt(e.Official), titleKey)
	if err != nil {
		return 0, false, fmt.Errorf("sqlite: insert event: %w", err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("sqlite: event id: %w", err)
	}
	return id, true, nil
}

// AttachEvidence links a raw item to an event.
func (s *DB) AttachEvidence(ctx context.Context, eventID, rawItemID int64, sourceID string, trust int, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO event_evidence (event_id, raw_item_id, source_id, trust, added_at)
VALUES (?, ?, ?, ?, ?)`, eventID, rawItemID, sourceID, trust, at.UTC().Unix())
	if err != nil {
		return fmt.Errorf("sqlite: attach evidence: %w", err)
	}
	return nil
}

// TouchEvent recomputes the aggregates that change as evidence accumulates.
//
// Doing this in SQL from the evidence table, rather than incrementing counters
// in Go, means the numbers cannot drift: source_count, best_trust and
// confirmed_at are always exactly what the evidence supports, even if a
// process died halfway through attaching some.
func (s *DB) TouchEvent(ctx context.Context, eventID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE events SET
    source_count = (SELECT COUNT(DISTINCT source_id) FROM event_evidence WHERE event_id = ?),
    best_trust   = COALESCE((SELECT MAX(trust) FROM event_evidence WHERE event_id = ?), best_trust),
    official     = CASE WHEN EXISTS (
                       SELECT 1 FROM event_evidence ev
                       JOIN raw_items ri ON ri.id = ev.raw_item_id
                       WHERE ev.event_id = ? AND ev.trust >= 100
                   ) THEN 1 ELSE official END,
    confirmed_at = COALESCE(confirmed_at, (
                       SELECT MIN(ri.discovered_at) FROM event_evidence ev
                       JOIN raw_items ri ON ri.id = ev.raw_item_id
                       WHERE ev.event_id = ? AND ev.trust >= 100
                   )),
    updated_at   = ?
WHERE id = ?`, eventID, eventID, eventID, eventID, at.UTC().Unix(), eventID)
	if err != nil {
		return fmt.Errorf("sqlite: touch event %d: %w", eventID, err)
	}
	return nil
}

// UpsertEventEntities records which companies an event concerns.
//
// A stronger match replaces a weaker one for the same symbol, so a news report
// that guessed at a company is corrected when the exchange filing names it.
// Model-supplied direction is preserved: this path only ever writes matching
// evidence, and must not erase a reading the classifier already made.
func (s *DB) UpsertEventEntities(ctx context.Context, eventID int64, entities []news.EventEntity) error {
	if len(entities) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin upsert entities: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO event_entities (event_id, symbol, relationship, match_confidence, match_method)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (event_id, symbol) DO UPDATE SET
    relationship     = CASE WHEN excluded.match_confidence > event_entities.match_confidence
                            THEN excluded.relationship ELSE event_entities.relationship END,
    match_method     = CASE WHEN excluded.match_confidence > event_entities.match_confidence
                            THEN excluded.match_method ELSE event_entities.match_method END,
    match_confidence = MAX(event_entities.match_confidence, excluded.match_confidence)`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare upsert entities: %w", err)
	}
	defer stmt.Close()

	for _, e := range entities {
		rel := e.Relationship
		if rel == "" {
			rel = news.RelMentioned
		}
		if _, err := stmt.ExecContext(ctx, eventID, e.Symbol, string(rel), e.MatchConfidence, e.MatchMethod); err != nil {
			return fmt.Errorf("sqlite: upsert entity %s: %w", e.Symbol, err)
		}
	}
	return tx.Commit()
}

// SaveEventFacts stores the structured values pulled out of a filing.
func (s *DB) SaveEventFacts(ctx context.Context, eventID int64, facts map[string]string) error {
	if len(facts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin save facts: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO event_facts (event_id, key, value, num) VALUES (?, ?, ?, ?)
ON CONFLICT (event_id, key) DO UPDATE SET value = excluded.value, num = excluded.num`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare save facts: %w", err)
	}
	defer stmt.Close()

	for k, v := range facts {
		var num any
		if f, ok := parseFactNumber(v); ok {
			num = f
		}
		if _, err := stmt.ExecContext(ctx, eventID, k, v, num); err != nil {
			return fmt.Errorf("sqlite: save fact %q: %w", k, err)
		}
	}
	return tx.Commit()
}

// parseFactNumber reads a numeric fact value, tolerating a percent sign and
// surrounding space. A non-numeric value stores NULL rather than zero, because
// "not a number" and "zero" are different readings and a later query that
// averages them must not silently include the former.
func parseFactNumber(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(v, "%")
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func nullableInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// SaveEventClassification records the model's reading of an event.
//
// The write is transactional across the event row and its entities because a
// half-applied classification is worse than none: an event marked classified
// but missing its per-company directions would never be picked up again by
// the pending query, and the gap would be invisible.
func (s *DB) SaveEventClassification(ctx context.Context, c events.Classification) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin save classification: %w", err)
	}
	defer tx.Rollback()

	summary := c.Summary
	if c.WhyItMatters != "" {
		if summary != "" {
			summary += " "
		}
		summary += c.WhyItMatters
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE events SET
    event_type    = COALESCE(NULLIF(?, ''), event_type),
    summary       = COALESCE(NULLIF(?, ''), summary),
    importance    = COALESCE(?, importance),
    confidence    = COALESCE(?, confidence),
    classified_at = ?,
    model         = ?,
    updated_at    = ?
WHERE id = ?`,
		c.Type, summary, nullableInt(c.Importance), nullableFloat(c.Confidence),
		c.ClassifiedAt.UTC().Unix(), c.Model, c.ClassifiedAt.UTC().Unix(), c.EventID); err != nil {
		return fmt.Errorf("sqlite: update event %d: %w", c.EventID, err)
	}

	if len(c.Entities) > 0 {
		// The model may name a company the resolver did not, so this inserts
		// as well as updates. Match confidence stays at zero for those: they
		// were asserted by a model rather than matched against the listed
		// master, and the two must remain distinguishable.
		stmt, err := tx.PrepareContext(ctx, `
INSERT INTO event_entities
    (event_id, symbol, relationship, match_confidence, match_method,
     direction, impact_strength, rationale)
VALUES (?, ?, ?, 0, 'model', ?, ?, ?)
ON CONFLICT (event_id, symbol) DO UPDATE SET
    direction       = excluded.direction,
    impact_strength = excluded.impact_strength,
    rationale       = CASE WHEN excluded.rationale != '' THEN excluded.rationale
                           ELSE event_entities.rationale END,
    relationship    = CASE WHEN event_entities.match_confidence = 0
                           THEN excluded.relationship ELSE event_entities.relationship END`)
		if err != nil {
			return fmt.Errorf("sqlite: prepare classification entities: %w", err)
		}
		defer stmt.Close()

		for _, e := range c.Entities {
			if _, err := stmt.ExecContext(ctx, c.EventID, e.Symbol, string(e.Relationship),
				string(e.Direction), e.ImpactStrength, e.Rationale); err != nil {
				return fmt.Errorf("sqlite: save entity reading %s: %w", e.Symbol, err)
			}
		}
	}
	return tx.Commit()
}

// SaveEventSectors records which industries an event bears on.
func (s *DB) SaveEventSectors(ctx context.Context, eventID int64, sectors []string) error {
	if len(sectors) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin save sectors: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO event_sectors (event_id, sector) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare save sectors: %w", err)
	}
	defer stmt.Close()

	for _, sec := range sectors {
		if _, err := stmt.ExecContext(ctx, eventID, sec); err != nil {
			return fmt.Errorf("sqlite: save sector %q: %w", sec, err)
		}
	}
	return tx.Commit()
}

// EventSectors returns the industries an event bears on.
func (s *DB) EventSectors(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sector FROM event_sectors WHERE event_id = ? ORDER BY sector`, eventID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: event sectors: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sec string
		if err := rows.Scan(&sec); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// PruneOrphanRawItems deletes old raw items that no event depends on.
//
// The retention rule follows from what each table is for. Events are the
// historical record the whole application is being built to accumulate, and
// they are never pruned. Raw items that back an event are its evidence — the
// thing that makes a bad parse or a bad prompt recoverable — and they are
// never pruned either.
//
// What is left is items that produced no event: mutual-fund NAV declarations
// filtered at the door, and duplicate syndicated copy that merged into
// evidence held elsewhere. Those are pure growth with no downstream reader,
// and at roughly a third of daily volume they are most of what the database
// would otherwise accumulate.
func (s *DB) PruneOrphanRawItems(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
DELETE FROM raw_items
WHERE discovered_at < ?
  AND processed_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM event_evidence ee WHERE ee.raw_item_id = raw_items.id)`,
		before.UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("sqlite: prune orphan raw items: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Compact reclaims free pages and truncates the write-ahead log.
//
// SQLite does not return deleted pages to the filesystem on its own, so after
// a prune the file stays the size it reached at its peak. This is cheap to run
// on a database of this size and is scheduled well outside market hours,
// because VACUUM takes a write lock for its duration.
func (s *DB) Compact(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("sqlite: checkpoint: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("sqlite: vacuum: %w", err)
	}
	// ANALYZE after a VACUUM keeps the query planner's statistics honest;
	// without it the planner works from counts taken before the prune.
	if _, err := s.db.ExecContext(ctx, `ANALYZE`); err != nil {
		return fmt.Errorf("sqlite: analyze: %w", err)
	}
	return nil
}

// StorageStats reports what the database holds, for the health page.
type StorageStats struct {
	RawItems     int   `json:"raw_items"`
	PendingItems int   `json:"pending_items"`
	Events       int   `json:"events"`
	Classified   int   `json:"classified_events"`
	Evidence     int   `json:"evidence_links"`
	Entities     int   `json:"entity_links"`
	PageCount    int64 `json:"page_count"`
	PageSize     int64 `json:"page_size"`
	FreePages    int64 `json:"free_pages"`
	SizeBytes    int64 `json:"size_bytes"`
	ReclaimBytes int64 `json:"reclaimable_bytes"`
}

// Stats gathers storage counters in one pass.
func (s *DB) Stats(ctx context.Context) (StorageStats, error) {
	var st StorageStats
	scalar := func(query string, dest *int) error {
		return s.db.QueryRowContext(ctx, query).Scan(dest)
	}
	scalar64 := func(query string, dest *int64) error {
		return s.db.QueryRowContext(ctx, query).Scan(dest)
	}
	for _, q := range []struct {
		sql  string
		dest *int
	}{
		{`SELECT COUNT(1) FROM raw_items`, &st.RawItems},
		{`SELECT COUNT(1) FROM raw_items WHERE processed_at IS NULL`, &st.PendingItems},
		{`SELECT COUNT(1) FROM events`, &st.Events},
		{`SELECT COUNT(1) FROM events WHERE classified_at IS NOT NULL`, &st.Classified},
		{`SELECT COUNT(1) FROM event_evidence`, &st.Evidence},
		{`SELECT COUNT(1) FROM event_entities`, &st.Entities},
	} {
		if err := scalar(q.sql, q.dest); err != nil {
			return st, fmt.Errorf("sqlite: stats: %w", err)
		}
	}
	for _, q := range []struct {
		sql  string
		dest *int64
	}{
		{`PRAGMA page_count`, &st.PageCount},
		{`PRAGMA page_size`, &st.PageSize},
		{`PRAGMA freelist_count`, &st.FreePages},
	} {
		if err := scalar64(q.sql, q.dest); err != nil {
			return st, fmt.Errorf("sqlite: stats: %w", err)
		}
	}
	st.SizeBytes = st.PageCount * st.PageSize
	st.ReclaimBytes = st.FreePages * st.PageSize
	return st, nil
}

// ResetEvents discards every derived event and reopens the raw items that
// produced them.
//
// This is the operation that makes the "never lose the original" rule pay for
// itself. A parser fix, a taxonomy change or a clustering bug can be applied
// retrospectively to the whole archive, because nothing derived was ever the
// only copy: raw_items still holds exactly what each publisher sent. Two real
// defects — headlines keeping NSE's boilerplate, and duplicate filings failing
// to cluster during a backlog — were repaired this way rather than left
// permanently baked into history.
//
// Evidence itself is untouched. Only the interpretation is thrown away.
func (s *DB) ResetEvents(ctx context.Context) (int, error) {
	before, err := s.CountEvents(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlite: begin reset events: %w", err)
	}
	defer tx.Rollback()

	// Child rows are removed explicitly rather than relying on cascade, since
	// foreign-key enforcement is a connection pragma and this must be correct
	// regardless of how the connection was opened.
	for _, stmt := range []string{
		`DELETE FROM event_facts`,
		`DELETE FROM event_sectors`,
		`DELETE FROM event_entities`,
		`DELETE FROM event_evidence`,
		`DELETE FROM events`,
		`UPDATE raw_items SET processed_at = NULL`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return 0, fmt.Errorf("sqlite: reset events (%s): %w", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sqlite: commit reset events: %w", err)
	}
	return before, nil
}
