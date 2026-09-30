package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/storage"
)

// EventFilter is re-exported from the storage package so callers do not need
// to know which implementation is in use.
type EventFilter = storage.EventFilter

const eventColumns = `e.id, e.fingerprint, e.event_type, e.headline, e.summary,
       e.occurred_at, e.published_at, e.discovered_at, e.confirmed_at, e.updated_at,
       e.importance, e.confidence, e.best_trust, e.source_count, e.official,
       e.classified_at, e.model,
       -- The best link among this event's evidence, chosen by source trust so
       -- a reader lands on the exchange filing rather than a rewrite of it.
       -- A lateral join keeps this one index lookup per row instead of a
       -- second query per event.
       COALESCE(link.url, '') AS primary_url,
       COALESCE(link.trust_kind, '') AS timestamp_trust,
       -- Who that best link came from. Without these the feed could show a
       -- headline and a link but never say who reported it -- which, for an
       -- app whose argument is provenance, is the one thing a row must say.
       COALESCE(link.source_id, '') AS primary_source_id,
       COALESCE(link.publisher, '') AS primary_publisher`

const eventJoin = `
LEFT JOIN LATERAL (
    SELECT ri.url, ee.source_id, ri.publisher,
           -- Whether the publication time on this event may be believed.
           -- Derived from which source supplied it rather than stored twice,
           -- so the two cannot disagree.
           CASE
               WHEN ee.source_id LIKE 'disc-%' OR ee.source_id LIKE 'watch-%'
                    OR ee.source_id LIKE 'gdelt%' THEN 'observed'
               WHEN ee.trust >= 100 THEN 'exact'
               ELSE 'publisher'
           END AS trust_kind
    FROM event_evidence ee
    JOIN raw_items ri ON ri.id = ee.raw_item_id
    WHERE ee.event_id = e.id AND ri.url <> ''
    ORDER BY ee.trust DESC, ri.discovered_at ASC
    LIMIT 1
) AS link ON TRUE`

// contentAgeExpr is what the feed treats as an item's age: its publication
// time, clamped to discovery whenever that is missing or implausibly later.
//
// Written once because events_content_age_idx (migration 0022) indexes this
// exact expression, and because the filter and the sort must agree. They did
// not: the window was measured on the clamped value while the order was taken
// from a raw COALESCE, so a publisher claiming a future timestamp was held out
// of the window by one rule and floated to the top of the feed by the other.
// The observed skew was small -- 124 events, none off by more than four
// minutes -- but two rules for one question is the kind of disagreement that
// only ever grows.
//
// Postgres matches an expression index by its parsed tree, so any drift
// between this constant and the migration does not fail loudly. It silently
// restores a sequential scan over the whole archive.
const contentAgeExpr = `CASE
                WHEN e.published_at IS NULL THEN e.discovered_at
                WHEN e.published_at > e.discovered_at THEN e.discovered_at
                ELSE e.published_at
             END`

// ListEvents returns events matching a filter, most recently discovered first.
//
// Ordering is by discovery rather than importance so the default view is a
// timeline of what happened. Importance is a filter, not a sort: an operator
// scanning the day wants it in order, and re-ranking by a score would make the
// same feed look different every time the classifier ran.
// indianSymbol matches a symbol listed in India. The product covers US
// equities only; the archive still holds events on Indian listings from
// before that, and every read leaves them out rather than deleting them.
const indianSymbol = `'\.(NSE|BSE)$'`

// usRelevant keeps an event unless everything tying it to a company points
// at an Indian listing: an Indian entity with no other, or evidence that came
// only from an Indian instrument's own watchlist search.
const usRelevant = `NOT (
    EXISTS (SELECT 1 FROM event_entities xi WHERE xi.event_id = e.id AND xi.symbol ~ ` + indianSymbol + `)
    AND NOT EXISTS (SELECT 1 FROM event_entities xu WHERE xu.event_id = e.id AND xu.symbol !~ ` + indianSymbol + `)
) AND NOT (
    EXISTS (SELECT 1 FROM event_evidence wi WHERE wi.event_id = e.id AND wi.source_id ~ '^watch-.*\.(nse|bse)$')
    AND NOT EXISTS (SELECT 1 FROM event_evidence wu WHERE wu.event_id = e.id AND wu.source_id !~ '^watch-.*\.(nse|bse)$')
)`

