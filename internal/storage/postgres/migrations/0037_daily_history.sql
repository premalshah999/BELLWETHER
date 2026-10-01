-- Deep daily history for the forecast engine: bars older than what the
-- candles table keeps, so the engine can be tested across more than one
-- market. It can be fetched again from the price service at any time, so the
-- backup service leaves its rows out.
CREATE TABLE daily_history (
    symbol TEXT NOT NULL,
    ts     TIMESTAMPTZ NOT NULL,
    open   REAL NOT NULL,
    high   REAL NOT NULL,
    low    REAL NOT NULL,
    close  REAL NOT NULL,
    volume REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (symbol, ts)
);
