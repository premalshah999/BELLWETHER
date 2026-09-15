-- Fundamentals.
--
-- Two tables because there are two genuinely different kinds of number here,
-- and a single table would force one of them to lie about its timestamp.
--
-- A trailing P/E is a snapshot: it changes every time the price ticks, and the
-- only honest date to attach to it is when we looked. A quarter's revenue is
-- periodic: it describes three specific months and became public on a specific
-- later day. Storing both in one row would mean either stamping the ratios
-- with a quarter they do not belong to, or stamping the quarter with the
-- moment we happened to fetch it.

-- The point-in-time ratio snapshot.
--
-- Append-only, one row per fetch. Kept rather than updated in place so that
-- "what was this trading at when the story broke" stays answerable, and so a
-- ratio that moves sharply can be seen moving rather than simply being
-- different from last time anyone looked.
CREATE TABLE IF NOT EXISTS fundamentals_snapshot (
    id              BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol          TEXT        NOT NULL,
    as_of           TIMESTAMPTZ NOT NULL DEFAULT now(),

    sector          TEXT        NOT NULL DEFAULT '',
    industry        TEXT        NOT NULL DEFAULT '',

    -- Valuation. Every one of these is nullable and means it: a loss-making
    -- company has no meaningful P/E, and storing zero would put it top of any
    -- "cheapest" ranking.
    pe_trailing     DOUBLE PRECISION,
    pe_forward      DOUBLE PRECISION,
    price_to_book   DOUBLE PRECISION,
    market_cap      DOUBLE PRECISION,
    enterprise_value DOUBLE PRECISION,
    ev_to_ebitda    DOUBLE PRECISION,
    ev_to_revenue   DOUBLE PRECISION,
    peg_ratio       DOUBLE PRECISION,

    -- Returns and quality.
    return_on_equity DOUBLE PRECISION,
    return_on_assets DOUBLE PRECISION,
    profit_margin    DOUBLE PRECISION,
    operating_margin DOUBLE PRECISION,
    gross_margin     DOUBLE PRECISION,
    ebitda_margin    DOUBLE PRECISION,

    -- Balance sheet and payout.
    debt_to_equity  DOUBLE PRECISION,
    current_ratio   DOUBLE PRECISION,
    quick_ratio     DOUBLE PRECISION,
    dividend_yield  DOUBLE PRECISION,
    payout_ratio    DOUBLE PRECISION,

    -- Per share.
    eps_trailing    DOUBLE PRECISION,
    eps_forward     DOUBLE PRECISION,
    book_value      DOUBLE PRECISION,
    shares_outstanding DOUBLE PRECISION,

    -- Growth, as the provider computes it.
    revenue_growth  DOUBLE PRECISION,
    earnings_growth DOUBLE PRECISION,

    beta            DOUBLE PRECISION,

    -- Everything the provider returned, including fields with no column.
    -- Same reasoning as raw_items: a metric nobody wanted today can be
    -- recovered later without refetching a year of history.
    raw             JSONB       NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS fundamentals_snapshot_symbol_idx
    ON fundamentals_snapshot (symbol, as_of DESC);

-- The latest snapshot per symbol, which is what almost every read wants.
CREATE INDEX IF NOT EXISTS fundamentals_snapshot_latest_idx
    ON fundamentals_snapshot (symbol, id DESC);

-- Reported financial statements, by period.
CREATE TABLE IF NOT EXISTS financials (
    symbol          TEXT        NOT NULL,
    -- The period the numbers describe.
    period_end      DATE        NOT NULL,
    period_type     TEXT        NOT NULL CHECK (period_type IN ('quarterly', 'annual')),

    -- When the numbers became public.
    --
    -- Nullable, and null means "we do not know", never "the same as
    -- period_end". Indian companies report six to eight weeks after a quarter
    -- closes, so anything that treats the period end as the moment the figures
    -- existed can see June's results in June. Any query about what was knowable
    -- at a point in time must filter on this column and must exclude nulls
    -- rather than fall back to period_end.
    report_date     TIMESTAMPTZ,

    discovered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    revenue             DOUBLE PRECISION,
    gross_profit        DOUBLE PRECISION,
    operating_income    DOUBLE PRECISION,
    ebitda              DOUBLE PRECISION,
    ebit                DOUBLE PRECISION,
    net_income          DOUBLE PRECISION,
    eps_basic           DOUBLE PRECISION,
    eps_diluted         DOUBLE PRECISION,
    interest_expense    DOUBLE PRECISION,
    tax_provision       DOUBLE PRECISION,
    total_expenses      DOUBLE PRECISION,

    total_assets        DOUBLE PRECISION,
    total_debt          DOUBLE PRECISION,
    net_debt            DOUBLE PRECISION,
    equity              DOUBLE PRECISION,
    cash                DOUBLE PRECISION,
    working_capital     DOUBLE PRECISION,
    invested_capital    DOUBLE PRECISION,
    tangible_book_value DOUBLE PRECISION,
    shares_outstanding  DOUBLE PRECISION,

    free_cash_flow      DOUBLE PRECISION,
    capex               DOUBLE PRECISION,
    operating_cash_flow DOUBLE PRECISION,

    raw             JSONB       NOT NULL DEFAULT '{}'::jsonb,

    PRIMARY KEY (symbol, period_end, period_type)
);

CREATE INDEX IF NOT EXISTS financials_symbol_idx
    ON financials (symbol, period_type, period_end DESC);

-- Periods whose publication date is unknown. Small and worth being able to
-- find: they are the rows a point-in-time query has to leave out.
CREATE INDEX IF NOT EXISTS financials_unknown_report_idx
    ON financials (symbol) WHERE report_date IS NULL;
