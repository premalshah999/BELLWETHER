-- Price-and-news analysis kept with each research turn, so a conversation
-- reopens with its charts.
ALTER TABLE research_turns ADD COLUMN IF NOT EXISTS analyses JSONB NOT NULL DEFAULT '[]'::jsonb;
