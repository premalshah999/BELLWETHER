package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/search"
)

// LLMTokensUsed reports total tokens consumed in a period.
func (s *DB) LLMTokensUsed(ctx context.Context, period string) (int, error) {
	var total sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT SUM(total_tokens) FROM llm_usage WHERE period = ?`, period).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sqlite: llm tokens used: %w", err)
	}
	return int(total.Int64), nil
}

// RecordLLMUsage stores one call's consumption.
func (s *DB) RecordLLMUsage(ctx context.Context, rec ai.UsageRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO llm_usage (period, feature, model, prompt_tokens, completion_tokens, total_tokens, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.Period, rec.Feature, rec.Model,
		rec.Usage.PromptTokens, rec.Usage.CompletionTokens, rec.Usage.TotalTokens,
		rec.At.UTC().Unix())
	if err != nil {
		return fmt.Errorf("sqlite: record llm usage: %w", err)
	}
	return nil
}

// LLMUsageByFeature breaks a period's spend down per feature.
func (s *DB) LLMUsageByFeature(ctx context.Context, period string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT feature, SUM(total_tokens) FROM llm_usage WHERE period = ? GROUP BY feature`, period)
	if err != nil {
		return nil, fmt.Errorf("sqlite: llm usage by feature: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var (
			feature string
			total   sql.NullInt64
		)
		if err := rows.Scan(&feature, &total); err != nil {
			return nil, fmt.Errorf("sqlite: scan llm usage: %w", err)
		}
		out[feature] = int(total.Int64)
	}
	return out, rows.Err()
}

// SaveOutput stores an AI response for the archive.
func (s *DB) SaveOutput(ctx context.Context, out *ai.Output) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO ai_outputs (kind, symbol, created_at, model, tokens, content)
VALUES (?, ?, ?, ?, ?, ?)`,
		out.Kind, out.Symbol, out.CreatedAt.UTC().Unix(), out.Model, out.Tokens, string(out.Content))
	if err != nil {
		return 0, fmt.Errorf("sqlite: save ai output: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: ai output id: %w", err)
	}
	out.ID = id
	return id, nil
}

// LatestOutput returns the most recent output of a kind, optionally for one
// symbol. It is what stops a page load regenerating a brief.
func (s *DB) LatestOutput(ctx context.Context, kind, symbol string) (ai.Output, bool, error) {
	query := `
SELECT id, kind, symbol, created_at, model, tokens, content
FROM ai_outputs WHERE kind = ?`
	args := []any{kind}
	if symbol != "" {
		query += " AND symbol = ?"
		args = append(args, symbol)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT 1"

	out, err := scanOutput(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return ai.Output{}, false, nil
	}
	if err != nil {
		return ai.Output{}, false, err
	}
	return out, true, nil
}

// ListOutputs returns recent outputs of a kind, newest first.
func (s *DB) ListOutputs(ctx context.Context, kind string, limit int) ([]ai.Output, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, kind, symbol, created_at, model, tokens, content FROM ai_outputs`
	args := []any{}
	if kind != "" {
		query += " WHERE kind = ?"
		args = append(args, kind)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list ai outputs: %w", err)
	}
	defer rows.Close()

	var out []ai.Output
	for rows.Next() {
		o, err := scanOutput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func scanOutput(r rowScanner) (ai.Output, error) {
	var (
		o         ai.Output
		createdAt int64
		content   string
	)
	if err := r.Scan(&o.ID, &o.Kind, &o.Symbol, &createdAt, &o.Model, &o.Tokens, &content); err != nil {
		return ai.Output{}, err
	}
	o.CreatedAt = time.Unix(createdAt, 0).UTC()
	o.Content = []byte(content)
	return o, nil
}

// SaveOutlook logs a scenario forecast for later scoring.
func (s *DB) SaveOutlook(ctx context.Context, o *ai.Outlook) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO outlooks (
    symbol, created_at, horizon_days, price_at_creation,
    base_probability, base_move, base_reasoning,
    bull_probability, bull_move, bull_reasoning,
    bear_probability, bear_move, bear_reasoning,
    key_risk, model)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.Symbol, o.CreatedAt.UTC().Unix(), o.HorizonDays, o.PriceAtCreation,
		o.Base.Probability, o.Base.MovePercent, o.Base.Reasoning,
		o.Bull.Probability, o.Bull.MovePercent, o.Bull.Reasoning,
		o.Bear.Probability, o.Bear.MovePercent, o.Bear.Reasoning,
		o.KeyRisk, o.Model)
	if err != nil {
		return 0, fmt.Errorf("sqlite: save outlook: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite: outlook id: %w", err)
	}
	o.ID = id
	return id, nil
}

