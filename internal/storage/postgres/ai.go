package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/jev"
)

// LLMTokensUsed reports total consumption in a period.
func (d *DB) LLMTokensUsed(ctx context.Context, period string) (int, error) {
	var total sql.NullInt64
	if err := d.db.QueryRowContext(ctx,
		`SELECT sum(total_tokens) FROM llm_usage WHERE period = $1`, period).Scan(&total); err != nil {
		return 0, fmt.Errorf("postgres: llm tokens used: %w", err)
	}
	return int(total.Int64), nil
}

// RecordLLMUsage appends one usage record.
func (d *DB) RecordLLMUsage(ctx context.Context, rec ai.UsageRecord) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO llm_usage (period, feature, model, prompt_tokens, completion_tokens, total_tokens,
                       cache_hit_tokens, cache_miss_tokens, cost_usd, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		rec.Period, rec.Feature, rec.Model,
		rec.Usage.PromptTokens, rec.Usage.CompletionTokens, rec.Usage.TotalTokens,
		rec.Usage.CacheHitTokens, rec.Usage.CacheMissTokens, rec.CostUSD, rec.At.UTC())
	if err != nil {
		return fmt.Errorf("postgres: record llm usage: %w", err)
	}
	return nil
}

// LLMUsageByFeature breaks a period's consumption down by feature.
func (d *DB) LLMUsageByFeature(ctx context.Context, period string) (map[string]int, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT feature, sum(total_tokens) FROM llm_usage WHERE period = $1 GROUP BY feature`, period)
	if err != nil {
		return nil, fmt.Errorf("postgres: llm usage by feature: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var (
			feature string
			total   sql.NullInt64
		)
		if err := rows.Scan(&feature, &total); err != nil {
			return nil, fmt.Errorf("postgres: scan llm usage: %w", err)
		}
		out[feature] = int(total.Int64)
	}
	return out, rows.Err()
}

// SaveOutput stores a generated AI artefact.
func (d *DB) SaveOutput(ctx context.Context, out *ai.Output) (int64, error) {
	var id int64
	// Content is JSONB, so a malformed payload is rejected at the write
	// rather than discovered when the archive page tries to render it.
	err := d.db.QueryRowContext(ctx, `
INSERT INTO ai_outputs (kind, symbol, created_at, model, tokens, content)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		out.Kind, out.Symbol, out.CreatedAt.UTC(), out.Model, out.Tokens, []byte(out.Content)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("postgres: save ai output: %w", err)
	}
	out.ID = id
	return id, nil
}

const aiOutputColumns = `id, kind, symbol, created_at, model, tokens, content`

func scanOutput(r rowScanner) (ai.Output, error) {
	var (
		o       ai.Output
		content []byte
	)
	if err := r.Scan(&o.ID, &o.Kind, &o.Symbol, &o.CreatedAt, &o.Model, &o.Tokens, &content); err != nil {
		return ai.Output{}, err
	}
	o.CreatedAt = o.CreatedAt.UTC()
	o.Content = content
	return o, nil
}

// LatestOutput returns the newest artefact of a kind.
func (d *DB) LatestOutput(ctx context.Context, kind, symbol string) (ai.Output, bool, error) {
	query := `SELECT ` + aiOutputColumns + ` FROM ai_outputs WHERE kind = $1`
	args := []any{kind}
	if symbol != "" {
		query += " AND symbol = $2"
		args = append(args, symbol)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT 1"

	out, err := scanOutput(d.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return ai.Output{}, false, nil
	}
	if err != nil {
		return ai.Output{}, false, fmt.Errorf("postgres: latest ai output: %w", err)
	}
	return out, true, nil
}

const outlookColumns = `id, symbol, created_at, horizon_days, price_at_creation,
    base_probability, base_move, base_reasoning,
    bull_probability, bull_move, bull_reasoning,
    bear_probability, bear_move, bear_reasoning,
    key_risk, model, resolved_at, realized_price, realized_move,
    actual_scenario, brier_score`

func scanOutlook(r rowScanner) (ai.Outlook, error) {
	var (
		o                           ai.Outlook
		resolvedAt                  sql.NullTime
		realizedPrice, realizedMove sql.NullFloat64
		brier                       sql.NullFloat64
	)
	if err := r.Scan(&o.ID, &o.Symbol, &o.CreatedAt, &o.HorizonDays, &o.PriceAtCreation,
		&o.Base.Probability, &o.Base.MovePercent, &o.Base.Reasoning,
		&o.Bull.Probability, &o.Bull.MovePercent, &o.Bull.Reasoning,
		&o.Bear.Probability, &o.Bear.MovePercent, &o.Bear.Reasoning,
		&o.KeyRisk, &o.Model, &resolvedAt, &realizedPrice, &realizedMove,
		&o.ActualScenario, &brier); err != nil {
		return ai.Outlook{}, err
	}
	o.CreatedAt = o.CreatedAt.UTC()
	if resolvedAt.Valid {
		t := resolvedAt.Time.UTC()
		o.ResolvedAt = &t
	}
	// These stay nil when unresolved. A pointer distinguishes "not scored
	// yet" from "scored zero", and a Brier score of zero is a perfect
	// forecast rather than a missing one.
	if realizedPrice.Valid {
		v := realizedPrice.Float64
		o.RealizedPrice = &v
	}
	if realizedMove.Valid {
		v := realizedMove.Float64
		o.RealizedMove = &v
	}
	if brier.Valid {
		v := brier.Float64
		o.BrierScore = &v
	}
	return o, nil
}

// SaveOutlook records a forecast.
func (d *DB) SaveOutlook(ctx context.Context, o *ai.Outlook) (int64, error) {
	var id int64
	err := d.db.QueryRowContext(ctx, `
INSERT INTO outlooks (
    symbol, created_at, horizon_days, price_at_creation,
    base_probability, base_move, base_reasoning,
    bull_probability, bull_move, bull_reasoning,
    bear_probability, bear_move, bear_reasoning,
    key_risk, model)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING id`,
		o.Symbol, o.CreatedAt.UTC(), o.HorizonDays, o.PriceAtCreation,
		o.Base.Probability, o.Base.MovePercent, o.Base.Reasoning,
		o.Bull.Probability, o.Bull.MovePercent, o.Bull.Reasoning,
		o.Bear.Probability, o.Bear.MovePercent, o.Bear.Reasoning,
		o.KeyRisk, o.Model).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("postgres: save outlook: %w", err)
	}
	o.ID = id
	return id, nil
}

