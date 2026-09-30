-- The forecast model's runs: each with its walk-forward report, and every
-- stock's score on the session it ran for, so the live record can be scored
-- once the horizon has passed.
CREATE TABLE forecast_runs (
    id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    as_of   DATE NOT NULL,
    horizon INT NOT NULL,
    report  JSONB NOT NULL
);
CREATE UNIQUE INDEX forecast_runs_as_of_idx ON forecast_runs (as_of, horizon);

CREATE TABLE forecast_predictions (
    run_id     BIGINT NOT NULL REFERENCES forecast_runs(id) ON DELETE CASCADE,
    symbol     TEXT NOT NULL,
    score      DOUBLE PRECISION NOT NULL,
    percentile DOUBLE PRECISION NOT NULL,
    drivers    JSONB NOT NULL DEFAULT '[]',
    PRIMARY KEY (run_id, symbol)
);
CREATE INDEX forecast_predictions_symbol_idx ON forecast_predictions (symbol, run_id DESC);

-- The India-era Nifty benchmark the earlier cleanup missed.
DELETE FROM candles WHERE symbol = 'NSEI.INDEX';
DELETE FROM quotes WHERE symbol = 'NSEI.INDEX';
DELETE FROM series_coverage WHERE symbol = 'NSEI.INDEX';
