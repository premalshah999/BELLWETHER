-- Paper trading: wallets funded with simulated money, orders filled against
-- real market prices, and agents that trade them.
--
-- Money is whole cents (BIGINT), never floating point. Every movement of value
-- is a ledger transaction whose postings sum to zero across the wallet's
-- accounts, so the books always balance and any balance can be rebuilt from
-- the postings alone.

CREATE TABLE paper_wallets (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT NOT NULL,
    owner       TEXT NOT NULL DEFAULT '',
    currency    TEXT NOT NULL DEFAULT 'USD',
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
    -- Execution realism and risk limits; see internal/paper.Settings.
    settings    JSONB NOT NULL DEFAULT '{}',
    -- The highest equity ever marked, for the drawdown limit.
    peak_equity_cents BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A ledger transaction groups postings that must balance.
CREATE TABLE paper_ledger_tx (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wallet_id   BIGINT NOT NULL REFERENCES paper_wallets(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('deposit', 'withdrawal', 'buy', 'sell', 'adjustment')),
    ref_type    TEXT NOT NULL DEFAULT '',
    ref_id      BIGINT,
    memo        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX paper_ledger_tx_wallet_idx ON paper_ledger_tx (wallet_id, id DESC);

-- Accounts: cash, external (the outside world money arrives from and leaves
-- to), securities (holdings at cost), fees (commissions and regulatory fees),
-- realized_pnl (gains and losses closed out).
CREATE TABLE paper_postings (
    tx_id        BIGINT NOT NULL REFERENCES paper_ledger_tx(id) ON DELETE CASCADE,
    wallet_id    BIGINT NOT NULL,
    account      TEXT NOT NULL CHECK (account IN ('cash', 'external', 'securities', 'fees', 'realized_pnl')),
    amount_cents BIGINT NOT NULL,
    PRIMARY KEY (tx_id, account)
);
CREATE INDEX paper_postings_wallet_idx ON paper_postings (wallet_id, account);

-- A payment moves money between the outside world and a wallet. It follows
-- a provider's lifecycle, so a real processor can replace the simulated one:
-- created -> processing -> succeeded | failed | canceled. The ledger is
-- written only on success, exactly once, guarded by the idempotency key.
CREATE TABLE paper_payments (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wallet_id       BIGINT NOT NULL REFERENCES paper_wallets(id) ON DELETE CASCADE,
    direction       TEXT NOT NULL CHECK (direction IN ('deposit', 'withdrawal')),
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    currency        TEXT NOT NULL DEFAULT 'USD',
    provider        TEXT NOT NULL DEFAULT 'simulated',
    provider_ref    TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL CHECK (status IN ('created', 'processing', 'succeeded', 'failed', 'canceled')),
    idempotency_key TEXT NOT NULL,
    failure_reason  TEXT NOT NULL DEFAULT '',
    ledger_tx_id    BIGINT REFERENCES paper_ledger_tx(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (wallet_id, idempotency_key)
);

CREATE TABLE paper_orders (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wallet_id         BIGINT NOT NULL REFERENCES paper_wallets(id) ON DELETE CASCADE,
    client_order_id   TEXT NOT NULL,
    symbol            TEXT NOT NULL,
    side              TEXT NOT NULL CHECK (side IN ('buy', 'sell')),
    type              TEXT NOT NULL CHECK (type IN ('market', 'limit', 'stop', 'stop_limit')),
    qty               BIGINT NOT NULL CHECK (qty > 0),
    limit_cents       BIGINT,
    stop_cents        BIGINT,
    tif               TEXT NOT NULL CHECK (tif IN ('day', 'gtc')),
    status            TEXT NOT NULL CHECK (status IN ('accepted', 'filled', 'canceled', 'rejected', 'expired')),
    filled_qty        BIGINT NOT NULL DEFAULT 0,
    avg_fill_cents    BIGINT,
    -- Cash held back for an open buy, so two orders cannot spend it twice.
    reserved_cents    BIGINT NOT NULL DEFAULT 0,
    source            TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'agent', 'protective')),
    agent_id          BIGINT,
    reason            TEXT NOT NULL DEFAULT '',
    reject_reason     TEXT NOT NULL DEFAULT '',
    -- A buy may carry exits, placed as orders of their own when it fills.
    stop_loss_pct     DOUBLE PRECISION,
    take_profit_pct   DOUBLE PRECISION,
    parent_order_id   BIGINT REFERENCES paper_orders(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    filled_at         TIMESTAMPTZ,
    UNIQUE (wallet_id, client_order_id)
);
CREATE INDEX paper_orders_open_idx ON paper_orders (status) WHERE status = 'accepted';
CREATE INDEX paper_orders_wallet_idx ON paper_orders (wallet_id, id DESC);

CREATE TABLE paper_fills (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id     BIGINT NOT NULL REFERENCES paper_orders(id) ON DELETE CASCADE,
    wallet_id    BIGINT NOT NULL,
    symbol       TEXT NOT NULL,
    side         TEXT NOT NULL,
    qty          BIGINT NOT NULL,
    price_cents  BIGINT NOT NULL,
    fee_cents    BIGINT NOT NULL DEFAULT 0,
    -- The market price the fill was taken from, before slippage.
    quote_cents  BIGINT NOT NULL,
    realized_cents BIGINT,
    -- For a sale, when the position it closes was opened: a round trip.
    opened_at    TIMESTAMPTZ,
    ledger_tx_id BIGINT REFERENCES paper_ledger_tx(id),
    filled_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX paper_fills_wallet_idx ON paper_fills (wallet_id, filled_at DESC);

-- Holdings, kept in the same transaction as each fill. cost_cents is the
-- total cost basis of the shares still held (average-cost method).
CREATE TABLE paper_positions (
    wallet_id   BIGINT NOT NULL REFERENCES paper_wallets(id) ON DELETE CASCADE,
    symbol      TEXT NOT NULL,
    qty         BIGINT NOT NULL CHECK (qty >= 0),
    cost_cents  BIGINT NOT NULL,
    opened_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (wallet_id, symbol)
);

CREATE TABLE paper_equity (
    wallet_id           BIGINT NOT NULL REFERENCES paper_wallets(id) ON DELETE CASCADE,
    at                  TIMESTAMPTZ NOT NULL,
    cash_cents          BIGINT NOT NULL,
    market_value_cents  BIGINT NOT NULL,
    equity_cents        BIGINT NOT NULL,
    -- The S&P 500 at the same moment, so performance is always read against it.
    benchmark           DOUBLE PRECISION,
    PRIMARY KEY (wallet_id, at)
);

CREATE TABLE paper_agents (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wallet_id   BIGINT NOT NULL REFERENCES paper_wallets(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('ai', 'algorithm', 'webhook')),
    name        TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT false,
    config      JSONB NOT NULL DEFAULT '{}',
    halted_reason TEXT NOT NULL DEFAULT '',
    last_run_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Every decision an agent made, with what it saw and why, whether or not it
-- traded: the audit trail that says whether it was skill or luck.
CREATE TABLE paper_decisions (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id    BIGINT NOT NULL REFERENCES paper_agents(id) ON DELETE CASCADE,
    wallet_id   BIGINT NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    summary     TEXT NOT NULL DEFAULT '',
    intents     JSONB NOT NULL DEFAULT '[]',
    order_ids   BIGINT[] NOT NULL DEFAULT '{}',
    rejected    JSONB NOT NULL DEFAULT '[]',
    model       TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX paper_decisions_agent_idx ON paper_decisions (agent_id, at DESC);
