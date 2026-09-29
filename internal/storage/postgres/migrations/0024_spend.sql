-- Spend in dollars, for both model providers.
--
-- llm_usage counted tokens, which is enough for a monthly token budget and not
-- enough for a daily dollar cap: DeepSeek prices a cached input token at a
-- fiftieth of an uncached one and bills peak hours at twice off-peak, so the
-- same token count can cost very different amounts. The cost is computed at
-- the moment of the call, at the rate then in force, and stored -- rather than
-- recomputed later from tokens -- so a price-list change never rewrites what
-- was actually spent.

ALTER TABLE llm_usage
    ADD COLUMN IF NOT EXISTS cache_hit_tokens  INTEGER          NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_miss_tokens INTEGER          NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cost_usd          DOUBLE PRECISION NOT NULL DEFAULT 0;

-- The daily cap sums cost since UTC midnight before every call, so it has to
-- be an index lookup and not a scan of every call ever made.
CREATE INDEX IF NOT EXISTS llm_usage_created_idx ON llm_usage (created_at);

-- Jev usage. Separate from llm_usage because it is a different kind of spend:
-- priced on input tokens alone, with output reported but not billed under
-- TypeSafe's published pricing at the time of writing.
CREATE TABLE IF NOT EXISTS jev_usage (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    feature       TEXT             NOT NULL DEFAULT '',
    input_tokens  INTEGER          NOT NULL DEFAULT 0,
    output_tokens INTEGER          NOT NULL DEFAULT 0,
    cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CONSTRAINT jev_usage_nonneg CHECK (input_tokens >= 0 AND output_tokens >= 0 AND cost_usd >= 0)
);
CREATE INDEX IF NOT EXISTS jev_usage_created_idx ON jev_usage (created_at);

-- A negative cost would let a bad price table lower the day's spend and open
-- the cap. Refused at the database, not only in Go.
ALTER TABLE llm_usage DROP CONSTRAINT IF EXISTS llm_usage_cost_nonneg;
ALTER TABLE llm_usage ADD CONSTRAINT llm_usage_cost_nonneg
    CHECK (cost_usd >= 0 AND cache_hit_tokens >= 0 AND cache_miss_tokens >= 0);
