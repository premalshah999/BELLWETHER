-- The feature layer: algorithms, alerts, AI outputs, outlooks, caches.

CREATE TABLE IF NOT EXISTS algorithms (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT        NOT NULL,
    -- The symbol list is a real array rather than a JSON string, so
    -- "which algorithms watch RELIANCE" is an indexable containment query.
    symbols     TEXT[]      NOT NULL DEFAULT '{}',
    interval    TEXT        NOT NULL,
    -- The rule tree, as JSONB rather than TEXT. Storing it as a string meant
    -- every read paid a parse and no query could reach inside it; JSONB makes
    -- "which algorithms reference RSI" an indexable question.
    definition  JSONB       NOT NULL,
    cooldown_hours INTEGER  NOT NULL DEFAULT 24,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT algorithms_name_present CHECK (name <> ''),
    CONSTRAINT algorithms_cooldown_nonneg CHECK (cooldown_hours >= 0)
);

CREATE INDEX IF NOT EXISTS algorithms_enabled_idx ON algorithms (enabled, interval);
CREATE INDEX IF NOT EXISTS algorithms_definition_idx ON algorithms USING GIN (definition);
CREATE INDEX IF NOT EXISTS algorithms_symbols_idx ON algorithms USING GIN (symbols);

-- Per-symbol evaluation state: when an algorithm last fired for a symbol
-- (which drives the cooldown) and what the last evaluation concluded.
CREATE TABLE IF NOT EXISTS algorithm_symbol_state (
    algorithm_id      BIGINT      NOT NULL REFERENCES algorithms (id) ON DELETE CASCADE,
    symbol            TEXT        NOT NULL,
    last_fired_at     TIMESTAMPTZ,
    last_evaluated_at TIMESTAMPTZ,
    last_status       TEXT        NOT NULL DEFAULT '',
    last_reason       TEXT        NOT NULL DEFAULT '',
    last_summary      TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (algorithm_id, symbol)
);

