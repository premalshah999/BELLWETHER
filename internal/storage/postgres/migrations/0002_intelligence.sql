-- The intelligence schema: raw evidence, events, entities, sectors, facts.
--
-- The organising idea is unchanged from the first implementation — the central
-- object is an EVENT, and articles and filings are EVIDENCE for it — but the
-- structure now enforces it rather than merely intending it.

-- Direction is a real enumeration because "unclear" is a first-class answer.
-- Modelling it as a signed number would force every genuinely ambiguous event
-- to be recorded as zero, which reads as "no effect" — a different and wrong
-- claim. Three values, and no way to write a fourth.
DO $$ BEGIN
    CREATE TYPE event_direction AS ENUM ('positive', 'negative', 'unclear');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE entity_relationship AS ENUM ('primary', 'mentioned', 'peer', 'sector');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- raw_items is the immutable record of everything ever fetched.
--
-- Nothing downstream may modify a row here beyond marking it processed. Every
-- derived artefact is reproducible from this and only from this, which is what
-- makes a bad parser or a bad prompt a recoverable mistake rather than
-- permanent data loss. Four separate defects have been repaired retroactively
-- by rebuilding from this table.
CREATE TABLE IF NOT EXISTS raw_items (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id     TEXT        NOT NULL,
    content_hash  TEXT        NOT NULL,
    url           TEXT        NOT NULL DEFAULT '',
    canonical_url TEXT        NOT NULL DEFAULT '',
    title         TEXT        NOT NULL DEFAULT '',
    description   TEXT        NOT NULL DEFAULT '',
    publisher     TEXT        NOT NULL DEFAULT '',
    payload       TEXT        NOT NULL DEFAULT '',

    -- The three timestamps, kept apart deliberately.
    --
    --   occurred_at   when the event itself happened. Usually unknown for an
    --                 article; often stated inside a filing, and frequently in
    --                 the future for calendar items like a record date.
    --   published_at  when the source says it published. Nullable because
    --                 sources omit it, round it, and occasionally lie.
    --   discovered_at when THIS system first held the bytes. NOT NULL because
    --                 it is the only one we can vouch for, and therefore the
    --                 only one a strategy may treat as its knowledge time.
    --
    -- Conflating discovered_at with published_at is how a backtest quietly
    -- learns the future, so the schema refuses to let the honest one be null.
    occurred_at   TIMESTAMPTZ,
    published_at  TIMESTAMPTZ,
    discovered_at TIMESTAMPTZ NOT NULL,
    fetched_at    TIMESTAMPTZ NOT NULL,
    processed_at  TIMESTAMPTZ,

    CONSTRAINT raw_items_identity UNIQUE (source_id, content_hash),
    -- An item with neither a title nor a description carries no information
    -- and cannot become an event. NSE emits some of its most valuable notices
    -- with no link at all, so a URL is deliberately not required.
    CONSTRAINT raw_items_has_substance CHECK (title <> '' OR description <> '')
);

-- The work queue and the evidence archive are the same rows: an item is
-- pending until processed, and permanent afterwards. A partial index keeps the
-- queue lookup proportional to the backlog rather than to the whole archive,
-- which is the difference between a constant-time claim and one that degrades
-- as history accumulates.
CREATE INDEX IF NOT EXISTS raw_items_pending_idx
    ON raw_items (discovered_at, id) WHERE processed_at IS NULL;

CREATE INDEX IF NOT EXISTS raw_items_discovered_idx ON raw_items (discovered_at DESC);
CREATE INDEX IF NOT EXISTS raw_items_source_idx ON raw_items (source_id, discovered_at DESC);
CREATE INDEX IF NOT EXISTS raw_items_canonical_idx
    ON raw_items (canonical_url) WHERE canonical_url <> '';

CREATE TABLE IF NOT EXISTS events (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    fingerprint   TEXT        NOT NULL UNIQUE,
    event_type    TEXT        NOT NULL DEFAULT 'UNCLASSIFIED',
    headline      TEXT        NOT NULL,
    summary       TEXT        NOT NULL DEFAULT '',
    title_key     TEXT        NOT NULL DEFAULT '',

    occurred_at   TIMESTAMPTZ,
    published_at  TIMESTAMPTZ,
    discovered_at TIMESTAMPTZ NOT NULL,
    -- The interval between discovery and confirmation is itself informative:
    -- a report the company files against twenty minutes later is a different
    -- animal from one that is never confirmed at all.
    confirmed_at  TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Importance and direction are separate by design. A chief executive
    -- resigning without explanation is a 9 whose direction is genuinely
    -- unclear, so importance lives here and direction lives per company.
    importance    SMALLINT,
    confidence    REAL,
    best_trust    SMALLINT    NOT NULL DEFAULT 0,
    source_count  INTEGER     NOT NULL DEFAULT 0,
    official      BOOLEAN     NOT NULL DEFAULT FALSE,

    classified_at TIMESTAMPTZ,
    model         TEXT        NOT NULL DEFAULT '',

    CONSTRAINT events_importance_range CHECK (importance IS NULL OR importance BETWEEN 0 AND 10),
    CONSTRAINT events_confidence_range CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    CONSTRAINT events_trust_range CHECK (best_trust BETWEEN 0 AND 100),
    CONSTRAINT events_headline_present CHECK (headline <> '')
);