func (d *DB) ListEvents(ctx context.Context, f EventFilter) ([]news.Event, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var (
		where []string
		args  []any
	)
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	where = append(where, usRelevant)

	if f.Symbol != "" {
		sym := strings.ToUpper(f.Symbol)
		// Two ways an event belongs to an instrument: a resolved entity, or
		// evidence from that instrument's own watchlist search. The second
		// matters for anything outside the NSE master — a foreign holding has
		// no entity to resolve to, and without this its page would be empty
		// while its search quietly collected a hundred items.
		args = append(args, sym, "watch-"+strings.ToLower(sym))
		where = append(where, fmt.Sprintf(`(
    EXISTS (SELECT 1 FROM event_entities x WHERE x.event_id = e.id AND x.symbol = $%d)
    OR EXISTS (SELECT 1 FROM event_evidence ev WHERE ev.event_id = e.id AND ev.source_id = $%d)
)`, len(args)-1, len(args)))
	}
	if len(f.Types) > 0 {
		add(`e.event_type = ANY($%d::text[])`, f.Types)
	}
	if f.MinImportance > 0 {
		add(`COALESCE(e.importance, 0) >= $%d`, f.MinImportance)
	}
	if f.IndexOnly {
		// The exemption is for events that name no company *by nature* — a
		// commodity move, a policy change — and not for those that merely
		// failed to resolve one. Without that distinction the escape hatch
		// swallows the filter whole: an exchange filing whose filer the
		// resolver could not match has no entities either, so every microcap
		// notice the feed was meant to demote comes straight back through it.
		if len(f.MacroTypes) > 0 {
			args = append(args, stringArray(f.MacroTypes))
			where = append(where, fmt.Sprintf(`(
                EXISTS (
                    SELECT 1 FROM event_entities en
                    JOIN index_constituents ic ON ic.symbol = en.symbol
                    WHERE en.event_id = e.id
                )
                OR (
                    e.event_type::TEXT = ANY($%d)
                    AND NOT EXISTS (SELECT 1 FROM event_entities en WHERE en.event_id = e.id)
                )
            )`, len(args)))
		} else {
			where = append(where, `EXISTS (
                SELECT 1 FROM event_entities en
                JOIN index_constituents ic ON ic.symbol = en.symbol
                WHERE en.event_id = e.id
            )`)
		}
	}

	if !f.Since.IsZero() {
		// The window is about content age, not discovery.
		//
		// A watchlist search returns a publisher's back catalogue, so one poll
		// can discover a hundred articles at once spanning years. Filtering on
		// discovery alone put a January 2025 story into a "last 24 hours" feed
		// — measured on one company, twelve items discovered in a single batch
		// carried publication dates spread over nineteen months, several rated
		// importance 8. That is the same defect as showing a March article as
		// today's news, and it is what makes a feed untrustworthy.
		//
		// published_at is used only when it is not after discovery. Aggregators
		// report their own surfacing time, and one that claims to be from the
		// future has told us nothing about the article's age.
		add(contentAgeExpr+` >= $%d`, f.Since.UTC())
	}
	if f.OfficialOnly {
		where = append(where, `e.official`)
	}
	if !f.IncludeUnattributedWatchlist {
		// Excluded: events with no resolved company whose evidence came only
		// from watchlist searches. Those are per-instrument results for
		// holdings outside the listed master, and they belong on that
		// instrument's page rather than in the market feed.
		where = append(where, `(
    EXISTS (SELECT 1 FROM event_entities x WHERE x.event_id = e.id)
    OR EXISTS (
        SELECT 1 FROM event_evidence ev
        WHERE ev.event_id = e.id AND ev.source_id NOT LIKE 'watch-%'
    )
)`)
	}
	if len(f.Sectors) > 0 {
		add(`EXISTS (SELECT 1 FROM event_sectors s WHERE s.event_id = e.id AND s.sector = ANY($%d::text[]))`,
			f.Sectors)
	}

	// Full-text search rather than a LIKE scan. websearch_to_tsquery accepts
	// what a person actually types — quoted phrases, OR, leading minus — and
	// never errors on malformed input, which matters when the query comes
	// straight from a search box.
	orderBy := `ORDER BY e.discovered_at DESC, e.id DESC`
	if f.OrderByContentAge {
		// The same expression the window is measured with, so an item cannot
		// be dated one way for inclusion and another way for position. An
		// undated item still sorts by discovery, which is the most charitable
		// assumption available and keeps it in the running.
		orderBy = `ORDER BY ` + contentAgeExpr + ` DESC, e.id DESC`
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, q)
		idx := len(args)
		where = append(where, fmt.Sprintf(`e.search_vector @@ websearch_to_tsquery('english', $%d)`, idx))
		// When the operator is searching, relevance is the useful order and
		// recency is the tiebreak. When browsing, it is the reverse.
		orderBy = fmt.Sprintf(
			`ORDER BY ts_rank(e.search_vector, websearch_to_tsquery('english', $%d)) DESC, e.discovered_at DESC`, idx)
	}

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit, f.Offset)

	query := fmt.Sprintf(`SELECT %s FROM events e %s %s %s LIMIT $%d OFFSET $%d`,
		eventColumns, eventJoin, clause, orderBy, len(args)-1, len(args))

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list events: %w", err)
	}
	defer rows.Close()

	out, ids, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	if err := d.attachEntities(ctx, out, ids); err != nil {
		return nil, err
	}
	if err := d.attachSectors(ctx, out, ids); err != nil {
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
			occurred, published, confirmed sql.NullTime
			classified                     sql.NullTime
			importance                     sql.NullInt64
			confidence                     sql.NullFloat64
			trustKind                      string
		)
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.Type, &e.Headline, &e.Summary,
			&occurred, &published, &e.DiscoveredAt, &confirmed, &e.UpdatedAt,
			&importance, &confidence, &e.BestTrust, &e.SourceCount, &e.Official,
			&classified, &e.Model, &e.PrimaryURL, &trustKind,
			&e.PrimarySourceID, &e.PrimaryPublisher); err != nil {
			return nil, nil, fmt.Errorf("postgres: scan event: %w", err)
		}
		e.OccurredAt = timeOrZero(occurred)
		e.PublishedAt = timeOrZero(published)
		e.ConfirmedAt = timeOrZero(confirmed)
		e.ClassifiedAt = timeOrZero(classified)
		e.DiscoveredAt = e.DiscoveredAt.UTC()
		e.UpdatedAt = e.UpdatedAt.UTC()
		e.TimestampTrust = news.TimestampTrust(trustKind)
		if importance.Valid {
			v := int(importance.Int64)
			e.Importance = &v
		}
		if confidence.Valid {
			v := confidence.Float64
			e.Confidence = &v
		}
		out = append(out, e)
		ids = append(ids, e.ID)
	}
	return out, ids, rows.Err()
}

