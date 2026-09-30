package postgres

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

// SaveRawItems stores fetched items, ignoring ones already held. Uniqueness is
// (source_id, content_hash), so re-polling converges while an edited headline
// is stored as the separate item it is. One statement over unnested arrays,
// not a loop: a large poll is one round trip.
func (d *DB) SaveRawItems(ctx context.Context, items []news.RawItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	n := len(items)
	var (
		sourceIDs  = make([]string, 0, n)
		hashes     = make([]string, 0, n)
		urls       = make([]string, 0, n)
		canonicals = make([]string, 0, n)
		titles     = make([]string, 0, n)
		descs      = make([]string, 0, n)
		publishers = make([]string, 0, n)
		payloads   = make([]string, 0, n)
		occurred   = make([]sql.NullTime, 0, n)
		published  = make([]sql.NullTime, 0, n)
		discovered = make([]time.Time, 0, n)
		fetched    = make([]time.Time, 0, n)
	)
	for _, it := range items {
		if it.ContentHash == "" || it.SourceID == "" {
			continue
		}
		// The schema requires substance; skipping here keeps one unusable
		// item from failing the whole batch on a constraint.
		if strings.TrimSpace(it.Title) == "" && strings.TrimSpace(it.Description) == "" {
			continue
		}
		sourceIDs = append(sourceIDs, it.SourceID)
		hashes = append(hashes, it.ContentHash)
		urls = append(urls, webURL(it.URL))
		canonicals = append(canonicals, webURL(it.CanonicalURL))
		titles = append(titles, it.Title)
		descs = append(descs, it.Description)
		publishers = append(publishers, it.Publisher)
		payloads = append(payloads, it.Payload)
		occurred = append(occurred, sql.NullTime{Time: it.OccurredAt.UTC(), Valid: !it.OccurredAt.IsZero()})
		published = append(published, sql.NullTime{Time: it.PublishedAt.UTC(), Valid: !it.PublishedAt.IsZero()})
		discovered = append(discovered, it.DiscoveredAt.UTC())
		fetched = append(fetched, it.FetchedAt.UTC())
	}
	if len(sourceIDs) == 0 {
		return 0, nil
	}

	var added int
	err := d.db.QueryRowContext(ctx, `
WITH inserted AS (
    INSERT INTO raw_items
        (source_id, content_hash, url, canonical_url, title, description,
         publisher, payload, occurred_at, published_at, discovered_at, fetched_at)
    SELECT * FROM unnest(
        $1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[],
        $7::text[], $8::text[], $9::timestamptz[], $10::timestamptz[],
        $11::timestamptz[], $12::timestamptz[])
    ON CONFLICT (source_id, content_hash) DO NOTHING
    RETURNING 1
)
SELECT count(*) FROM inserted`,
		sourceIDs, hashes, urls, canonicals, titles, descs, publishers, payloads,
		occurred, published, discovered, fetched).Scan(&added)
	if err != nil {
		return 0, fmt.Errorf("postgres: save raw items: %w", err)
	}
	return added, nil
}

const rawItemColumns = `id, source_id, content_hash, url, canonical_url, title,
       description, publisher, occurred_at, published_at, discovered_at, fetched_at`

// rawItemColumnsQualified is the same list through an alias, written out
// rather than derived by string replacement (which also rewrites the "id,"
// inside "source_id,").
const rawItemColumnsQualified = `ri.id, ri.source_id, ri.content_hash, ri.url,
       ri.canonical_url, ri.title, ri.description, ri.publisher, ri.occurred_at,
       ri.published_at, ri.discovered_at, ri.fetched_at`

func scanRawItems(rows *sql.Rows) ([]news.RawItem, error) {
	var out []news.RawItem
	for rows.Next() {
		var (
			it                  news.RawItem
			occurred, published sql.NullTime
		)
		if err := rows.Scan(&it.ID, &it.SourceID, &it.ContentHash, &it.URL,
			&it.CanonicalURL, &it.Title, &it.Description, &it.Publisher,
			&occurred, &published, &it.DiscoveredAt, &it.FetchedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan raw item: %w", err)
		}
		it.OccurredAt = timeOrZero(occurred)
		it.PublishedAt = timeOrZero(published)
		it.DiscoveredAt = it.DiscoveredAt.UTC()
		it.FetchedAt = it.FetchedAt.UTC()
		out = append(out, it)
	}
	return out, rows.Err()
}

