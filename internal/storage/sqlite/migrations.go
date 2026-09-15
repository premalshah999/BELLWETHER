package sqlite

// migrations are applied in order and recorded in schema_migrations. Never edit
// a migration that has shipped; append a new one instead.
var migrations = []struct {
	Name string
	SQL  string
}{
	{
		Name: "0001_price_cache",
		SQL: `
CREATE TABLE IF NOT EXISTS candles (
    symbol     TEXT    NOT NULL,
    interval   TEXT    NOT NULL,
    ts         INTEGER NOT NULL,        -- bar open, unix seconds UTC
    open       REAL    NOT NULL,
    high       REAL    NOT NULL,
    low        REAL    NOT NULL,
    close      REAL    NOT NULL,
    volume     REAL    NOT NULL,
    source     TEXT    NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (symbol, interval, ts)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_candles_lookup ON candles (symbol, interval, ts DESC);

CREATE TABLE IF NOT EXISTS quotes (
    symbol         TEXT PRIMARY KEY,
    price          REAL NOT NULL,
    prev_close     REAL NOT NULL,
    change         REAL NOT NULL,
    change_percent REAL NOT NULL,
    day_high       REAL NOT NULL,
    day_low        REAL NOT NULL,
    volume         REAL NOT NULL,
    currency       TEXT NOT NULL,
    as_of          INTEGER NOT NULL,
    source         TEXT NOT NULL,
    fetched_at     INTEGER NOT NULL
);
`,
	},
	{
		Name: "0002_watchlist",
		SQL: `
CREATE TABLE IF NOT EXISTS watchlist (
    symbol   TEXT PRIMARY KEY,
    note     TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0,
    added_at INTEGER NOT NULL
);
`,
	},
	{
		Name: "0003_provider_budget_and_health",
		SQL: `
CREATE TABLE IF NOT EXISTS provider_budget (
    provider TEXT    NOT NULL,
    period   TEXT    NOT NULL,          -- e.g. "2026-08-24" for a daily budget
    used     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, period)
);

CREATE TABLE IF NOT EXISTS provider_health (
    provider      TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    status        TEXT NOT NULL,
    message       TEXT NOT NULL DEFAULT '',
    last_ok_at    INTEGER,
    last_error_at INTEGER,
    updated_at    INTEGER NOT NULL
);
`,
	},
	{
		Name: "0004_candle_resolved_symbol",
		SQL: `
-- Records the listing that actually served a bar when a provider substituted a
-- sibling venue, so a cached series stays as honest as a freshly fetched one.
ALTER TABLE candles ADD COLUMN resolved_symbol TEXT NOT NULL DEFAULT '';
`,
	},
	{
		Name: "0005_series_coverage",
		SQL: `
-- How deep we have previously fetched for a symbol and interval. Lets the
-- router tell a shallow cache apart from a short history, so a 30-bar
-- sparkline request cannot starve a 400-bar chart request of its data.
CREATE TABLE IF NOT EXISTS series_coverage (
    symbol          TEXT    NOT NULL,
    interval        TEXT    NOT NULL,
    requested_limit INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (symbol, interval)
);
`,
	},
	{
		Name: "0006_algorithms_and_alerts",
		SQL: `
CREATE TABLE IF NOT EXISTS algorithms (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    definition TEXT    NOT NULL,          -- the full algorithm as JSON
    enabled    INTEGER NOT NULL DEFAULT 0,
    interval   TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_algorithms_enabled ON algorithms (enabled, interval);

CREATE TABLE IF NOT EXISTS alerts (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    algorithm_id   INTEGER NOT NULL,
    algorithm_name TEXT    NOT NULL,      -- denormalised so history survives a rename or delete
    symbol         TEXT    NOT NULL,
    interval       TEXT    NOT NULL,
    fired_at       INTEGER NOT NULL,
    bar_time       INTEGER NOT NULL,
    price          REAL    NOT NULL,
    summary        TEXT    NOT NULL,
    conditions     TEXT    NOT NULL,      -- full evaluation snapshot as JSON
    ai_context     TEXT    NOT NULL DEFAULT '',
    ai_status      TEXT    NOT NULL DEFAULT '',
    delivery       TEXT    NOT NULL DEFAULT '[]',
    read_at        INTEGER
);

CREATE INDEX IF NOT EXISTS idx_alerts_recent ON alerts (fired_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_unread ON alerts (read_at, fired_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_algorithm ON alerts (algorithm_id, fired_at DESC);

-- Per (algorithm, symbol) state. last_fired_at is what enforces the cooldown,
-- and it lives in the database so a restart cannot reset it and re-spam.
CREATE TABLE IF NOT EXISTS algorithm_symbol_state (
    algorithm_id     INTEGER NOT NULL,
    symbol           TEXT    NOT NULL,
    last_fired_at    INTEGER,
    last_evaluated_at INTEGER,
    last_status      TEXT NOT NULL DEFAULT '',
    last_reason      TEXT NOT NULL DEFAULT '',
    last_summary     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (algorithm_id, symbol)
);
`,
	},
	{
		Name: "0007_ai_news_and_search",
		SQL: `
-- Token accounting. One row per billed call, so the settings page can show
-- where a month's budget actually went rather than a single opaque total.
CREATE TABLE IF NOT EXISTS llm_usage (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    period            TEXT    NOT NULL,      -- UTC month, e.g. "2026-08"
    feature           TEXT    NOT NULL,
    model             TEXT    NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens      INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_llm_usage_period ON llm_usage (period);

-- Collected news. Uniqueness is per (symbol, url): the same story reached
-- through two symbols' feeds is stored once per symbol, because relevance and
-- sentiment are per-symbol judgements.
CREATE TABLE IF NOT EXISTS articles (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    symbol       TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    url          TEXT    NOT NULL,
    source       TEXT    NOT NULL DEFAULT '',
    published_at INTEGER,
    fetched_at   INTEGER NOT NULL,
    relevance    REAL,
    sentiment    REAL,
    one_line     TEXT    NOT NULL DEFAULT '',
    score_model  TEXT    NOT NULL DEFAULT '',
    scored_at    INTEGER,
    UNIQUE (symbol, url)
);

CREATE INDEX IF NOT EXISTS idx_articles_symbol ON articles (symbol, published_at DESC, fetched_at DESC);
CREATE INDEX IF NOT EXISTS idx_articles_unscored ON articles (scored_at, fetched_at);

-- Stored AI responses, for the archive page.
CREATE TABLE IF NOT EXISTS ai_outputs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL,
    symbol     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    model      TEXT    NOT NULL DEFAULT '',
    tokens     INTEGER NOT NULL DEFAULT 0,
    content    TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ai_outputs_kind ON ai_outputs (kind, created_at DESC);

-- Logged scenario forecasts and their scoring. This table is the calibration
-- feature: without a durable record of what was predicted, and when, the
-- model's stated confidence can never be checked.
CREATE TABLE IF NOT EXISTS outlooks (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    symbol            TEXT    NOT NULL,
    created_at        INTEGER NOT NULL,
    horizon_days      INTEGER NOT NULL,
    price_at_creation REAL    NOT NULL,

    base_probability  REAL NOT NULL,
    base_move         REAL NOT NULL,
    base_reasoning    TEXT NOT NULL DEFAULT '',
    bull_probability  REAL NOT NULL,
    bull_move         REAL NOT NULL,
    bull_reasoning    TEXT NOT NULL DEFAULT '',
    bear_probability  REAL NOT NULL,
    bear_move         REAL NOT NULL,
    bear_reasoning    TEXT NOT NULL DEFAULT '',

    key_risk          TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',

    resolved_at       INTEGER,
    realized_price    REAL,
    realized_move     REAL,
    actual_scenario   TEXT NOT NULL DEFAULT '',
    brier_score       REAL
);

CREATE INDEX IF NOT EXISTS idx_outlooks_symbol ON outlooks (symbol, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_outlooks_pending ON outlooks (resolved_at, created_at);

-- Search results, cached for an hour.
CREATE TABLE IF NOT EXISTS search_cache (
    key        TEXT PRIMARY KEY,
    query      TEXT NOT NULL,
    provider   TEXT NOT NULL,
    results    TEXT NOT NULL,
    fetched_at INTEGER NOT NULL
);
`,
	},
	{
		Name: "0002_event_intelligence",
		SQL: `
-- The intelligence schema. Its organising idea is that the central object is
-- an EVENT -- something that happened in the market -- and that articles and
-- filings are merely EVIDENCE for it. Eleven stories about one acquisition are
-- one event with eleven pieces of evidence, not eleven rows a reader must
-- deduplicate by eye.

-- raw_items is the immutable record of everything we ever fetched.
--
-- Nothing downstream may modify a row here. If the parser has a bug, if the
-- entity resolver mis-attributes, if the model summarises badly, every one of
-- those can be recomputed from this table -- but only if we still have the
-- bytes. Storing the payload costs a few hundred bytes an item and buys the
-- ability to rebuild the entire derived world.
CREATE TABLE IF NOT EXISTS raw_items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id     TEXT    NOT NULL,
    content_hash  TEXT    NOT NULL,   -- identical re-fetches collapse here
    url           TEXT    NOT NULL DEFAULT '',
    canonical_url TEXT    NOT NULL DEFAULT '',  -- tracking parameters stripped
    title         TEXT    NOT NULL DEFAULT '',
    description   TEXT    NOT NULL DEFAULT '',
    publisher     TEXT    NOT NULL DEFAULT '',
    payload       TEXT    NOT NULL DEFAULT '',  -- the original item, verbatim

    -- The three timestamps. Keeping them apart is what makes an honest
    -- backtest possible later.
    --
    --   occurred_at   when the event itself happened. Usually unknown for a
    --                 news article; sometimes stated inside a filing.
    --   published_at  when the source says it published. Sources omit this,
    --                 round it, and occasionally lie about it, so it is
    --                 nullable and never trusted on its own.
    --   discovered_at when THIS system first held the bytes. It is the only
    --                 timestamp we can vouch for, and therefore the only one
    --                 a strategy may treat as its knowledge time. Conflating
    --                 it with published_at is how a backtest quietly learns
    --                 the future.
    occurred_at   INTEGER,
    published_at  INTEGER,
    discovered_at INTEGER NOT NULL,
    fetched_at    INTEGER NOT NULL,

    UNIQUE (source_id, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_raw_items_discovered ON raw_items (discovered_at DESC);
CREATE INDEX IF NOT EXISTS idx_raw_items_canonical ON raw_items (canonical_url);
CREATE INDEX IF NOT EXISTS idx_raw_items_source ON raw_items (source_id, discovered_at DESC);

-- events is the central object of the whole application.
CREATE TABLE IF NOT EXISTS events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    fingerprint  TEXT    NOT NULL UNIQUE,  -- clustering key; see news.Fingerprint
    event_type   TEXT    NOT NULL DEFAULT 'UNCLASSIFIED',
    headline     TEXT    NOT NULL,
    summary      TEXT    NOT NULL DEFAULT '',

    occurred_at   INTEGER,
    published_at  INTEGER,          -- earliest publication across all evidence
    discovered_at INTEGER NOT NULL, -- when we first held any evidence
    -- confirmed_at is when an exchange or regulator corroborated the event.
    -- The gap between discovered_at and confirmed_at is itself a signal: a
    -- story that never gets confirmed is a different animal from one the
    -- company filed twenty minutes later.
    confirmed_at  INTEGER,
    updated_at    INTEGER NOT NULL,

    -- Importance and direction are deliberately separate columns. A CEO
    -- resigning is a 9/10 event whose direction is genuinely unclear, and a
    -- schema that forces every event onto a bullish/bearish axis would have
    -- to invent an answer.
    importance   INTEGER,          -- 0-10, null until classified
    confidence   REAL,             -- 0-1 in the classification itself
    best_trust   INTEGER NOT NULL DEFAULT 0,  -- highest source trust seen
    source_count INTEGER NOT NULL DEFAULT 0,
    official     INTEGER NOT NULL DEFAULT 0,  -- backed by exchange/regulator

    classified_at INTEGER,          -- when the model last processed this
    model         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_events_discovered ON events (discovered_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_type ON events (event_type, discovered_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_unclassified ON events (classified_at, discovered_at);
CREATE INDEX IF NOT EXISTS idx_events_importance ON events (importance DESC, discovered_at DESC);

-- event_evidence links an event to the raw items supporting it.
CREATE TABLE IF NOT EXISTS event_evidence (
    event_id    INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    raw_item_id INTEGER NOT NULL REFERENCES raw_items (id) ON DELETE CASCADE,
    source_id   TEXT    NOT NULL,
    trust       INTEGER NOT NULL DEFAULT 0,
    added_at    INTEGER NOT NULL,
    PRIMARY KEY (event_id, raw_item_id)
);

CREATE INDEX IF NOT EXISTS idx_evidence_item ON event_evidence (raw_item_id);

-- event_entities records which instruments an event concerns, and what it
-- means for each of them separately.
--
-- Direction has to live here rather than on the event, because the same event
-- points opposite ways for different companies: crude rising is good for ONGC
-- and bad for IndiGo. An event-level sentiment column would have to pick one
-- and be wrong for the other.
CREATE TABLE IF NOT EXISTS event_entities (
    event_id        INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    symbol          TEXT    NOT NULL,
    relationship    TEXT    NOT NULL DEFAULT 'mentioned', -- primary|mentioned|peer|sector
    match_confidence REAL   NOT NULL DEFAULT 0,
    match_method    TEXT    NOT NULL DEFAULT '',

    -- Filled by the model, later. Direction is a label rather than a number
    -- because "unclear" is a real and common answer that no signed float can
    -- express: zero means "no effect", which is a different claim.
    direction       TEXT,           -- positive|negative|unclear
    impact_strength REAL,           -- 0-1
    rationale       TEXT NOT NULL DEFAULT '',

    PRIMARY KEY (event_id, symbol)
);

CREATE INDEX IF NOT EXISTS idx_event_entities_symbol ON event_entities (symbol, event_id DESC);

-- source_health is what the fetch scheduler reasons about: which sources are
-- healthy, which are failing, and which should be left alone for a while.
CREATE TABLE IF NOT EXISTS source_health (
    source_id            TEXT PRIMARY KEY,
    last_attempt_at      INTEGER,
    last_success_at      INTEGER,
    last_failure_at      INTEGER,
    last_error           TEXT    NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    total_attempts       INTEGER NOT NULL DEFAULT 0,
    total_successes      INTEGER NOT NULL DEFAULT 0,
    total_items          INTEGER NOT NULL DEFAULT 0,
    total_new_items      INTEGER NOT NULL DEFAULT 0,
    -- Conditional-request tokens. Honouring these is the difference between
    -- being a good citizen of a free feed and being blocked by it.
    etag                 TEXT    NOT NULL DEFAULT '',
    last_modified        TEXT    NOT NULL DEFAULT ''
);
`,
	},
	{
		Name: "0003_event_processing",
		SQL: `
-- processed_at marks a raw item as having been through the event pipeline.
--
-- It is a column on raw_items rather than a separate queue table because the
-- queue and the evidence are the same rows: an item is pending until it has
-- been turned into or attached to an event, and after that it stays as the
-- permanent record of where that event came from. A NULL here is the work
-- list, and the partial index makes finding it cheap no matter how large the
-- archive grows.
ALTER TABLE raw_items ADD COLUMN processed_at INTEGER;

CREATE INDEX IF NOT EXISTS idx_raw_items_pending
    ON raw_items (processed_at, discovered_at)
    WHERE processed_at IS NULL;

-- title_key is the normalised headline used to recognise the same story
-- arriving from a second publisher. Storing it saves recomputing the
-- normalisation for every candidate on every comparison.
ALTER TABLE events ADD COLUMN title_key TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_events_title_key ON events (title_key, discovered_at DESC);

-- Structured facts pulled out of a filing without a model: a dividend per
-- share, a promoter holding percentage, a meeting date. They live in their own
-- table because they are sparse -- most events have none, a few have several --
-- and because Phase 2 will query them by name across companies and quarters,
-- which a JSON blob on the event would make expensive.
CREATE TABLE IF NOT EXISTS event_facts (
    event_id  INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    key       TEXT    NOT NULL,
    value     TEXT    NOT NULL,
    -- num holds the numeric reading where the value is a number, so that
    -- "promoter holding above 60%" is an index scan rather than a table scan
    -- with a cast. It is NULL when the value is not numeric, which is a
    -- different thing from zero.
    num       REAL,
    PRIMARY KEY (event_id, key)
);

CREATE INDEX IF NOT EXISTS idx_event_facts_key ON event_facts (key, num);
`,
	},
	{
		Name: "0004_event_sectors",
		SQL: `
-- Sector reach, stored on the event rather than fanned out to companies.
--
-- A repo-rate decision touches every one of the 121 listed financial
-- companies. Writing 121 event_entities rows for it would bury the handful of
-- events that genuinely name a company, and would make "news about RELIANCE"
-- return every macro story that happened to mention energy. Storing the
-- sector once and expanding to companies at query time keeps the two kinds of
-- exposure — named and inferred — visibly different.
CREATE TABLE IF NOT EXISTS event_sectors (
    event_id INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    sector   TEXT    NOT NULL,
    PRIMARY KEY (event_id, sector)
);

CREATE INDEX IF NOT EXISTS idx_event_sectors_sector ON event_sectors (sector, event_id DESC);
`,
	},
}
