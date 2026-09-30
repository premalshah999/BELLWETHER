-- Who is buying: insider trades from SEC Form 4, and quarterly holdings of
-- well-known funds from SEC 13F.

CREATE TABLE IF NOT EXISTS insider_trades (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    accession     TEXT NOT NULL,
    line          INTEGER NOT NULL,
    symbol        TEXT NOT NULL,
    issuer_cik    TEXT NOT NULL,
    issuer_name   TEXT NOT NULL,
    owner_cik     TEXT NOT NULL,
    owner_name    TEXT NOT NULL,
    is_director   BOOLEAN NOT NULL DEFAULT false,
    is_officer    BOOLEAN NOT NULL DEFAULT false,
    is_ten_pct    BOOLEAN NOT NULL DEFAULT false,
    officer_title TEXT NOT NULL DEFAULT '',
    security      TEXT NOT NULL DEFAULT '',
    tx_date       DATE NOT NULL,
    code          TEXT NOT NULL,
    acquired      BOOLEAN NOT NULL,
    shares        DOUBLE PRECISION NOT NULL,
    price         DOUBLE PRECISION,
    value         DOUBLE PRECISION,
    owned_after   DOUBLE PRECISION,
    direct        BOOLEAN NOT NULL DEFAULT true,
    plan_10b5_1   BOOLEAN NOT NULL DEFAULT false,
    filed_at      DATE NOT NULL,
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (accession, line)
);
CREATE INDEX IF NOT EXISTS insider_trades_symbol_idx ON insider_trades (symbol, tx_date DESC);
CREATE INDEX IF NOT EXISTS insider_trades_date_idx ON insider_trades (tx_date DESC);

-- Every Form 4 fetched, successful or not, so a sync never re-downloads one.
CREATE TABLE IF NOT EXISTS insider_filings_seen (
    accession  TEXT PRIMARY KEY,
    ok         BOOLEAN NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS funds (
    cik     TEXT PRIMARY KEY,
    name    TEXT NOT NULL,
    manager TEXT NOT NULL,
    style   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS fund_filings (
    accession   TEXT PRIMARY KEY,
    cik         TEXT NOT NULL REFERENCES funds (cik) ON DELETE CASCADE,
    period      DATE NOT NULL,
    filed       DATE NOT NULL,
    total_value DOUBLE PRECISION NOT NULL DEFAULT 0,
    positions   INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS fund_filings_period_idx ON fund_filings (cik, period);

CREATE TABLE IF NOT EXISTS fund_holdings (
    accession TEXT NOT NULL REFERENCES fund_filings (accession) ON DELETE CASCADE,
    cusip     TEXT NOT NULL,
    issuer    TEXT NOT NULL,
    symbol    TEXT NOT NULL DEFAULT '',
    value     DOUBLE PRECISION NOT NULL,
    shares    DOUBLE PRECISION NOT NULL,
    put_call  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (accession, cusip, put_call)
);
CREATE INDEX IF NOT EXISTS fund_holdings_symbol_idx ON fund_holdings (symbol);

-- CUSIP to ticker, looked up once and kept.
CREATE TABLE IF NOT EXISTS cusip_symbols (
    cusip        TEXT PRIMARY KEY,
    symbol       TEXT NOT NULL DEFAULT '',
    looked_up_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
