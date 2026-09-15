-- A short explanation of one event, generated once and kept.
--
-- The feed answers "what happened". It did not answer "so what", and the
-- headline of a filing rarely does either: "Time Technoplast Limited:
-- securing order of Rs. 250 crore" tells a reader who already follows the
-- company something, and everyone else nothing.
--
-- Keyed by event rather than stored in ai_outputs, which is keyed by symbol:
-- an event can name several companies or none, and forcing it through a
-- symbol column would either duplicate the brief or lose it.
CREATE TABLE IF NOT EXISTS event_briefs (
    event_id   BIGINT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    brief      TEXT NOT NULL,
    model      TEXT NOT NULL DEFAULT '',
    tokens     INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
