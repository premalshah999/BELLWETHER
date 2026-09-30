-- Funds become a directory the operator curates: a shipped list of well-known
-- managers plus any 13F filer they search for and follow. Unfollowing keeps the
-- filings, so following again is instant.
ALTER TABLE funds
    ADD COLUMN followed BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN curated  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN added_at TIMESTAMPTZ NOT NULL DEFAULT now();
UPDATE funds SET curated = true;
CREATE INDEX IF NOT EXISTS fund_filings_cik_period_idx ON fund_filings (cik, period DESC);
