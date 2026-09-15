package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/tradesys/dashboard/internal/storage/postgres"
	"github.com/tradesys/dashboard/internal/storage/sqlite"
)

// runDataMigration copies an existing SQLite database into Postgres.
//
// Only what cannot be recomputed is copied. Events, their entities, facts,
// sectors and evidence links are all *derived* from raw_items, so they are
// deliberately left behind and rebuilt by the reprocess pass afterwards. That
// halves the work, and it exercises the same path a parser fix takes — which
// is a better guarantee that the derivation still works than copying the old
// results across would have been.
//
// Everything here is idempotent: running it twice inserts nothing the second
// time, so a partial run can simply be repeated.
func runDataMigration(ctx context.Context, log *slog.Logger, sqlitePath, databaseURL string) error {
	src, err := sqlite.Open(sqlitePath)
	if err != nil {
		return fmt.Errorf("open sqlite source %s: %w", sqlitePath, err)
	}
	defer src.Close()

	dst, err := postgres.Open(ctx, databaseURL, postgres.WithLogger(log))
	if err != nil {
		return fmt.Errorf("open postgres target: %w", err)
	}
	defer dst.Close()

	from, to := src.SQL(), dst.SQL()

	type table struct {
		name string
		// read pulls rows out of SQLite; write puts one row into Postgres.
		// Times cross as Unix seconds in SQLite and as timestamptz in
		// Postgres, so each pair does its own conversion rather than sharing
		// a generic one that would have to guess which columns are times.
		migrate func(context.Context, *sql.DB, *sql.DB) (int, error)
	}

	tables := []table{
		{"watchlist", migrateWatchlist},
		{"candles", migrateCandles},
		{"series_coverage", migrateSeriesCoverage},
		{"quotes", migrateQuotes},
		{"provider_budget", migrateProviderBudget},
		{"provider_health", migrateProviderHealth},
		{"algorithms", migrateAlgorithms},
		{"algorithm_symbol_state", migrateAlgorithmState},
		{"alerts", migrateAlerts},
		{"llm_usage", migrateLLMUsage},
		{"ai_outputs", migrateAIOutputs},
		{"outlooks", migrateOutlooks},
		{"news_articles", migrateArticles},
		{"source_health", migrateSourceHealth},
		{"raw_items", migrateRawItems},
	}

	start := time.Now()
	for _, t := range tables {
		n, err := t.migrate(ctx, from, to)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", t.name, err)
		}
		log.Info("migrated", "table", t.name, "rows", n)
	}
	log.Info("data migration complete", "took", time.Since(start).Round(time.Millisecond))
	log.Info("events were not copied: run with -reprocess to rebuild them from raw_items")
	return nil
}

// unix converts a SQLite epoch-seconds column to a time, treating zero and
// NULL alike as unknown. Storing the epoch for an unknown time would make an
// undated row sort as the oldest thing in the database.
func unix(v sql.NullInt64) any {
	if !v.Valid || v.Int64 == 0 {
		return nil
	}
	return time.Unix(v.Int64, 0).UTC()
}

func unixRequired(v int64) time.Time { return time.Unix(v, 0).UTC() }