// ListPendingRawItems returns items not yet through the event pipeline.
//
// Oldest first, because events are built by accumulating evidence: processing
// a follow-up report before the filing it follows would anchor the event on
// the report and leave the filing to merge into it.
func (d *DB) ListPendingRawItems(ctx context.Context, limit int) ([]news.RawItem, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT `+rawItemColumns+` FROM raw_items WHERE processed_at IS NULL
ORDER BY discovered_at ASC, id ASC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list pending raw items: %w", err)
	}
	defer rows.Close()
	return scanRawItems(rows)
}

// MarkRawItemsProcessed records that items have been through the pipeline.
func (d *DB) MarkRawItemsProcessed(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE raw_items SET processed_at = $2 WHERE id = ANY($1::bigint[])`, ids, at.UTC())
	if err != nil {
		return fmt.Errorf("postgres: mark raw items processed: %w", err)
	}
	return nil
}

// CountRawItems reports how many items are held.
func (d *DB) CountRawItems(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM raw_items`).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count raw items: %w", err)
	}
	return n, nil
}

// PruneOrphanRawItems deletes old items no event depends on: filtered noise
// and duplicate syndicated copy. Events and the items that back them are the
// historical record and are never pruned.
func (d *DB) PruneOrphanRawItems(ctx context.Context, before time.Time) (int, error) {
	res, err := d.db.ExecContext(ctx, `
DELETE FROM raw_items ri
WHERE ri.discovered_at < $1
  AND ri.processed_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM event_evidence ee WHERE ee.raw_item_id = ri.id)`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("postgres: prune orphan raw items: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SaveSourceHealth records the outcome of one fetch.
func (d *DB) SaveSourceHealth(ctx context.Context, h news.SourceHealth) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO source_health
    (source_id, last_attempt_at, last_success_at, last_failure_at, last_error,
     consecutive_failures, total_attempts, total_successes, total_items,
     total_new_items, etag, last_modified)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
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
		h.SourceID, nullTime(h.LastAttemptAt), nullTime(h.LastSuccessAt),
		nullTime(h.LastFailureAt), h.LastError, h.ConsecutiveFailures,
		h.TotalAttempts, h.TotalSuccesses, h.TotalItems, h.TotalNewItems,
		h.ETag, h.LastModified)
	if err != nil {
		return fmt.Errorf("postgres: save source health for %s: %w", h.SourceID, err)
	}
	return nil
}

// LoadSourceHealth returns the stored health of every source.
func (d *DB) LoadSourceHealth(ctx context.Context) ([]news.SourceHealth, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT source_id, last_attempt_at, last_success_at, last_failure_at, last_error,
       consecutive_failures, total_attempts, total_successes, total_items,
       total_new_items, etag, last_modified
FROM source_health ORDER BY source_id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: load source health: %w", err)
	}
	defer rows.Close()

	var out []news.SourceHealth
	for rows.Next() {
		var (
			h                         news.SourceHealth
			attempt, success, failure sql.NullTime
		)
		if err := rows.Scan(&h.SourceID, &attempt, &success, &failure, &h.LastError,
			&h.ConsecutiveFailures, &h.TotalAttempts, &h.TotalSuccesses,
			&h.TotalItems, &h.TotalNewItems, &h.ETag, &h.LastModified); err != nil {
			return nil, fmt.Errorf("postgres: scan source health: %w", err)
		}
		h.LastAttemptAt = timeOrZero(attempt)
		h.LastSuccessAt = timeOrZero(success)
		h.LastFailureAt = timeOrZero(failure)
		out = append(out, h)
	}
	return out, rows.Err()
}

