-- Past earnings announcements with the consensus and the surprise, for the
-- event study's earnings history and as a model feature. announced_at is when
-- the result became public, the only honest anchor for its reaction.
CREATE TABLE earnings_history (
    symbol       TEXT NOT NULL,
    announced_at TIMESTAMPTZ NOT NULL,
    eps_estimate DOUBLE PRECISION,
    eps_actual   DOUBLE PRECISION NOT NULL,
    surprise_pct DOUBLE PRECISION,
    fetched_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (symbol, announced_at)
);
CREATE INDEX earnings_history_announced_idx ON earnings_history (announced_at);