// attachEntities loads every event's companies in one query rather than one
// per event, which is what keeps a hundred-row feed to two round trips.
func (d *DB) attachEntities(ctx context.Context, list []news.Event, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT event_id, symbol, relationship::text, match_confidence, match_method,
       direction::text, impact_strength, rationale
FROM event_entities
WHERE event_id = ANY($1::bigint[]) AND symbol !~ `+indianSymbol+`
ORDER BY match_confidence DESC, symbol`, ids)
	if err != nil {
		return fmt.Errorf("postgres: load event entities: %w", err)
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
			return fmt.Errorf("postgres: scan event entity: %w", err)
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

// attachSectors fills in the industries each event reaches, the same
// batch-by-id shape as attachEntities. Only events with a SectorScope type
// ever have rows in event_sectors, so this is a no-op for the (large)
// majority of company-specific events.
func (d *DB) attachSectors(ctx context.Context, list []news.Event, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT event_id, sector FROM event_sectors WHERE event_id = ANY($1::bigint[]) ORDER BY sector`, ids)
	if err != nil {
		return fmt.Errorf("postgres: load event sectors: %w", err)
	}
	defer rows.Close()

	byEvent := map[int64][]string{}
	for rows.Next() {
		var id int64
		var sector string
		if err := rows.Scan(&id, &sector); err != nil {
			return fmt.Errorf("postgres: scan event sector: %w", err)
		}
		byEvent[id] = append(byEvent[id], sector)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range list {
		list[i].Sectors = byEvent[list[i].ID]
	}
	return nil
}

