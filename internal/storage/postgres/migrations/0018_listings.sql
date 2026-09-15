-- The listed-instrument reference: every symbol the app can recognize,
-- across both venues, with the venue itself as data rather than something
-- inferred from string shape.
--
-- Before this, "is RELIANCE an NSE stock" was answered by checking whether
-- it happened to appear in index_constituents, which only ever held NSE
-- names because nothing else was ever loaded there. That stopped being true
-- the moment a second venue existed: RELIANCE.NSE and AAPL both needed a
-- home, and index_constituents' bare symbol column could not tell ABB India
-- from ABB's NYSE-listed namesake apart, because both would be the same
-- three characters.
--
-- listings is deliberately broader than index_constituents: it is the full
-- referenceable universe (NSE's 2,557-row equity_l.csv plus SEC's 10,421-row
-- exchange-listed ticker file), the same way equity_l.csv is broader than
-- the ~750-name industry.csv scan subset. Most rows here will never appear
-- on a watchlist or in a scan; what they are for is recognizing a symbol at
-- all, so that a filing or an article mentioning an obscure ticker resolves
-- to something instead of being silently dropped or, worse, silently
-- mis-attached to a same-named instrument on the wrong venue.
CREATE TABLE IF NOT EXISTS listings (
    symbol     TEXT PRIMARY KEY,        -- canonical form: AAPL, RELIANCE.NSE
    ticker     TEXT NOT NULL,
    venue      TEXT NOT NULL,           -- US | NSE | BSE
    exchange   TEXT,                    -- Nasdaq | NYSE | CBOE | OTC (US only)
    name       TEXT NOT NULL,
    cik        TEXT,                    -- 10-digit, US only. Not unique: dual
                                         -- class shares (BRK-A/BRK-B) share one.
    isin       TEXT,                    -- NSE only
    taxonomy   TEXT,                    -- 'nse-industry' | 'gics', unset outside
                                         -- the scan universe
    industry   TEXT,
    active     BOOLEAN NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT listings_venue_check CHECK (venue IN ('US', 'NSE', 'BSE'))
);

CREATE INDEX IF NOT EXISTS listings_ticker_idx ON listings (ticker);
CREATE INDEX IF NOT EXISTS listings_cik_idx    ON listings (cik) WHERE cik IS NOT NULL;
CREATE INDEX IF NOT EXISTS listings_taxonomy_idx ON listings (taxonomy, industry)
    WHERE taxonomy IS NOT NULL;

-- index_constituents stays the narrower "what the scanner actually covers"
-- table (its own name says so), but it needs to say which venue and which
-- taxonomy its industry column speaks, now that it holds more than one of
-- each. Backfilled by the 0019 Go migration alongside the symbol rewrite,
-- because that is also where the venue becomes known for the first time.
ALTER TABLE index_constituents ADD COLUMN IF NOT EXISTS venue    TEXT;
ALTER TABLE index_constituents ADD COLUMN IF NOT EXISTS taxonomy TEXT;
