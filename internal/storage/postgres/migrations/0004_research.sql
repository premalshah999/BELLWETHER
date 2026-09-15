-- Research conversations.
--
-- Research was a single request-response: a query in, an answer out, nothing
-- kept. That makes the second question as expensive as the first and unable to
-- build on it, which is the wrong shape for how the work is actually done —
-- an operator asks something, reads the answer, and then asks the question the
-- answer raised.
--
-- Storing the exchange makes follow-ups possible and makes the reasoning
-- auditable weeks later, which for a tool that informs decisions matters more
-- than the convenience.

CREATE TABLE IF NOT EXISTS research_conversations (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- The opening question, kept as the conversation's name so a list of them
    -- reads as a list of questions rather than of identifiers.
    title       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Denormalised so the list view needs one query rather than a join and an
    -- aggregate per row.
    turn_count  INTEGER     NOT NULL DEFAULT 0,
    -- Instruments referenced anywhere in the conversation, so a company page
    -- can surface the research done about it.
    symbols     TEXT[]      NOT NULL DEFAULT '{}',
    archived    BOOLEAN     NOT NULL DEFAULT FALSE,

    CONSTRAINT research_conversations_title_present CHECK (title <> '')
);

CREATE INDEX IF NOT EXISTS research_conversations_recent_idx
    ON research_conversations (updated_at DESC) WHERE NOT archived;
CREATE INDEX IF NOT EXISTS research_conversations_symbols_idx
    ON research_conversations USING GIN (symbols);

-- One exchange: what was asked, what was retrieved, what was concluded.
--
-- The retrieved sources are stored alongside the answer rather than being
-- re-fetched, because the answer cites them by number and a citation that
-- points at a different document than it did when written is worse than no
-- citation. This is the same discipline as event evidence: keep what the
-- conclusion rested on.
CREATE TABLE IF NOT EXISTS research_turns (
    id              BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT      NOT NULL REFERENCES research_conversations (id) ON DELETE CASCADE,
    seq             INTEGER     NOT NULL,

    question        TEXT        NOT NULL,
    -- The query actually sent to the scrapers, which for a follow-up is
    -- rewritten from the question plus the conversation so far. Kept because
    -- a disappointing result is usually a bad rewrite, and without this there
    -- is no way to see that.
    search_query    TEXT        NOT NULL DEFAULT '',

    answer          TEXT        NOT NULL DEFAULT '',
    findings        JSONB       NOT NULL DEFAULT '[]'::jsonb,
    companies       JSONB       NOT NULL DEFAULT '[]'::jsonb,
    gaps            JSONB       NOT NULL DEFAULT '[]'::jsonb,
    followups       JSONB       NOT NULL DEFAULT '[]'::jsonb,
    sources         JSONB       NOT NULL DEFAULT '[]'::jsonb,
    providers       JSONB       NOT NULL DEFAULT '[]'::jsonb,

    model           TEXT        NOT NULL DEFAULT '',
    -- Degraded means retrieval succeeded but synthesis did not. Recorded
    -- rather than inferred, so a thin answer is explainable later.
    degraded        BOOLEAN     NOT NULL DEFAULT FALSE,
    note            TEXT        NOT NULL DEFAULT '',
    elapsed_ms      INTEGER     NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT research_turns_order UNIQUE (conversation_id, seq),
    CONSTRAINT research_turns_question_present CHECK (question <> '')
);

CREATE INDEX IF NOT EXISTS research_turns_conversation_idx
    ON research_turns (conversation_id, seq);
