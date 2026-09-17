-- Real money at risk, not just names on a watchlist.
--
-- Everything upstream of this migration -- the scanner, the news feed, the
-- Geopolitics page -- answers questions about symbols the operator has
-- expressed interest in. None of it knows how much of anything the operator
-- actually owns, so "does this event touch my holdings" has only ever been
-- answerable against a watchlist, which mixes real exposure with names
-- someone is merely tracking.
--
-- Positions and trades are deliberately two tables, not one. A position is
-- open and mutable -- its quantity and cost basis change as it is added to
-- or trimmed. A trade is a closed, immutable fact: it happened at a price,
-- on a date, and realized a specific P&L, and it stays true forever after.
-- Closing a position (fully or partially) is the seam between them: it
-- removes or reduces the position and appends a trade row, never edits one.
-- That asymmetry is what will let a future trade journal trust the trades
-- table as a real record instead of a snapshot that could have been
-- silently rewritten.
CREATE TABLE IF NOT EXISTS positions (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol      TEXT        NOT NULL,
    quantity    DOUBLE PRECISION NOT NULL,
    cost_basis  DOUBLE PRECISION NOT NULL, -- per share/unit, in the symbol's own currency
    opened_at   DATE        NOT NULL,
    account     TEXT        NOT NULL DEFAULT '',
    notes       TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT positions_quantity_positive CHECK (quantity > 0),
    CONSTRAINT positions_cost_basis_nonneg CHECK (cost_basis >= 0)
);

CREATE INDEX IF NOT EXISTS positions_symbol_idx ON positions (symbol);

-- A closed trade: what a position (or part of one) actually returned.
-- Long-only for now, matching the position side above -- short positions
-- are a real gap, not a design decision, and can be added by giving both
-- tables a side column when they are needed.
CREATE TABLE IF NOT EXISTS trades (
    id           BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol       TEXT        NOT NULL,
    quantity     DOUBLE PRECISION NOT NULL,
    entry_price  DOUBLE PRECISION NOT NULL,
    exit_price   DOUBLE PRECISION NOT NULL,
    opened_at    DATE        NOT NULL,
    closed_at    DATE        NOT NULL,
    -- Stored, not computed on read: a trade is a historical fact, and
    -- storing the realized figure means it survives even if this system's
    -- own P&L formula is later revised (fees added, for instance) without
    -- silently rewriting what every past trade is remembered to have made.
    realized_pnl DOUBLE PRECISION NOT NULL,
    account      TEXT        NOT NULL DEFAULT '',
    notes        TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT trades_quantity_positive CHECK (quantity > 0),
    CONSTRAINT trades_dates_ordered CHECK (closed_at >= opened_at)
);

CREATE INDEX IF NOT EXISTS trades_symbol_idx ON trades (symbol);
CREATE INDEX IF NOT EXISTS trades_closed_at_idx ON trades (closed_at DESC);
