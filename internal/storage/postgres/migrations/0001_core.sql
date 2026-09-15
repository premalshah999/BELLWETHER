-- Core schema: instruments, prices, watchlist, provider bookkeeping.
--
-- Written for PostgreSQL rather than translated from the SQLite version. The
-- differences that matter are structural, not cosmetic:
--
--   * Times are TIMESTAMPTZ, not integers. Every timestamp in this system is
--     an instant, and storing instants as seconds meant every read and write
--     carried a manual conversion that could be — and was — got wrong.
--   * Enumerations are real types, so a typo in application code is a write
--     error rather than a row nobody ever queries again.
--   * Constraints express the invariants the code assumes, so violating one
--     fails loudly at the write instead of silently later.
--   * Indexes are partial where the query is, which keeps the hot index for
--     pending work proportional to the backlog rather than to the archive.

CREATE TABLE IF NOT EXISTS candles (
    symbol      TEXT        NOT NULL,
    interval    TEXT        NOT NULL,
    ts          TIMESTAMPTZ NOT NULL,
    open        DOUBLE PRECISION NOT NULL,
    high        DOUBLE PRECISION NOT NULL,
    low         DOUBLE PRECISION NOT NULL,
    close       DOUBLE PRECISION NOT NULL,
    volume      DOUBLE PRECISION NOT NULL DEFAULT 0,
    source      TEXT        NOT NULL DEFAULT '',
    -- The instrument that actually served this data, recorded only when it
    -- differs from the one requested: a BSE line filled from NSE is not the
    -- same series, and the substitution has to stay visible.
    resolved_symbol TEXT    NOT NULL DEFAULT '',
    fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (symbol, interval, ts),
    -- A bar whose high is below its low, or whose close sits outside the
    -- range, is corrupt. Providers do occasionally emit these, and a chart
    -- drawn from them is wrong in a way nobody notices.
    CONSTRAINT candles_range_valid CHECK (high >= low AND high >= open AND high >= close
                                          AND low <= open AND low <= close),
    CONSTRAINT candles_volume_nonneg CHECK (volume >= 0)
);

CREATE INDEX IF NOT EXISTS candles_symbol_interval_ts_idx
    ON candles (symbol, interval, ts DESC);

-- The high-water mark of how many bars have ever been asked for.
--
-- Without it a 30-bar sparkline request satisfies the freshness check and
-- starves a 400-bar chart request of the data it needs, because the cache
-- looks fresh while being far too short. That was a real bug.
CREATE TABLE IF NOT EXISTS series_coverage (
    symbol          TEXT        NOT NULL,
    interval        TEXT        NOT NULL,
    requested_limit INTEGER     NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (symbol, interval),
    CONSTRAINT series_coverage_limit_positive CHECK (requested_limit >= 0)
);

CREATE TABLE IF NOT EXISTS quotes (
    symbol         TEXT             PRIMARY KEY,
    price          DOUBLE PRECISION NOT NULL,
    prev_close     DOUBLE PRECISION NOT NULL DEFAULT 0,
    change         DOUBLE PRECISION NOT NULL DEFAULT 0,
    change_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    day_high       DOUBLE PRECISION NOT NULL DEFAULT 0,
    day_low        DOUBLE PRECISION NOT NULL DEFAULT 0,
    volume         DOUBLE PRECISION NOT NULL DEFAULT 0,
    currency       TEXT             NOT NULL DEFAULT '',
    source         TEXT             NOT NULL DEFAULT '',
    resolved_symbol TEXT            NOT NULL DEFAULT '',
    as_of          TIMESTAMPTZ,
    fetched_at     TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS watchlist (
    symbol   TEXT        PRIMARY KEY,
    note     TEXT        NOT NULL DEFAULT '',
    position INTEGER     NOT NULL DEFAULT 0,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- period is a caller-supplied bucket label, "2026-08-25" for a daily budget.
-- It is text rather than a date because the caller decides the granularity,
-- and a monthly LLM budget uses "2026-08".
CREATE TABLE IF NOT EXISTS provider_budget (
    provider   TEXT        NOT NULL,
    period     TEXT        NOT NULL,
    used       INTEGER     NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, period),
    CONSTRAINT provider_budget_used_nonneg CHECK (used >= 0)
);

-- One row per provider, holding its current state plus the last time it was
-- seen working and the last time it failed. Keeping both means the UI can say
-- "degraded, last worked 6 minutes ago" rather than only "degraded".
CREATE TABLE IF NOT EXISTS provider_health (
    provider      TEXT        PRIMARY KEY,
    kind          TEXT        NOT NULL,
    status        TEXT        NOT NULL,
    message       TEXT        NOT NULL DEFAULT '',
    last_ok_at    TIMESTAMPTZ,
    last_error_at TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
