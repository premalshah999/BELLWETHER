-- Outlooks start from the forecast engine's distribution. The prior, the
-- adjusted distribution and the writer's adjustment are kept, and the prior
-- is scored on the same outcome, so the track record can say whether the
-- writer's adjustments help.
ALTER TABLE outlooks
    ADD COLUMN quant       jsonb,
    ADD COLUMN final       jsonb,
    ADD COLUMN adjustment  jsonb,
    ADD COLUMN quant_brier double precision
        CHECK (quant_brier IS NULL OR (quant_brier >= 0 AND quant_brier <= 2));