// GetEvent returns one event with all its evidence attached.
func (d *DB) GetEvent(ctx context.Context, id int64) (news.Event, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events e `+eventJoin+` WHERE e.id = $1`, id)
	if err != nil {
		return news.Event{}, fmt.Errorf("postgres: get event: %w", err)
	}
	list, ids, err := scanEvents(rows)
	rows.Close()
	if err != nil {
		return news.Event{}, err
	}
	if len(list) == 0 {
		return news.Event{}, sql.ErrNoRows
	}
	if err := d.attachEntities(ctx, list, ids); err != nil {
		return news.Event{}, err
	}
	if err := d.attachSectors(ctx, list, ids); err != nil {
		return news.Event{}, err
	}

	ev, err := d.db.QueryContext(ctx, `
SELECT `+rawItemColumnsQualified+`
FROM event_evidence ee
JOIN raw_items ri ON ri.id = ee.raw_item_id
WHERE ee.event_id = $1
ORDER BY ee.trust DESC, ri.discovered_at ASC`, id)
	if err != nil {
		return news.Event{}, fmt.Errorf("postgres: load evidence: %w", err)
	}
	defer ev.Close()
	evidence, err := scanRawItems(ev)
	if err != nil {
		return news.Event{}, err
	}
	list[0].Evidence = evidence
	return list[0], nil
}

// CountEvents reports how many events are held.
func (d *DB) CountEvents(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count events: %w", err)
	}
	return n, nil
}

// ListUnclassifiedEvents returns events the model has not processed, most
// important first so a limited token budget is spent where it counts.
func (d *DB) ListUnclassifiedEvents(ctx context.Context, limit int) ([]news.Event, error) {
	if limit <= 0 {
		limit = 40
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT `+eventColumns+` FROM events e `+eventJoin+`
WHERE e.classified_at IS NULL
ORDER BY
    -- Index constituents first.
    --
    -- Classification is by far the largest consumer of the token budget:
    -- measured over one month it took 1,546,726 of 2,000,000 tokens, and the
    -- queue it was working through is dominated by routine filings from the
    -- long tail of listed companies. Ordering by importance alone cannot see
    -- that, because a board meeting notice is a genuine importance-4 event
    -- whoever files it. Spending three quarters of a month of budget
    -- classifying notices nobody will read, and then exhausting it before the
    -- month ends, is the worst of both outcomes.
    (EXISTS (
        SELECT 1 FROM event_entities en
        JOIN index_constituents ic ON ic.symbol = en.symbol
        WHERE en.event_id = e.id
    )) DESC,
    e.official DESC,
    e.importance DESC NULLS LAST, e.best_trust DESC, e.discovered_at DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list unclassified events: %w", err)
	}
	defer rows.Close()

	out, ids, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	if err := d.attachEntities(ctx, out, ids); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchEvents is the full-text search the research engine uses.
//
// It deliberately does not reuse the feed's search. websearch_to_tsquery joins
// terms with AND, which is right for a search box — someone typing "reliance
// dividend" wants both — but wrong for a research question. "Which Indian
// cement companies are exposed to the coal price?" required every one of those
// words in a single headline and returned nothing, while an OR of the content
// words returned nineteen.
//
// So the question is reduced to its content words, joined with OR, and ranked:
// documents matching more terms sort first, which is what makes recall safe.
//
// It is also a purpose-shaped method rather than an exposed filter, because
// importing the storage package into research would close an import cycle.
func (d *DB) SearchEvents(ctx context.Context, query string, since time.Time, limit int) ([]news.Event, error) {
	terms := contentWords(query)
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// to_tsquery needs a well-formed expression, so the terms are normalised
	// and joined here rather than handed a raw string that could fail to
	// parse. Each is lexeme-prefixed so "cement" also reaches "cements".
	expr := strings.Join(terms, " | ")

	rows, err := d.db.QueryContext(ctx, `
SELECT `+eventColumns+` FROM events e `+eventJoin+`
WHERE e.discovered_at >= $1
  AND e.search_vector @@ to_tsquery('english', $2)
ORDER BY ts_rank(e.search_vector, to_tsquery('english', $2)) DESC,
         e.official DESC,
         e.discovered_at DESC
LIMIT $3`, since.UTC(), expr, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: search events: %w", err)
	}
	defer rows.Close()

	out, ids, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	if err := d.attachEntities(ctx, out, ids); err != nil {
		return nil, err
	}
	return out, nil
}

// questionWords carry no meaning in a search and would match everything.
var questionWords = map[string]bool{
	"which": true, "what": true, "who": true, "when": true, "where": true,
	"why": true, "how": true, "is": true, "are": true, "was": true, "were": true,
	"the": true, "a": true, "an": true, "of": true, "to": true, "in": true,
	"on": true, "for": true, "and": true, "or": true, "by": true, "with": true,
	"about": true, "from": true, "at": true, "as": true, "that": true,
	"this": true, "it": true, "its": true, "do": true, "does": true, "did": true,
	"has": true, "have": true, "had": true, "can": true, "could": true,
	"any": true, "all": true, "there": true, "their": true, "them": true,
	"companies": true, "company": true, "stock": true, "stocks": true,
	"share": true, "shares": true, "india": true, "indian": true,
}

// contentWords reduces a question to searchable lexemes.
//
// Words that appear in nearly every document — "companies", "stocks",
// "Indian" — are dropped along with grammar. Left in, they dominate the rank
// and every result looks equally relevant.
func contentWords(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Fields(strings.ToLower(q)) {
		var b strings.Builder
		for _, r := range raw {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		w := b.String()
		if len(w) < 3 || questionWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) >= 12 {
			break
		}
	}
	return out
}
