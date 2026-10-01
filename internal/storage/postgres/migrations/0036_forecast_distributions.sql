-- Forecasts are distributions now: each prediction carries its simulated
-- quantiles, probabilities and path fan. Runs from the ranking-only model
-- are removed; their report has a different shape and nothing scores them.
ALTER TABLE forecast_predictions ADD COLUMN dist jsonb;
DELETE FROM forecast_runs WHERE NOT (report ? 'version');