func migrateWatchlist(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `SELECT symbol, note, position, added_at FROM watchlist`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			symbol, note string
			position     int
			addedAt      int64
		)
		if err := rows.Scan(&symbol, &note, &position, &addedAt); err != nil {
			return n, err
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO watchlist (symbol, note, position, added_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (symbol) DO NOTHING`, symbol, note, position, unixRequired(addedAt)); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func migrateCandles(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT symbol, interval, ts, open, high, low, close, volume, source, resolved_symbol, fetched_at
FROM candles`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	tx, err := to.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO candles (symbol, interval, ts, open, high, low, close, volume, source, resolved_symbol, fetched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (symbol, interval, ts) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	n, skipped := 0, 0
	for rows.Next() {
		var (
			symbol, interval, source, resolved string
			ts, fetchedAt                      int64
			o, h, l, c, v                      float64
		)
		if err := rows.Scan(&symbol, &interval, &ts, &o, &h, &l, &c, &v, &source, &resolved, &fetchedAt); err != nil {
			return n, err
		}
		// The Postgres schema rejects an incoherent bar. A handful of these
		// exist in the old data from a provider that briefly emitted them,
		// and they are dropped rather than allowed to fail the migration —
		// they would have drawn a wrong chart either way.
		if h < l || h < o || h < c || l > o || l > c || v < 0 {
			skipped++
			continue
		}
		if _, err := stmt.ExecContext(ctx, symbol, interval, unixRequired(ts),
			o, h, l, c, v, source, resolved, unixRequired(fetchedAt)); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if skipped > 0 {
		slog.Default().Warn("dropped incoherent candles during migration", "count", skipped)
	}
	return n, tx.Commit()
}

func migrateSeriesCoverage(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `SELECT symbol, interval, requested_limit, updated_at FROM series_coverage`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			symbol, interval string
			limit            int
			updatedAt        int64
		)
		if err := rows.Scan(&symbol, &interval, &limit, &updatedAt); err != nil {
			return n, err
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO series_coverage (symbol, interval, requested_limit, updated_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (symbol, interval) DO NOTHING`, symbol, interval, limit, unixRequired(updatedAt)); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func migrateQuotes(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT symbol, price, prev_close, change, change_percent, day_high, day_low,
       volume, currency, as_of, source, fetched_at FROM quotes`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			symbol, currency, source              string
			price, prev, chg, chgPct, hi, lo, vol float64
			asOf, fetchedAt                       int64
		)
		if err := rows.Scan(&symbol, &price, &prev, &chg, &chgPct, &hi, &lo,
			&vol, &currency, &asOf, &source, &fetchedAt); err != nil {
			return n, err
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO quotes (symbol, price, prev_close, change, change_percent, day_high,
                    day_low, volume, currency, as_of, source, fetched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (symbol) DO NOTHING`,
			symbol, price, prev, chg, chgPct, hi, lo, vol, currency,
			unix(sql.NullInt64{Int64: asOf, Valid: true}), source, unixRequired(fetchedAt)); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func migrateProviderBudget(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `SELECT provider, period, used FROM provider_budget`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			provider, period string
			used             int
		)
		if err := rows.Scan(&provider, &period, &used); err != nil {
			return n, err
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO provider_budget (provider, period, used) VALUES ($1, $2, $3)
ON CONFLICT (provider, period) DO NOTHING`, provider, period, used); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func migrateProviderHealth(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT provider, kind, status, message, last_ok_at, last_error_at, updated_at FROM provider_health`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			provider, kind, status, message string
			okAt, errAt                     sql.NullInt64
			updatedAt                       int64
		)
		if err := rows.Scan(&provider, &kind, &status, &message, &okAt, &errAt, &updatedAt); err != nil {
			return n, err
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO provider_health (provider, kind, status, message, last_ok_at, last_error_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (provider) DO NOTHING`,
			provider, kind, status, message, unix(okAt), unix(errAt), unixRequired(updatedAt)); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func migrateAlgorithms(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT id, name, definition, enabled, interval, created_at, updated_at FROM algorithms`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id                   int64
			name, definition     string
			enabled              int
			interval             string
			createdAt, updatedAt int64
		)
		if err := rows.Scan(&id, &name, &definition, &enabled, &interval, &createdAt, &updatedAt); err != nil {
			return n, err
		}
		// The identity column is overridden so alerts keep pointing at the
		// right algorithm; the sequence is resynchronised afterwards.
		if _, err := to.ExecContext(ctx, `
INSERT INTO algorithms (id, name, definition, enabled, interval, created_at, updated_at)
OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7)
ON CONFLICT (id) DO NOTHING`,
			id, name, definition, enabled != 0, interval,
			unixRequired(createdAt), unixRequired(updatedAt)); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, resyncSequence(ctx, to, "algorithms")
}

func migrateAlgorithmState(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT algorithm_id, symbol, last_fired_at, last_evaluated_at, last_status, last_reason, last_summary
FROM algorithm_symbol_state`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			algorithmID                    int64
			symbol, status, reason, summry string
			firedAt, evaluatedAt           sql.NullInt64
		)
		if err := rows.Scan(&algorithmID, &symbol, &firedAt, &evaluatedAt, &status, &reason, &summry); err != nil {
			return n, err
		}
		// Skipped when the algorithm did not survive: the foreign key is real
		// now, and orphaned state has no meaning anyway.
		if _, err := to.ExecContext(ctx, `
INSERT INTO algorithm_symbol_state
    (algorithm_id, symbol, last_fired_at, last_evaluated_at, last_status, last_reason, last_summary)
SELECT $1, $2, $3, $4, $5, $6, $7
WHERE EXISTS (SELECT 1 FROM algorithms WHERE id = $1)
ON CONFLICT (algorithm_id, symbol) DO NOTHING`,
			algorithmID, symbol, unix(firedAt), unix(evaluatedAt), status, reason, summry); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func migrateAlerts(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT id, algorithm_id, algorithm_name, symbol, interval, fired_at, bar_time,
       price, summary, conditions, ai_context, ai_status, delivery, read_at FROM alerts`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id, algorithmID                        int64
			name, symbol, interval, summary        string
			conditions, aiContext, aiStatus, deliv string
			firedAt, barTime                       int64
			price                                  float64
			readAt                                 sql.NullInt64
		)
		if err := rows.Scan(&id, &algorithmID, &name, &symbol, &interval, &firedAt, &barTime,
			&price, &summary, &conditions, &aiContext, &aiStatus, &deliv, &readAt); err != nil {
			return n, err
		}
		if conditions == "" {
			conditions = "[]"
		}
		if deliv == "" {
			deliv = "[]"
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO alerts (id, algorithm_id, algorithm_name, symbol, interval, fired_at,
                    bar_time, price, summary, conditions, ai_context, ai_status, delivery, read_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, $13::jsonb, $14)
ON CONFLICT (id) DO NOTHING`,
			id, algorithmID, name, symbol, interval, unixRequired(firedAt),
			unix(sql.NullInt64{Int64: barTime, Valid: barTime != 0}), price, summary,
			conditions, aiContext, aiStatus, deliv, unix(readAt)); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, resyncSequence(ctx, to, "alerts")
}

func migrateLLMUsage(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT period, feature, model, prompt_tokens, completion_tokens, total_tokens, created_at FROM llm_usage`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			period, feature, model  string
			prompt, completion, tot int
			createdAt               int64
		)
		if err := rows.Scan(&period, &feature, &model, &prompt, &completion, &tot, &createdAt); err != nil {
			return n, err
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO llm_usage (period, feature, model, prompt_tokens, completion_tokens, total_tokens, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			period, feature, model, prompt, completion, tot, unixRequired(createdAt)); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func migrateAIOutputs(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT id, kind, symbol, created_at, model, tokens, content FROM ai_outputs`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n, skipped := 0, 0
	for rows.Next() {
		var (
			id                           int64
			kind, symbol, model, content string
			createdAt                    int64
			tokens                       int
		)
		if err := rows.Scan(&id, &kind, &symbol, &createdAt, &model, &tokens, &content); err != nil {
			return n, err
		}
		// The column is JSONB now. A payload that is not valid JSON was
		// unreadable by the archive page anyway, so it is dropped rather than
		// allowed to fail the migration.
		if !json.Valid([]byte(content)) {
			skipped++
			continue
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO ai_outputs (id, kind, symbol, created_at, model, tokens, content)
OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
ON CONFLICT (id) DO NOTHING`,
			id, kind, symbol, unixRequired(createdAt), model, tokens, content); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if skipped > 0 {
		slog.Default().Warn("dropped AI outputs that were not valid JSON", "count", skipped)
	}
	return n, resyncSequence(ctx, to, "ai_outputs")
}

func migrateOutlooks(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT id, symbol, created_at, horizon_days, price_at_creation,
       base_probability, base_move, base_reasoning,
       bull_probability, bull_move, bull_reasoning,
       bear_probability, bear_move, bear_reasoning,
       key_risk, model, resolved_at, realized_price, realized_move,
       actual_scenario, brier_score FROM outlooks`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n, skipped := 0, 0
	for rows.Next() {
		var (
			id                         int64
			symbol                     string
			createdAt                  int64
			horizon                    int
			price                      float64
			bp, bm                     float64
			br                         string
			up, um                     float64
			ur                         string
			dp, dm                     float64
			dr                         string
			keyRisk, model, actual     string
			resolvedAt                 sql.NullInt64
			realPrice, realMove, brier sql.NullFloat64
		)
		if err := rows.Scan(&id, &symbol, &createdAt, &horizon, &price,
			&bp, &bm, &br, &up, &um, &ur, &dp, &dm, &dr,
			&keyRisk, &model, &resolvedAt, &realPrice, &realMove, &actual, &brier); err != nil {
			return n, err
		}
		// The new schema requires the three probabilities to sum to one,
		// which is what makes a Brier score meaningful. Anything that fails
		// that could never have been scored correctly.
		if sum := bp + up + dp; sum < 0.98 || sum > 1.02 {
			skipped++
			continue
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO outlooks (id, symbol, created_at, horizon_days, price_at_creation,
    base_probability, base_move, base_reasoning,
    bull_probability, bull_move, bull_reasoning,
    bear_probability, bear_move, bear_reasoning,
    key_risk, model, resolved_at, realized_price, realized_move, actual_scenario, brier_score)
OVERRIDING SYSTEM VALUE
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
ON CONFLICT (id) DO NOTHING`,
			id, symbol, unixRequired(createdAt), horizon, price,
			bp, bm, br, up, um, ur, dp, dm, dr,
			keyRisk, model, unix(resolvedAt),
			nullFloat64(realPrice), nullFloat64(realMove), actual, nullFloat64(brier)); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if skipped > 0 {
		slog.Default().Warn("dropped outlooks whose probabilities did not sum to one", "count", skipped)
	}
	return n, resyncSequence(ctx, to, "outlooks")
}

func migrateArticles(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT symbol, title, url, source, published_at, fetched_at,
       relevance, sentiment, one_line, score_model, scored_at FROM articles`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			symbol, title, url, source string
			oneLine, scoreModel        string
			published                  sql.NullInt64
			fetchedAt                  int64
			relevance, sentiment       sql.NullFloat64
			scoredAt                   sql.NullInt64
		)
		if err := rows.Scan(&symbol, &title, &url, &source, &published, &fetchedAt,
			&relevance, &sentiment, &oneLine, &scoreModel, &scoredAt); err != nil {
			return n, err
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO news_articles (symbol, title, url, source, published_at, fetched_at,
                           relevance, sentiment, one_line, score_model, scored_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (symbol, url) DO NOTHING`,
			symbol, title, url, source, unix(published), unixRequired(fetchedAt),
			nullFloat64(relevance), nullFloat64(sentiment), oneLine, scoreModel, unix(scoredAt)); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func migrateSourceHealth(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT source_id, last_attempt_at, last_success_at, last_failure_at, last_error,
       consecutive_failures, total_attempts, total_successes, total_items,
       total_new_items, etag, last_modified FROM source_health`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			sourceID, lastError, etag, lastMod string
			attempt, success, failure          sql.NullInt64
			failures, attempts, successes      int64
			items, newItems                    int64
		)
		if err := rows.Scan(&sourceID, &attempt, &success, &failure, &lastError,
			&failures, &attempts, &successes, &items, &newItems, &etag, &lastMod); err != nil {
			return n, err
		}
		if _, err := to.ExecContext(ctx, `
INSERT INTO source_health (source_id, last_attempt_at, last_success_at, last_failure_at,
    last_error, consecutive_failures, total_attempts, total_successes, total_items,
    total_new_items, etag, last_modified)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (source_id) DO NOTHING`,
			sourceID, unix(attempt), unix(success), unix(failure), lastError,
			failures, attempts, successes, items, newItems, etag, lastMod); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// migrateRawItems copies the evidence archive.
//
// This is the only table whose loss would be permanent, so it is copied last:
// if anything earlier fails, the run can simply be repeated. processed_at is
// deliberately not carried over — every item is reopened so the reprocess pass
// rebuilds the whole event layer against the current parser.
func migrateRawItems(ctx context.Context, from, to *sql.DB) (int, error) {
	rows, err := from.QueryContext(ctx, `
SELECT source_id, content_hash, url, canonical_url, title, description, publisher,
       payload, occurred_at, published_at, discovered_at, fetched_at FROM raw_items`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	tx, err := to.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO raw_items (source_id, content_hash, url, canonical_url, title, description,
                       publisher, payload, occurred_at, published_at, discovered_at, fetched_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (source_id, content_hash) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	n, skipped := 0, 0
	for rows.Next() {
		var (
			sourceID, hash, url, canonical string
			title, description, publisher  string
			payload                        string
			occurred, published            sql.NullInt64
			discovered, fetched            int64
		)
		if err := rows.Scan(&sourceID, &hash, &url, &canonical, &title, &description,
			&publisher, &payload, &occurred, &published, &discovered, &fetched); err != nil {
			return n, err
		}
		// The new schema requires substance. An item with neither a title nor
		// a description could never have become an event.
		if title == "" && description == "" {
			skipped++
			continue
		}
		if _, err := stmt.ExecContext(ctx, sourceID, hash, url, canonical, title,
			description, publisher, payload, unix(occurred), unix(published),
			unixRequired(discovered), unixRequired(fetched)); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if skipped > 0 {
		slog.Default().Warn("dropped raw items with no title or description", "count", skipped)
	}
	return n, tx.Commit()
}

// resyncSequence advances an identity sequence past the largest migrated id.
//
// Rows were inserted with explicit ids, which does not move the sequence. Left
// alone, the next natural insert would collide with a migrated row — and it
// would do so at some arbitrary later moment rather than here.
func resyncSequence(ctx context.Context, to *sql.DB, table string) error {
	_, err := to.ExecContext(ctx, fmt.Sprintf(`
SELECT setval(pg_get_serial_sequence('%s', 'id'),
              GREATEST(COALESCE((SELECT max(id) FROM %s), 0), 1))`, table, table))
	if err != nil {
		return fmt.Errorf("resync %s sequence: %w", table, err)
	}
	return nil
}

func nullFloat64(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}
