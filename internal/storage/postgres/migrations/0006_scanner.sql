-- Scanner snapshots.
--
-- The scanner answers "what is behaving abnormally right now", but the more
-- valuable question is "what was behaving abnormally before the news came
-- out". That is only answerable if every scan is kept, so these tables are
-- append-only: a scan is never updated in place, and a finding is never
-- revised once written.
--
-- This is the same discipline as the three timestamps on events. The scan's
-- as_of is when the market data was current; scanned_at is when we learned
-- it. Keeping both is what makes it possible to ask, later, whether the
-- volume showed up before the announcement did.

CREATE TABLE IF NOT EXISTS scans (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    as_of       TIMESTAMPTZ NOT NULL,
    scanned_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    universe    INTEGER     NOT NULL CHECK (universe >= 0),
    scanned     INTEGER     NOT NULL CHECK (scanned >= 0),
    failed      INTEGER     NOT NULL CHECK (failed >= 0),
    elapsed     TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS scans_scanned_at_idx ON scans (scanned_at DESC);

CREATE TABLE IF NOT EXISTS scan_findings (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scan_id     BIGINT      NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    symbol      TEXT        NOT NULL,
    signals     TEXT[]      NOT NULL DEFAULT '{}',
    score       DOUBLE PRECISION NOT NULL,

    close_price     DOUBLE PRECISION NOT NULL,
    return_1d       DOUBLE PRECISION NOT NULL,
    return_5d       DOUBLE PRECISION NOT NULL,
    return_z        DOUBLE PRECISION NOT NULL,
    volume          DOUBLE PRECISION NOT NULL,
    volume_ratio    DOUBLE PRECISION NOT NULL,
    volume_z        DOUBLE PRECISION NOT NULL,
    gap_percent     DOUBLE PRECISION NOT NULL,
    pct_from_52w_high DOUBLE PRECISION NOT NULL,
    pct_from_52w_low  DOUBLE PRECISION NOT NULL,

    -- Whether anything in the archive explained this move at the time it was
    -- found. Left null until the explain pass runs, so "not yet checked" and
    -- "checked, found nothing" stay distinguishable — the second is a real
    -- finding and the first is not.
    explained   BOOLEAN,
    explained_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS scan_findings_scan_idx ON scan_findings (scan_id, score DESC);
CREATE INDEX IF NOT EXISTS scan_findings_symbol_idx ON scan_findings (symbol, id DESC);

-- Unexplained findings are the work queue for search: a partial index because
-- they are a small minority of rows and the queue is polled often.
CREATE INDEX IF NOT EXISTS scan_findings_unexplained_idx
    ON scan_findings (id DESC) WHERE explained IS NOT TRUE;
