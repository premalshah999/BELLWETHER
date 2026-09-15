-- Custom screens need the whole universe, not just the interesting part of it.
--
-- The scanner already measures all 750 constituents on every pass, but until
-- now it discarded every instrument that did not trip one of the six built-in
-- signals -- roughly 710 of 750. That is the right call for the Scanner page,
-- which exists to show what is abnormal today. It makes a user-defined screen
-- impossible: "RSI-quiet stocks within 3% of their 52-week high" describes
-- instruments that are, by construction, not abnormal, so none of them were
-- ever written down.
--
-- So the measurements are kept separately from the findings. scan_findings
-- stays exactly what it was, and scan_metrics is the full sweep.
CREATE TABLE IF NOT EXISTS scan_metrics (
    scan_id           BIGINT NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    symbol            TEXT   NOT NULL,
    close_price       DOUBLE PRECISION NOT NULL,
    return_1d         DOUBLE PRECISION NOT NULL,
    return_5d         DOUBLE PRECISION NOT NULL,
    return_z          DOUBLE PRECISION NOT NULL,
    volume            DOUBLE PRECISION NOT NULL,
    volume_ratio      DOUBLE PRECISION NOT NULL,
    volume_z          DOUBLE PRECISION NOT NULL,
    gap_percent       DOUBLE PRECISION NOT NULL,
    pct_from_52w_high DOUBLE PRECISION NOT NULL,
    pct_from_52w_low  DOUBLE PRECISION NOT NULL,
    bars              INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (scan_id, symbol)
);

-- Every screen runs against the newest scan, so the ordering index earns its
-- keep on literally every query this table serves.
CREATE INDEX IF NOT EXISTS scan_metrics_scan_idx ON scan_metrics (scan_id);

-- A saved screen is a question the operator asks repeatedly.
--
-- The definition is JSONB rather than columns because the shape is a list of
-- conditions of varying length, and because the alternative -- a filters table
-- with a row per condition -- makes reading one screen a join and editing one
-- a transaction, for no gain: nothing ever queries across screens by filter.
CREATE TABLE IF NOT EXISTS screens (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    definition  JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_run_at TIMESTAMPTZ
);
