-- The instruments a market feed should default to.
--
-- India has roughly 2,500 listed companies and almost all of them file
-- something on any given day: board meeting notices, e-voting intimations,
-- newspaper publication copies. Measured over 24 hours, 1,258 events at
-- importance 4 or above were spread across 549 distinct companies. A feed
-- that treats all of them alike is not a market feed, it is the exchange's
-- filing queue, and the handful of items an operator actually needs are
-- buried in it.
--
-- Importance cannot fix this on its own. A board meeting notice is a genuine
-- importance-4 event whoever files it; what differs is whether anyone cares
-- about the filer. That is a property of the company, not of the event, so it
-- belongs in a table about companies.
--
-- Populated at startup from the NSE index constituent list already embedded
-- in the binary, so this stays in step with the company master rather than
-- becoming a second thing to maintain.
CREATE TABLE IF NOT EXISTS index_constituents (
    symbol     TEXT        PRIMARY KEY,
    industry   TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
