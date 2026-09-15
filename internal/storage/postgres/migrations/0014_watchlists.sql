-- Named watchlists, replacing the single implicit one.
--
-- One list forces every purpose into the same view: the names being traded
-- today, a longer-term holding set, and a handful being researched all sit
-- together, and the scanner, the algorithms and the price stream cannot tell
-- them apart. Separate lists let an algorithm watch "my positions" without
-- also firing on everything under consideration.
--
-- Capped at five by the application rather than the schema. The limit exists
-- because each list is polled live during market hours, and it is a product
-- decision that belongs where it can be explained to a person, not a
-- constraint that surfaces as a database error.
CREATE TABLE IF NOT EXISTS watchlists (
    id         BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT        NOT NULL CHECK (name <> ''),
    position   INTEGER     NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT watchlists_name_unique UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS watchlist_items (
    watchlist_id BIGINT      NOT NULL REFERENCES watchlists(id) ON DELETE CASCADE,
    symbol       TEXT        NOT NULL,
    note         TEXT        NOT NULL DEFAULT '',
    position     INTEGER     NOT NULL DEFAULT 0,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (watchlist_id, symbol)
);

CREATE INDEX IF NOT EXISTS watchlist_items_symbol_idx ON watchlist_items (symbol);

-- Carry the existing list across as "Main", so nothing is lost and the
-- application has a list to open on first run.
INSERT INTO watchlists (name, position)
SELECT 'Main', 0
WHERE NOT EXISTS (SELECT 1 FROM watchlists);

INSERT INTO watchlist_items (watchlist_id, symbol, note, position, added_at)
SELECT (SELECT id FROM watchlists ORDER BY position, id LIMIT 1),
       w.symbol, w.note, w.position, w.added_at
FROM watchlist w
ON CONFLICT (watchlist_id, symbol) DO NOTHING;