CREATE INDEX IF NOT EXISTS events_discovered_idx ON events (discovered_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS events_type_idx ON events (event_type, discovered_at DESC);
CREATE INDEX IF NOT EXISTS events_importance_idx ON events (importance DESC NULLS LAST, discovered_at DESC);
CREATE INDEX IF NOT EXISTS events_title_key_idx ON events (title_key, discovered_at DESC) WHERE title_key <> '';

-- The classification queue, ordered the way the classifier drains it: most
-- important first, so a limited token budget is spent where it counts.
CREATE INDEX IF NOT EXISTS events_unclassified_idx
    ON events (importance DESC NULLS LAST, best_trust DESC, discovered_at DESC)
    WHERE classified_at IS NULL;

-- Full-text search over the feed, replacing a LIKE '%term%' scan.
--
-- The vector is a generated column rather than a trigger-maintained one, so it
-- cannot drift out of step with the text it indexes. Weighting the headline
-- above the summary means a company named in the headline outranks one
-- mentioned in passing.
ALTER TABLE events ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(headline, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(summary, '')), 'B')
    ) STORED;

CREATE INDEX IF NOT EXISTS events_search_idx ON events USING GIN (search_vector);

CREATE TABLE IF NOT EXISTS event_evidence (
    event_id    BIGINT      NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    raw_item_id BIGINT      NOT NULL REFERENCES raw_items (id) ON DELETE CASCADE,
    source_id   TEXT        NOT NULL,
    trust       SMALLINT    NOT NULL DEFAULT 0,
    added_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, raw_item_id),
    CONSTRAINT event_evidence_trust_range CHECK (trust BETWEEN 0 AND 100)
);

CREATE INDEX IF NOT EXISTS event_evidence_item_idx ON event_evidence (raw_item_id);

CREATE TABLE IF NOT EXISTS event_entities (
    event_id         BIGINT              NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    symbol           TEXT                NOT NULL,
    relationship     entity_relationship NOT NULL DEFAULT 'mentioned',
    match_confidence REAL                NOT NULL DEFAULT 0,
    match_method     TEXT                NOT NULL DEFAULT '',

    -- Filled by the model. Direction is per company because the same event
    -- points opposite ways for different companies: crude rising is good for
    -- ONGC and bad for IndiGo. An event-level column would have to pick one.
    direction        event_direction,
    impact_strength  REAL,
    rationale        TEXT                NOT NULL DEFAULT '',

    PRIMARY KEY (event_id, symbol),
    CONSTRAINT event_entities_confidence_range CHECK (match_confidence >= 0 AND match_confidence <= 1),
    CONSTRAINT event_entities_impact_range CHECK (impact_strength IS NULL OR (impact_strength >= 0 AND impact_strength <= 1))
);

CREATE INDEX IF NOT EXISTS event_entities_symbol_idx ON event_entities (symbol, event_id DESC);

-- Sector reach lives on the event rather than being fanned out to companies.
-- A repo-rate decision touches all 121 listed financial companies; writing 121
-- entity rows would bury the handful of events that genuinely name a company,
-- and would make "news about RELIANCE" return every macro story about energy.
CREATE TABLE IF NOT EXISTS event_sectors (
    event_id BIGINT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    sector   TEXT   NOT NULL,
    PRIMARY KEY (event_id, sector)
);

CREATE INDEX IF NOT EXISTS event_sectors_sector_idx ON event_sectors (sector, event_id DESC);

-- Facts extracted from a filing without a model: a dividend per share, a
-- promoter holding percentage, a meeting date. Sparse, so they live in their
-- own table; queried by name across companies and quarters in Phase 2, which
-- a JSON blob on the event would make expensive.
CREATE TABLE IF NOT EXISTS event_facts (
    event_id BIGINT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    key      TEXT   NOT NULL,
    value    TEXT   NOT NULL,
    -- num holds the numeric reading when the value is a number, and is NULL
    -- when it is not — which is a different thing from zero, and a query that
    -- averages these must not silently include the former.
    num      DOUBLE PRECISION,
    PRIMARY KEY (event_id, key)
);

CREATE INDEX IF NOT EXISTS event_facts_key_idx ON event_facts (key, num) WHERE num IS NOT NULL;

CREATE TABLE IF NOT EXISTS source_health (
    source_id            TEXT        PRIMARY KEY,
    last_attempt_at      TIMESTAMPTZ,
    last_success_at      TIMESTAMPTZ,
    last_failure_at      TIMESTAMPTZ,
    last_error           TEXT        NOT NULL DEFAULT '',
    consecutive_failures INTEGER     NOT NULL DEFAULT 0,
    total_attempts       BIGINT      NOT NULL DEFAULT 0,
    total_successes      BIGINT      NOT NULL DEFAULT 0,
    total_items          BIGINT      NOT NULL DEFAULT 0,
    total_new_items      BIGINT      NOT NULL DEFAULT 0,
    -- Conditional-request tokens. Sending these back is the difference
    -- between being a well-behaved consumer of a free feed and being blocked.
    etag                 TEXT        NOT NULL DEFAULT '',
    last_modified        TEXT        NOT NULL DEFAULT '',
    CONSTRAINT source_health_counters_nonneg CHECK (
        consecutive_failures >= 0 AND total_attempts >= 0 AND
        total_successes >= 0 AND total_items >= 0 AND total_new_items >= 0
    )
);
