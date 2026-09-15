package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/research"
)

// CreateConversation starts a research thread.
func (d *DB) CreateConversation(ctx context.Context, title string) (int64, error) {
	var id int64
	err := d.db.QueryRowContext(ctx,
		`INSERT INTO research_conversations (title) VALUES ($1) RETURNING id`, title).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("postgres: create conversation: %w", err)
	}
	return id, nil
}

// ListConversations returns threads, most recently used first.
func (d *DB) ListConversations(ctx context.Context, limit int, includeArchived bool) ([]research.Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := "WHERE NOT archived"
	if includeArchived {
		where = ""
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT id, title, created_at, updated_at, turn_count, symbols, archived
FROM research_conversations `+where+`
ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list conversations: %w", err)
	}
	defer rows.Close()

	var out []research.Conversation
	for rows.Next() {
		var (
			c       research.Conversation
			symbols stringArray
		)
		if err := rows.Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt,
			&c.TurnCount, &symbols, &c.Archived); err != nil {
			return nil, fmt.Errorf("postgres: scan conversation: %w", err)
		}
		c.Symbols = symbols
		c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConversation returns a thread with every turn.
func (d *DB) GetConversation(ctx context.Context, id int64) (research.Conversation, error) {
	var (
		c       research.Conversation
		symbols stringArray
	)
	err := d.db.QueryRowContext(ctx, `
SELECT id, title, created_at, updated_at, turn_count, symbols, archived
FROM research_conversations WHERE id = $1`, id).Scan(
		&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt, &c.TurnCount, &symbols, &c.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		return c, sql.ErrNoRows
	}
	if err != nil {
		return c, fmt.Errorf("postgres: get conversation: %w", err)
	}
	c.Symbols = symbols
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()

	rows, err := d.db.QueryContext(ctx, `
SELECT id, conversation_id, seq, question, search_query, answer,
       findings, companies, gaps, followups, sources, providers, measurements,
       sections,
       model, degraded, note, elapsed_ms, created_at,
       status::text, stage, progress, started_at, finished_at, error
FROM research_turns WHERE conversation_id = $1 ORDER BY seq`, id)
	if err != nil {
		return c, fmt.Errorf("postgres: load turns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			t                                    research.Turn
			findings, companies, gaps, followups []byte
			sources, providers, progress         []byte
			measurements, sections               []byte
			status                               string
			startedAt, finishedAt                sql.NullTime
		)
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.Seq, &t.Question, &t.SearchQuery,
			&t.Answer, &findings, &companies, &gaps, &followups, &sources, &providers,
			&measurements, &sections,
			&t.Model, &t.Degraded, &t.Note, &t.ElapsedMS, &t.CreatedAt,
			&status, &t.Stage, &progress, &startedAt, &finishedAt, &t.Error); err != nil {
			return c, fmt.Errorf("postgres: scan turn: %w", err)
		}
		t.CreatedAt = t.CreatedAt.UTC()
		t.Status = research.Status(status)
		t.StartedAt = timeOrZero(startedAt)
		t.FinishedAt = timeOrZero(finishedAt)
		_ = json.Unmarshal(progress, &t.Progress)
		// A payload that no longer decodes leaves that one field empty rather
		// than failing the whole thread. The question and answer are the
		// parts a reader needs; the structured extras are enrichment.
		_ = json.Unmarshal(findings, &t.Findings)
		_ = json.Unmarshal(companies, &t.Companies)
		_ = json.Unmarshal(gaps, &t.Gaps)
		_ = json.Unmarshal(followups, &t.Followups)
		_ = json.Unmarshal(sources, &t.Sources)
		_ = json.Unmarshal(measurements, &t.Measurements)
		_ = json.Unmarshal(sections, &t.Sections)
		_ = json.Unmarshal(providers, &t.Providers)
		c.Turns = append(c.Turns, t)
	}
	return c, rows.Err()
}

// StartTurn records a question and returns before any work begins.
//
// This is what makes a research request survive the browser. The row exists in
// 'running' from the moment the question is asked, so a client that navigates
// away and comes back finds the work in progress rather than discovering that
// it evaporated with the component.
func (d *DB) StartTurn(ctx context.Context, conversationID int64, question string) (*research.Turn, error) {
	t := &research.Turn{
		ConversationID: conversationID,
		Question:       question,
		Status:         research.StatusRunning,
		Stage:          "queued",
	}
	err := d.db.QueryRowContext(ctx, `
INSERT INTO research_turns (conversation_id, seq, question, status, stage, started_at)
VALUES ($1,
        COALESCE((SELECT max(seq) + 1 FROM research_turns WHERE conversation_id = $1), 1),
        $2, 'running', 'queued', now())
RETURNING id, seq, started_at, created_at`,
		conversationID, question).Scan(&t.ID, &t.Seq, &t.StartedAt, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("postgres: start turn: %w", err)
	}
	t.StartedAt, t.CreatedAt = t.StartedAt.UTC(), t.CreatedAt.UTC()

	// The thread's timestamp moves now, not on completion, so a running
	// question sorts to the top of the list where the reader left it.
	if _, err := d.db.ExecContext(ctx, `
UPDATE research_conversations
SET turn_count = (SELECT count(*) FROM research_turns WHERE conversation_id = $1),
    updated_at = now()
WHERE id = $1`, conversationID); err != nil {
		return nil, fmt.Errorf("postgres: touch conversation: %w", err)
	}
	return t, nil
}

