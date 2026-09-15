-- Measured market data, stored alongside the answer.
--
-- Kept because it is the half of a research answer that does not depend on the
-- model. Synthesis can fail — a token budget runs out, a provider is down —
-- and the existing degraded path already returns the retrieved sources when it
-- does. Arithmetic over the price series has no such dependency, so there is
-- no reason for a reader to lose it too: "RELIANCE is down 12% over six months
-- with 24% annualised volatility" is worth having on its own, and a question
-- about performance is very often answered by exactly that.
--
-- Stored as JSONB for the same reason sources are: this is a record of what
-- was computed at the time, not a table to query across.
ALTER TABLE research_turns
    ADD COLUMN IF NOT EXISTS measurements JSONB NOT NULL DEFAULT '[]'::jsonb;