CREATE TABLE IF NOT EXISTS alerts (
    id             BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Deliberately not a foreign key. An alert is a historical record of
    -- something that fired, and it has to survive the algorithm being
    -- deleted; a cascade would erase the history along with the rule, and
    -- SET NULL would lose which rule it was.
    algorithm_id   BIGINT      NOT NULL,
    -- Denormalised so the record still reads correctly after a rename.
    algorithm_name TEXT        NOT NULL DEFAULT '',
    symbol         TEXT        NOT NULL,
    interval       TEXT        NOT NULL DEFAULT '',
    fired_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    bar_time       TIMESTAMPTZ,
    price          DOUBLE PRECISION NOT NULL DEFAULT 0,
    summary        TEXT        NOT NULL DEFAULT '',
    -- The full evaluation snapshot: which conditions held, with the values
    -- they held at. JSONB so a later question like "how often did the RSI leg
    -- carry this alert" is answerable without re-running anything.
    conditions     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    ai_context     TEXT        NOT NULL DEFAULT '',
    ai_status      TEXT        NOT NULL DEFAULT '',
    delivery       JSONB       NOT NULL DEFAULT '[]'::jsonb,
    read_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS alerts_recent_idx ON alerts (fired_at DESC);
-- The unread badge is read on every page load, so it gets its own partial
-- index rather than scanning the whole history for a count.
CREATE INDEX IF NOT EXISTS alerts_unread_idx ON alerts (fired_at DESC) WHERE read_at IS NULL;
CREATE INDEX IF NOT EXISTS alerts_symbol_idx ON alerts (symbol, fired_at DESC);

CREATE TABLE IF NOT EXISTS llm_usage (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    period        TEXT        NOT NULL,
    feature       TEXT        NOT NULL DEFAULT '',
    model         TEXT        NOT NULL DEFAULT '',
    prompt_tokens INTEGER     NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER     NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT llm_usage_tokens_nonneg CHECK (
        prompt_tokens >= 0 AND completion_tokens >= 0 AND total_tokens >= 0
    )
);

CREATE INDEX IF NOT EXISTS llm_usage_period_idx ON llm_usage (period, feature);

CREATE TABLE IF NOT EXISTS ai_outputs (
    id         BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind       TEXT        NOT NULL,
    symbol     TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    model      TEXT        NOT NULL DEFAULT '',
    tokens     INTEGER     NOT NULL DEFAULT 0,
    content    JSONB       NOT NULL
);

CREATE INDEX IF NOT EXISTS ai_outputs_kind_idx ON ai_outputs (kind, created_at DESC);

-- A recorded forecast, kept so the model can be scored against reality later.
-- The three scenarios stay as separate columns rather than a JSON blob because
-- the calibration page aggregates across them, and Phase 2 will want to ask
-- questions like "how well calibrated is the bear case" in SQL.
CREATE TABLE IF NOT EXISTS outlooks (
    id                BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol            TEXT        NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    horizon_days      INTEGER     NOT NULL,
    price_at_creation DOUBLE PRECISION NOT NULL,

    base_probability  DOUBLE PRECISION NOT NULL,
    base_move         DOUBLE PRECISION NOT NULL,
    base_reasoning    TEXT NOT NULL DEFAULT '',
    bull_probability  DOUBLE PRECISION NOT NULL,
    bull_move         DOUBLE PRECISION NOT NULL,
    bull_reasoning    TEXT NOT NULL DEFAULT '',
    bear_probability  DOUBLE PRECISION NOT NULL,
    bear_move         DOUBLE PRECISION NOT NULL,
    bear_reasoning    TEXT NOT NULL DEFAULT '',

    key_risk          TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',

    resolved_at       TIMESTAMPTZ,
    realized_price    DOUBLE PRECISION,
    realized_move     DOUBLE PRECISION,
    actual_scenario   TEXT NOT NULL DEFAULT '',
    brier_score       DOUBLE PRECISION,

    CONSTRAINT outlooks_horizon_positive CHECK (horizon_days > 0),
    -- Probabilities must be probabilities, and the three scenarios are
    -- exhaustive, so they have to sum to one within rounding. A forecast whose
    -- probabilities do not sum to one cannot be scored, and catching that at
    -- the write is far cheaper than discovering it months later in the
    -- calibration numbers.
    CONSTRAINT outlooks_probabilities_valid CHECK (
        base_probability BETWEEN 0 AND 1 AND
        bull_probability BETWEEN 0 AND 1 AND
        bear_probability BETWEEN 0 AND 1 AND
        abs((base_probability + bull_probability + bear_probability) - 1) < 0.02
    ),
    CONSTRAINT outlooks_brier_range CHECK (brier_score IS NULL OR (brier_score >= 0 AND brier_score <= 2))
);

CREATE INDEX IF NOT EXISTS outlooks_symbol_idx ON outlooks (symbol, created_at DESC);
-- The scoring job asks only for outlooks that are due and unresolved.
CREATE INDEX IF NOT EXISTS outlooks_pending_idx
    ON outlooks (created_at) WHERE resolved_at IS NULL;

CREATE TABLE IF NOT EXISTS news_articles (
    id           BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol       TEXT        NOT NULL,
    title        TEXT        NOT NULL,
    url          TEXT        NOT NULL,
    source       TEXT        NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ,
    fetched_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    relevance    REAL,
    sentiment    REAL,
    one_line     TEXT        NOT NULL DEFAULT '',
    score_model  TEXT        NOT NULL DEFAULT '',
    scored_at    TIMESTAMPTZ,

    CONSTRAINT news_articles_identity UNIQUE (symbol, url),
    CONSTRAINT news_articles_relevance_range CHECK (relevance IS NULL OR (relevance >= 0 AND relevance <= 1)),
    CONSTRAINT news_articles_sentiment_range CHECK (sentiment IS NULL OR (sentiment >= -1 AND sentiment <= 1))
);

CREATE INDEX IF NOT EXISTS news_articles_symbol_idx
    ON news_articles (symbol, published_at DESC NULLS LAST, fetched_at DESC);
CREATE INDEX IF NOT EXISTS news_articles_unscored_idx
    ON news_articles (fetched_at) WHERE scored_at IS NULL;

CREATE TABLE IF NOT EXISTS search_cache (
    key        TEXT        PRIMARY KEY,
    query      TEXT        NOT NULL,
    provider   TEXT        NOT NULL,
    results    JSONB       NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS search_cache_age_idx ON search_cache (fetched_at);
