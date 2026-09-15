-- Let an algorithm watch a list rather than a fixed set of symbols.
--
-- Naming symbols on the algorithm freezes it: adding an instrument to a
-- portfolio means remembering to edit every rule that should cover it, and the
-- rules quietly drift out of step with what is actually held. Pointing a rule
-- at a list makes membership one fact in one place.
--
-- Both remain available. A rule about a single instrument is clearer written
-- against that instrument, and the symbols column stays the way to say so; the
-- evaluated set is the union of the two.
ALTER TABLE algorithms
    ADD COLUMN IF NOT EXISTS watchlist_ids BIGINT[] NOT NULL DEFAULT '{}';
