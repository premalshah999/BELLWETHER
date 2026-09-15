-- The body of a research report.
--
-- The answer column holds the summary; this holds the sections beneath it.
-- Split rather than concatenated into one blob of prose because the reader
-- navigates by heading, and because a section carries its own citations.
ALTER TABLE research_turns
    ADD COLUMN IF NOT EXISTS sections JSONB NOT NULL DEFAULT '[]'::jsonb;
