-- Indexes for the default market feed.
--
-- The feed asks for the last 72 hours of attributable events, newest first.
-- Every part of that was unindexed, so the query read all 93,402 events and
-- evaluated three correlated subqueries against each: 904 ms cold, 255 ms
-- warm, growing linearly with the archive.
--
-- Two things were missing.

-- 1. The age filter runs on an expression, not a column: publication time,
--    clamped to discovery when it is missing or implausibly later. No index
--    on published_at or discovered_at can serve that, so the planner had no
--    choice but a sequential scan.
--
--    This index must stay structurally identical to contentAgeExpr in
--    events_read.go. Postgres matches an expression index by its parsed
--    tree, so drift between the two does not fail loudly -- it silently puts
--    the full scan back.
--
--    Ordered DESC with id DESC as the tiebreak so it also satisfies the
--    feed's ORDER BY, which lets the scan stop at the requested page instead
--    of sorting the whole window.
CREATE INDEX IF NOT EXISTS events_content_age_idx ON events (
    (CASE
        WHEN published_at IS NULL THEN discovered_at
        WHEN published_at > discovered_at THEN discovered_at
        ELSE published_at
     END) DESC,
    id DESC
);

-- 2. The feed excludes events whose only evidence came from a per-symbol
--    watchlist search. Testing that meant a sequential scan of all 131,469
--    evidence rows, because the NOT LIKE could not be indexed from the
--    primary key. A partial index stores only the rows that qualify, which
--    is both the filter and the answer.
CREATE INDEX IF NOT EXISTS event_evidence_attributable_idx
    ON event_evidence (event_id) WHERE source_id NOT LIKE 'watch-%';