const outlookColumns = `
    id, symbol, created_at, horizon_days, price_at_creation,
    base_probability, base_move, base_reasoning,
    bull_probability, bull_move, bull_reasoning,
    bear_probability, bear_move, bear_reasoning,
    key_risk, model,
    resolved_at, realized_price, realized_move, actual_scenario, brier_score`

func scanOutlook(r rowScanner) (ai.Outlook, error) {
	var (
		o             ai.Outlook
		createdAt     int64
		resolvedAt    *int64
		realizedPrice *float64
		realizedMove  *float64
		brier         *float64
	)
	err := r.Scan(&o.ID, &o.Symbol, &createdAt, &o.HorizonDays, &o.PriceAtCreation,
		&o.Base.Probability, &o.Base.MovePercent, &o.Base.Reasoning,
		&o.Bull.Probability, &o.Bull.MovePercent, &o.Bull.Reasoning,
		&o.Bear.Probability, &o.Bear.MovePercent, &o.Bear.Reasoning,
		&o.KeyRisk, &o.Model,
		&resolvedAt, &realizedPrice, &realizedMove, &o.ActualScenario, &brier)
	if err != nil {
		return ai.Outlook{}, err
	}
	o.CreatedAt = time.Unix(createdAt, 0).UTC()
	if resolvedAt != nil {
		t := time.Unix(*resolvedAt, 0).UTC()
		o.ResolvedAt = &t
	}
	o.RealizedPrice = realizedPrice
	o.RealizedMove = realizedMove
	o.BrierScore = brier
	return o, nil
}

// ListOutlooks returns outlooks, newest first, optionally for one symbol.
func (s *DB) ListOutlooks(ctx context.Context, symbol string, limit int) ([]ai.Outlook, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	query := `SELECT ` + outlookColumns + ` FROM outlooks`
	args := []any{}
	if symbol != "" {
		query += " WHERE symbol = ?"
		args = append(args, symbol)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list outlooks: %w", err)
	}
	defer rows.Close()

	var out []ai.Outlook
	for rows.Next() {
		o, err := scanOutlook(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan outlook: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DueOutlooks returns unresolved outlooks whose horizon has elapsed.
//
// The horizon is expressed in trading days and converted to calendar days at
// the usual five-in-seven ratio, matching Outlook.DueAt.
func (s *DB) DueOutlooks(ctx context.Context, before time.Time) ([]ai.Outlook, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+outlookColumns+`
FROM outlooks
WHERE resolved_at IS NULL
  AND (created_at + (horizon_days * 7 / 5) * 86400) < ?
ORDER BY created_at ASC
LIMIT 200`, before.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("sqlite: due outlooks: %w", err)
	}
	defer rows.Close()

	var out []ai.Outlook
	for rows.Next() {
		o, err := scanOutlook(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan due outlook: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ResolveOutlook records the scoring of an outlook.
func (s *DB) ResolveOutlook(ctx context.Context, o *ai.Outlook) error {
	if o.ResolvedAt == nil {
		return fmt.Errorf("sqlite: cannot resolve outlook %d without a resolution time", o.ID)
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE outlooks
SET resolved_at = ?, realized_price = ?, realized_move = ?, actual_scenario = ?, brier_score = ?
WHERE id = ? AND resolved_at IS NULL`,
		o.ResolvedAt.UTC().Unix(), o.RealizedPrice, o.RealizedMove, o.ActualScenario, o.BrierScore, o.ID)
	if err != nil {
		return fmt.Errorf("sqlite: resolve outlook: %w", err)
	}
	// A zero row count means it was already resolved. Re-scoring an outlook
	// would let a later price rewrite history, so that is left alone.
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	return nil
}

// LoadSearch reads a cached search response.
func (s *DB) LoadSearch(ctx context.Context, key string) (search.Results, bool, error) {
	var (
		raw       string
		fetchedAt int64
		query     string
		provider  string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT query, provider, results, fetched_at FROM search_cache WHERE key = ?`, key).
		Scan(&query, &provider, &raw, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return search.Results{}, false, nil
	}
	if err != nil {
		return search.Results{}, false, fmt.Errorf("sqlite: load search: %w", err)
	}

	var out search.Results
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// A stored payload that no longer decodes is treated as a miss.
		return search.Results{}, false, nil
	}
	out.Query, out.Provider = query, provider
	out.FetchedAt = time.Unix(fetchedAt, 0).UTC()
	return out, true, nil
}

// SaveSearch stores a search response.
func (s *DB) SaveSearch(ctx context.Context, key string, r search.Results) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("sqlite: encode search results: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO search_cache (key, query, provider, results, fetched_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (key) DO UPDATE SET
    query = excluded.query, provider = excluded.provider,
    results = excluded.results, fetched_at = excluded.fetched_at`,
		key, r.Query, r.Provider, string(raw), r.FetchedAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("sqlite: save search: %w", err)
	}
	return nil
}
