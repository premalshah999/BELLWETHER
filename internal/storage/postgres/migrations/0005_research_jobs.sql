-- Research turns become jobs rather than request-scoped work.
--
-- Previously the whole search-and-synthesise cycle ran inside the HTTP
-- request. Navigating to another tab abandoned it: the component unmounted,
-- the request was dropped, and a minute of retrieval and a paid model call
-- went with it. Worse, there was nothing to look at while it ran, so a
-- ninety-second wait looked identical to a hang.
--
-- Making the turn a row that starts in 'running' and is updated as the work
-- proceeds fixes both. The answer survives navigation because it was never in
-- the browser, and progress is visible because it is recorded.

DO $$ BEGIN
    CREATE TYPE research_status AS ENUM ('running', 'done', 'failed');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE research_turns
    ADD COLUMN IF NOT EXISTS status research_status NOT NULL DEFAULT 'done',
    -- A short machine-readable stage: rewriting, searching, synthesising.
    ADD COLUMN IF NOT EXISTS stage TEXT NOT NULL DEFAULT '',
    -- The running commentary, appended to as the work proceeds. This is what
    -- the interface shows instead of a spinner: which providers answered,
    -- how many documents came back, what is being done with them.
    ADD COLUMN IF NOT EXISTS progress JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT '';

-- Finding work abandoned by a restart. A process that dies mid-turn leaves a
-- row in 'running' forever, and without this it would be invisible.
CREATE INDEX IF NOT EXISTS research_turns_running_idx
    ON research_turns (started_at) WHERE status = 'running';
