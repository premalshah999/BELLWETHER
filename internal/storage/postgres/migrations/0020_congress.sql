-- Congressional financial disclosures: who in Congress bought or sold what,
-- and how long they waited to say so.
--
-- One row per filing, not one row per ticker or per trade. The House
-- Clerk's PTR PDFs (internal/congress package doc explains why) do not
-- reliably support row-level ticker/transaction pairing, so this table
-- matches the extraction it actually stores: which symbols a filing names,
-- and the earliest transaction date found anywhere in it. A reader who
-- wants the exact line follows doc_url to the filing itself.
--
-- filing_date is the disclosure -- exact, from the Clerk's own structured
-- index. earliest_transaction_date is the best-effort read of when the
-- underlying trade happened, which is what disclosure_delay_days measures
-- against: the STOCK Act requires filing within 45 days, and a late filing
-- is itself a signal worth surfacing, not just a data quality footnote.
-- doc_url is not stored: it is a pure function of (year, doc_id) --
-- Filing.DocURL() -- and storing a derived value invites it to drift from
-- the rule that computes it.
CREATE TABLE IF NOT EXISTS congress_filings (
    doc_id                    TEXT PRIMARY KEY,
    chamber                   TEXT NOT NULL DEFAULT 'house',
    last_name                 TEXT NOT NULL,
    first_name                TEXT NOT NULL DEFAULT '',
    state_district            TEXT NOT NULL DEFAULT '',
    filing_type               TEXT NOT NULL,
    filing_date               DATE NOT NULL,
    year                      INTEGER NOT NULL,
    symbols                   TEXT[] NOT NULL DEFAULT '{}',
    unresolved_tickers        TEXT[] NOT NULL DEFAULT '{}',
    earliest_transaction_date DATE,
    disclosure_delay_days     INTEGER,
    discovered_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- "Has anyone in Congress traded this" is the query the page exists to
-- answer, so the symbol array needs its own index rather than a sequential
-- scan per lookup.
CREATE INDEX IF NOT EXISTS congress_filings_symbols_idx ON congress_filings USING GIN (symbols);
CREATE INDEX IF NOT EXISTS congress_filings_filing_date_idx ON congress_filings (filing_date DESC);