// ClusterCandidates returns recent events a new item might belong to. Symbol
// matches and entity-less matches are fetched with separate budgets: in one
// query the entity-less arm can fill the whole result during a backfill and
// crowd out the real match.
func (d *DB) ClusterCandidates(ctx context.Context, symbols []string, titleKey string, since time.Time, limit int) ([]events.Candidate, error) {
	if limit <= 0 {
		limit = 200
	}
	seen := map[int64]bool{}
	var out []events.Candidate

	collect := func(query string, args ...any) error {
		rows, err := d.db.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("postgres: cluster candidates: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				c   events.Candidate
				typ string
			)
			if err := rows.Scan(&c.ID, &typ, &c.Headline, &c.TitleKey, &c.DiscoveredAt, &c.Official); err != nil {
				return fmt.Errorf("postgres: scan cluster candidate: %w", err)
			}
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			c.Type = events.Type(typ)
			c.DiscoveredAt = c.DiscoveredAt.UTC()
			out = append(out, c)
		}
		return rows.Err()
	}

	const cols = `SELECT e.id, e.event_type, e.headline, e.title_key, e.discovered_at, e.official FROM events e `

	// Arm one: events already attached to one of this item's companies.
	if len(symbols) > 0 {
		if err := collect(cols+`
WHERE e.discovered_at >= $1
  AND EXISTS (SELECT 1 FROM event_entities x WHERE x.event_id = e.id AND x.symbol = ANY($2::text[]))
ORDER BY e.discovered_at DESC, e.id DESC LIMIT $3`, since.UTC(), symbols, limit); err != nil {
			return nil, err
		}
	}

	// Arm two: an exact headline-token match. An index lookup, so it stays
	// cheap however large the archive grows, and the only route by which
	// syndicated copy with no resolvable company collapses into one story.
	if titleKey != "" {
		if err := collect(cols+`
WHERE e.discovered_at >= $1 AND e.title_key = $2
ORDER BY e.discovered_at DESC, e.id DESC LIMIT 50`, since.UTC(), titleKey); err != nil {
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
	symsByEvent, err := d.symbolsForEvents(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Symbols = symsByEvent[out[i].ID]
	}
	return out, nil
}

func (d *DB) symbolsForEvents(ctx context.Context, ids []int64) (map[int64][]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT event_id, symbol FROM event_entities WHERE event_id = ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: load event symbols: %w", err)
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
// after a parser fix converges on the same event instead of forking it. The
// insert and the lookup are one statement so two processors racing on the same
// story cannot both create it.
func (d *DB) UpsertEvent(ctx context.Context, e news.Event, titleKey string) (int64, bool, error) {
	var (
		id      int64
		created bool
	)
	err := d.db.QueryRowContext(ctx, `
WITH attempted AS (
    INSERT INTO events
        (fingerprint, event_type, headline, summary, title_key, occurred_at,
         published_at, discovered_at, confirmed_at, updated_at, importance,
         confidence, best_trust, source_count, official)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
    ON CONFLICT (fingerprint) DO NOTHING
    RETURNING id
)
SELECT id, true FROM attempted
UNION ALL
SELECT id, false FROM events WHERE fingerprint = $1 AND NOT EXISTS (SELECT 1 FROM attempted)
LIMIT 1`,
		e.Fingerprint, e.Type, e.Headline, e.Summary, titleKey,
		nullTime(e.OccurredAt), nullTime(e.PublishedAt), e.DiscoveredAt.UTC(),
		nullTime(e.ConfirmedAt), e.UpdatedAt.UTC(), nullInt(e.Importance),
		nullFloat(e.Confidence), e.BestTrust, e.SourceCount, e.Official).Scan(&id, &created)
	if err != nil {
		return 0, false, fmt.Errorf("postgres: upsert event: %w", err)
	}
	return id, created, nil
}

// AttachEvidence links a raw item to an event.
func (d *DB) AttachEvidence(ctx context.Context, eventID, rawItemID int64, sourceID string, trust int, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO event_evidence (event_id, raw_item_id, source_id, trust, added_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (event_id, raw_item_id) DO NOTHING`,
		eventID, rawItemID, sourceID, trust, at.UTC())
	if err != nil {
		return fmt.Errorf("postgres: attach evidence: %w", err)
	}
	return nil
}

// TouchEvent recomputes the aggregates that change as evidence accumulates.
//
// Derived in SQL from the evidence table rather than incremented in Go, so the
// numbers cannot drift: source_count, best_trust and confirmed_at are always
// exactly what the evidence supports, even if a process died halfway through
// attaching some.
func (d *DB) TouchEvent(ctx context.Context, eventID int64, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE events e SET
    source_count = agg.sources,
    best_trust   = GREATEST(e.best_trust, COALESCE(agg.max_trust, 0)),
    official     = e.official OR agg.has_official,
    confirmed_at = COALESCE(e.confirmed_at, agg.first_official_at),
    updated_at   = $2
FROM (
    SELECT count(DISTINCT ev.source_id)                             AS sources,
           max(ev.trust)                                            AS max_trust,
           bool_or(ev.trust >= 100)                                 AS has_official,
           min(ri.discovered_at) FILTER (WHERE ev.trust >= 100)     AS first_official_at
    FROM event_evidence ev
    JOIN raw_items ri ON ri.id = ev.raw_item_id
    WHERE ev.event_id = $1
) AS agg
WHERE e.id = $1`, eventID, at.UTC())
	if err != nil {
		return fmt.Errorf("postgres: touch event %d: %w", eventID, err)
	}
	return nil
}

// UpsertEventEntities records which companies an event concerns.
//
// A stronger match replaces a weaker one for the same symbol, so a news report
// that guessed at a company is corrected when the exchange filing names it.
// Model-supplied direction is preserved: this path only ever writes matching
// evidence and must not erase a reading the classifier already made.
func (d *DB) UpsertEventEntities(ctx context.Context, eventID int64, entities []news.EventEntity) error {
	if len(entities) == 0 {
		return nil
	}
	n := len(entities)
	symbols := make([]string, 0, n)
	rels := make([]string, 0, n)
	confs := make([]float64, 0, n)
	methods := make([]string, 0, n)
	for _, e := range entities {
		rel := e.Relationship
		if rel == "" {
			rel = news.RelMentioned
		}
		symbols = append(symbols, e.Symbol)
		rels = append(rels, string(rel))
		confs = append(confs, e.MatchConfidence)
		methods = append(methods, e.MatchMethod)
	}
	_, err := d.db.ExecContext(ctx, `
INSERT INTO event_entities (event_id, symbol, relationship, match_confidence, match_method)
SELECT $1, s, r::entity_relationship, c, m
FROM unnest($2::text[], $3::text[], $4::real[], $5::text[]) AS t(s, r, c, m)
ON CONFLICT (event_id, symbol) DO UPDATE SET
    relationship     = CASE WHEN excluded.match_confidence > event_entities.match_confidence
                            THEN excluded.relationship ELSE event_entities.relationship END,
    match_method     = CASE WHEN excluded.match_confidence > event_entities.match_confidence
                            THEN excluded.match_method ELSE event_entities.match_method END,
    match_confidence = GREATEST(event_entities.match_confidence, excluded.match_confidence)`,
		eventID, symbols, rels, confs, methods)
	if err != nil {
		return fmt.Errorf("postgres: upsert event entities: %w", err)
	}
	return nil
}

// SaveEventFacts stores the structured values pulled out of a filing.
func (d *DB) SaveEventFacts(ctx context.Context, eventID int64, facts map[string]string) error {
	if len(facts) == 0 {
		return nil
	}
	keys := make([]string, 0, len(facts))
	values := make([]string, 0, len(facts))
	nums := make([]sql.NullFloat64, 0, len(facts))
	for k, v := range facts {
		keys = append(keys, k)
		values = append(values, v)
		f, ok := parseFactNumber(v)
		nums = append(nums, sql.NullFloat64{Float64: f, Valid: ok})
	}
	_, err := d.db.ExecContext(ctx, `
INSERT INTO event_facts (event_id, key, value, num)
SELECT $1, k, v, n FROM unnest($2::text[], $3::text[], $4::double precision[]) AS t(k, v, n)
ON CONFLICT (event_id, key) DO UPDATE SET value = excluded.value, num = excluded.num`,
		eventID, keys, values, nums)
	if err != nil {
		return fmt.Errorf("postgres: save event facts: %w", err)
	}
	return nil
}

// parseFactNumber reads a numeric fact value, tolerating a percent sign.
//
// A non-numeric value stores NULL rather than zero, because "not a number" and
// "zero" are different readings and a later query that averages them must not
// silently include the former.
func parseFactNumber(v string) (float64, bool) {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "%"))
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// SaveEventSectors records which industries an event bears on.
func (d *DB) SaveEventSectors(ctx context.Context, eventID int64, sectors []string) error {
	if len(sectors) == 0 {
		return nil
	}
	_, err := d.db.ExecContext(ctx, `
INSERT INTO event_sectors (event_id, sector)
SELECT $1, s FROM unnest($2::text[]) AS s
ON CONFLICT (event_id, sector) DO NOTHING`, eventID, sectors)
	if err != nil {
		return fmt.Errorf("postgres: save event sectors: %w", err)
	}
	return nil
}

// EventSectors returns the industries an event bears on.
func (d *DB) EventSectors(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT sector FROM event_sectors WHERE event_id = $1 ORDER BY sector`, eventID)
	if err != nil {
		return nil, fmt.Errorf("postgres: event sectors: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// EventFacts returns the structured values extracted for an event.
func (d *DB) EventFacts(ctx context.Context, eventID int64) (map[string]string, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT key, value FROM event_facts WHERE event_id = $1 ORDER BY key`, eventID)
	if err != nil {
		return nil, fmt.Errorf("postgres: event facts: %w", err)
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

// SaveEventClassification records the model's reading of an event.
//
// Transactional across the event row and its entities, because a half-applied
// classification is worse than none: an event marked classified but missing
// its per-company directions would never be picked up again by the pending
// query, and the gap would be invisible.
func (d *DB) SaveEventClassification(ctx context.Context, c events.Classification) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: begin save classification: %w", err)
	}
	defer tx.Rollback()

	summary := strings.TrimSpace(c.Summary)
	if c.WhyItMatters != "" {
		if summary != "" {
			summary += " "
		}
		summary += strings.TrimSpace(c.WhyItMatters)
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE events SET
    event_type    = COALESCE(NULLIF($1, ''), event_type),
    summary       = COALESCE(NULLIF($2, ''), summary),
    importance    = COALESCE($3, importance),
    confidence    = COALESCE($4, confidence),
    classified_at = $5,
    model         = $6,
    updated_at    = $5
WHERE id = $7`,
		c.Type, summary, nullInt(c.Importance), nullFloat(c.Confidence),
		c.ClassifiedAt.UTC(), c.Model, c.EventID); err != nil {
		return fmt.Errorf("postgres: update event %d: %w", c.EventID, err)
	}

	if len(c.Entities) > 0 {
		n := len(c.Entities)
		symbols := make([]string, 0, n)
		rels := make([]string, 0, n)
		dirs := make([]string, 0, n)
		impacts := make([]float64, 0, n)
		rationales := make([]string, 0, n)
		for _, e := range c.Entities {
			rel := e.Relationship
			if rel == "" {
				rel = news.RelMentioned
			}
			dir := e.Direction
			if dir == "" {
				dir = news.DirectionUnclear
			}
			symbols = append(symbols, e.Symbol)
			rels = append(rels, string(rel))
			dirs = append(dirs, string(dir))
			impacts = append(impacts, e.ImpactStrength)
			rationales = append(rationales, e.Rationale)
		}
		// The model may name a company the resolver did not, so this inserts
		// as well as updates. match_confidence stays at zero for those: they
		// were asserted by a model rather than matched against the listed
		// master, and the two must remain distinguishable.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO event_entities
    (event_id, symbol, relationship, match_confidence, match_method,
     direction, impact_strength, rationale)
SELECT $1, s, r::entity_relationship, 0, 'model', d::event_direction, i, rat
FROM unnest($2::text[], $3::text[], $4::text[], $5::real[], $6::text[])
     AS t(s, r, d, i, rat)
ON CONFLICT (event_id, symbol) DO UPDATE SET
    direction       = excluded.direction,
    impact_strength = excluded.impact_strength,
    rationale       = CASE WHEN excluded.rationale <> '' THEN excluded.rationale
                           ELSE event_entities.rationale END,
    relationship    = CASE WHEN event_entities.match_confidence = 0
                           THEN excluded.relationship ELSE event_entities.relationship END`,
			c.EventID, symbols, rels, dirs, impacts, rationales); err != nil {
			return fmt.Errorf("postgres: save entity readings: %w", err)
		}
	}
	return tx.Commit()
}

// ResetEvents discards every derived event and reopens the raw items that
// produced them, so a parser or clustering fix can be applied to the whole
// archive.
func (d *DB) ResetEvents(ctx context.Context) (int, error) {
	before, err := d.CountEvents(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("postgres: begin reset events: %w", err)
	}
	defer tx.Rollback()

	// TRUNCATE with CASCADE removes the dependent rows and resets the
	// identity sequences in one statement, which a DELETE would not.
	if _, err := tx.ExecContext(ctx,
		`TRUNCATE events, event_evidence, event_entities, event_facts, event_sectors RESTART IDENTITY CASCADE`); err != nil {
		return 0, fmt.Errorf("postgres: truncate derived events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE raw_items SET processed_at = NULL`); err != nil {
		return 0, fmt.Errorf("postgres: reopen raw items: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("postgres: commit reset events: %w", err)
	}
	return before, nil
}

// webURL keeps only http(s) links. A feed is untrusted input, and a
// "javascript:" link stored here would run in the reader's session on click.
func webURL(u string) string {
	l := strings.ToLower(strings.TrimSpace(u))
	if strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") {
		return strings.TrimSpace(u)
	}
	return ""
}