// RecordProgress appends one line of commentary to a running turn.
//
// Appending in SQL rather than reading, modifying and writing means two
// concurrent updates cannot lose one another's line — which matters because
// the provider fan-out reports as each one returns.
func (d *DB) RecordProgress(ctx context.Context, turnID int64, stage, detail string) error {
	step, err := json.Marshal(research.Step{At: time.Now().UTC(), Stage: stage, Detail: detail})
	if err != nil {
		return fmt.Errorf("postgres: encode progress: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `
UPDATE research_turns
SET progress = progress || $2::jsonb, stage = $3
WHERE id = $1 AND status = 'running'`, turnID, step, stage); err != nil {
		return fmt.Errorf("postgres: record progress: %w", err)
	}
	return nil
}

// CompleteTurn stores the finished answer.
func (d *DB) CompleteTurn(ctx context.Context, t *research.Turn) error {
	findings, err := json.Marshal(orEmpty(t.Findings))
	if err != nil {
		return fmt.Errorf("postgres: encode findings: %w", err)
	}
	companies, err := json.Marshal(orEmpty(t.Companies))
	if err != nil {
		return fmt.Errorf("postgres: encode companies: %w", err)
	}
	gaps, _ := json.Marshal(orEmptyStrings(t.Gaps))
	followups, _ := json.Marshal(orEmptyStrings(t.Followups))
	sources, err := json.Marshal(orEmpty(t.Sources))
	if err != nil {
		return fmt.Errorf("postgres: encode sources: %w", err)
	}
	providers, _ := json.Marshal(orEmpty(t.Providers))
	measurements, _ := json.Marshal(orEmpty(t.Measurements))
	sections, _ := json.Marshal(orEmpty(t.Sections))

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: begin complete turn: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
UPDATE research_turns SET
    search_query = $2, answer = $3, findings = $4::jsonb, companies = $5::jsonb,
    gaps = $6::jsonb, followups = $7::jsonb, sources = $8::jsonb, providers = $9::jsonb,
    measurements = $14::jsonb, sections = $15::jsonb,
    model = $10, degraded = $11, note = $12, elapsed_ms = $13,
    status = 'done', stage = 'done', finished_at = now()
WHERE id = $1`,
		t.ID, t.SearchQuery, t.Answer, findings, companies, gaps, followups,
		sources, providers, t.Model, t.Degraded, t.Note, t.ElapsedMS,
		measurements, sections); err != nil {
		return fmt.Errorf("postgres: complete turn: %w", err)
	}

	symbols := make([]string, 0, len(t.Companies))
	for _, c := range t.Companies {
		symbols = append(symbols, c.Symbol)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE research_conversations SET
    symbols    = ARRAY(SELECT DISTINCT unnest(symbols || $2::text[])),
    updated_at = now()
WHERE id = $1`, t.ConversationID, stringArray(symbols)); err != nil {
		return fmt.Errorf("postgres: update conversation symbols: %w", err)
	}
	return tx.Commit()
}

// FailTurn records that the work could not be completed.
//
// A failed turn stays in the thread rather than being deleted. The question
// was asked, and a thread that silently drops it leaves the reader wondering
// whether they imagined asking.
func (d *DB) FailTurn(ctx context.Context, turnID int64, reason string) error {
	if _, err := d.db.ExecContext(ctx, `
UPDATE research_turns
SET status = 'failed', stage = 'failed', error = $2, finished_at = now()
WHERE id = $1 AND status = 'running'`, turnID, reason); err != nil {
		return fmt.Errorf("postgres: fail turn: %w", err)
	}
	return nil
}

// ReapAbandonedTurns fails turns left running by a process that died.
//
// Without this a crash mid-research leaves a row spinning forever, and the
// interface would show a question permanently in progress with nothing behind
// it. Run at startup and periodically.
func (d *DB) ReapAbandonedTurns(ctx context.Context, olderThan time.Duration) (int, error) {
	res, err := d.db.ExecContext(ctx, `
UPDATE research_turns
SET status = 'failed', stage = 'abandoned', finished_at = now(),
    error = 'the process handling this request stopped before it finished'
WHERE status = 'running' AND started_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(olderThan.Seconds())))
	if err != nil {
		return 0, fmt.Errorf("postgres: reap abandoned turns: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ArchiveConversation hides or restores a thread.
func (d *DB) ArchiveConversation(ctx context.Context, id int64, archived bool) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE research_conversations SET archived = $2, updated_at = now() WHERE id = $1`,
		id, archived)
	if err != nil {
		return fmt.Errorf("postgres: archive conversation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteConversation removes a thread and its turns.
func (d *DB) DeleteConversation(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM research_conversations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete conversation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// orEmpty renders a nil slice as an empty JSON array rather than null, so the
// JSONB column always holds a value of the shape the reader expects.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