// ListOutlooks returns recorded forecasts.
func (d *DB) ListOutlooks(ctx context.Context, symbol string, limit int) ([]ai.Outlook, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	query := `SELECT ` + outlookColumns + ` FROM outlooks`
	args := []any{}
	if symbol != "" {
		query += " WHERE symbol = $1"
		args = append(args, symbol)
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list outlooks: %w", err)
	}
	defer rows.Close()

	var out []ai.Outlook
	for rows.Next() {
		o, err := scanOutlook(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan outlook: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DueOutlooks returns unresolved forecasts whose horizon has elapsed.
//
// The horizon is in trading days, so it is stretched by seven fifths to reach
// a calendar date. Postgres does that arithmetic in interval terms rather than
// in seconds, which keeps it correct across a daylight-saving boundary — not
// something India observes, but the global feeds this system reads do.
func (d *DB) DueOutlooks(ctx context.Context, before time.Time) ([]ai.Outlook, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT `+outlookColumns+` FROM outlooks
WHERE resolved_at IS NULL
  AND created_at + make_interval(days => (horizon_days * 7 / 5)) < $1
ORDER BY created_at ASC
LIMIT 200`, before.UTC())
	if err != nil {
		return nil, fmt.Errorf("postgres: due outlooks: %w", err)
	}
	defer rows.Close()

	var out []ai.Outlook
	for rows.Next() {
		o, err := scanOutlook(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan due outlook: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ResolveOutlook scores a forecast against what happened.
//
// The guard on resolved_at makes this idempotent: a second attempt affects no
// rows rather than overwriting a score with a later, different one.
func (d *DB) ResolveOutlook(ctx context.Context, o *ai.Outlook) error {
	if o.ResolvedAt == nil {
		return fmt.Errorf("postgres: cannot resolve outlook %d without a resolution time", o.ID)
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE outlooks
SET resolved_at = $1, realized_price = $2, realized_move = $3,
    actual_scenario = $4, brier_score = $5
WHERE id = $6 AND resolved_at IS NULL`,
		o.ResolvedAt.UTC(), o.RealizedPrice, o.RealizedMove,
		o.ActualScenario, o.BrierScore, o.ID)
	if err != nil {
		return fmt.Errorf("postgres: resolve outlook: %w", err)
	}
	return nil
}

// LLMSpendSince sums the cost of calls recorded at or after a moment.
func (d *DB) LLMSpendSince(ctx context.Context, since time.Time) (float64, error) {
	var usd float64
	if err := d.db.QueryRowContext(ctx,
		`SELECT COALESCE(sum(cost_usd), 0) FROM llm_usage WHERE created_at >= $1`,
		since.UTC()).Scan(&usd); err != nil {
		return 0, fmt.Errorf("postgres: llm spend since: %w", err)
	}
	return usd, nil
}

// RecordJevUsage stores one Jev request's consumption and cost.
func (d *DB) RecordJevUsage(ctx context.Context, feature string, u jev.Usage, costUSD float64, at time.Time) error {
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO jev_usage (feature, input_tokens, output_tokens, cost_usd, created_at)
VALUES ($1, $2, $3, $4, $5)`, feature, u.InputTokens, u.OutputTokens, costUSD, at.UTC()); err != nil {
		return fmt.Errorf("postgres: record jev usage: %w", err)
	}
	return nil
}
