package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/storage"
)

// SaveArticles upserts per-symbol articles and returns how many were new.
// Uniqueness is (symbol, url), because relevance is judged per symbol; an
// article already scored keeps its score.
func (d *DB) SaveArticles(ctx context.Context, articles []news.Article) (int, error) {
	if len(articles) == 0 {
		return 0, nil
	}
	n := len(articles)
	symbols := make([]string, 0, n)
	titles := make([]string, 0, n)
	urls := make([]string, 0, n)
	sources := make([]string, 0, n)
	published := make([]sql.NullTime, 0, n)
	fetched := make([]time.Time, 0, n)

	for _, a := range articles {
		if a.Symbol == "" || a.URL == "" {
			continue
		}
		symbols = append(symbols, a.Symbol)
		titles = append(titles, a.Title)
		urls = append(urls, a.URL)
		sources = append(sources, a.Source)
		published = append(published, sql.NullTime{Time: a.PublishedAt.UTC(), Valid: !a.PublishedAt.IsZero()})
		at := a.FetchedAt
		if at.IsZero() {
			at = time.Now()
		}
		fetched = append(fetched, at.UTC())
	}
	if len(symbols) == 0 {
		return 0, nil
	}

	var added int
	err := d.db.QueryRowContext(ctx, `
WITH inserted AS (
    INSERT INTO news_articles (symbol, title, url, source, published_at, fetched_at)
    SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[],
                         $5::timestamptz[], $6::timestamptz[])
    ON CONFLICT (symbol, url) DO NOTHING
    RETURNING 1
)
SELECT count(*) FROM inserted`,
		symbols, titles, urls, sources, published, fetched).Scan(&added)
	if err != nil {
		return 0, fmt.Errorf("postgres: save articles: %w", err)
	}
	return added, nil
}

const articleColumns = `id, symbol, title, url, source, published_at, fetched_at,
       relevance, sentiment, one_line, scored_at`

func scanArticles(rows *sql.Rows) ([]news.Article, error) {
	var out []news.Article
	for rows.Next() {
		var (
			a                    news.Article
			published, scored    sql.NullTime
			relevance, sentiment sql.NullFloat64
		)
		if err := rows.Scan(&a.ID, &a.Symbol, &a.Title, &a.URL, &a.Source,
			&published, &a.FetchedAt, &relevance, &sentiment, &a.OneLine, &scored); err != nil {
			return nil, fmt.Errorf("postgres: scan article: %w", err)
		}
		a.PublishedAt = timeOrZero(published)
		a.FetchedAt = a.FetchedAt.UTC()
		if relevance.Valid {
			v := relevance.Float64
			a.Relevance = &v
		}
		if sentiment.Valid {
			v := sentiment.Float64
			a.Sentiment = &v
		}
		if scored.Valid {
			t := scored.Time.UTC()
			a.ScoredAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListArticles returns recent articles for a symbol, newest first.
func (d *DB) ListArticles(ctx context.Context, symbol string, limit int) ([]news.Article, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT `+articleColumns+` FROM news_articles
WHERE symbol = $1
ORDER BY published_at DESC NULLS LAST, fetched_at DESC
LIMIT $2`, symbol, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list articles: %w", err)
	}
	defer rows.Close()
	return scanArticles(rows)
}

// ListUnscored returns articles awaiting an AI score.
func (d *DB) ListUnscored(ctx context.Context, limit int) ([]news.Article, error) {
	if limit <= 0 || limit > 500 {
		limit = 40
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT `+articleColumns+` FROM news_articles
WHERE scored_at IS NULL
ORDER BY fetched_at DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list unscored: %w", err)
	}
	defer rows.Close()
	return scanArticles(rows)
}

// SaveScores records AI assessments.
func (d *DB) SaveScores(ctx context.Context, scores []news.Score) error {
	if len(scores) == 0 {
		return nil
	}
	n := len(scores)
	ids := make([]int64, 0, n)
	relevance := make([]float64, 0, n)
	sentiment := make([]float64, 0, n)
	oneLines := make([]string, 0, n)
	models := make([]string, 0, n)
	scoredAt := make([]time.Time, 0, n)

	for _, s := range scores {
		at := s.ScoredAt
		if at.IsZero() {
			at = time.Now()
		}
		ids = append(ids, s.ArticleID)
		relevance = append(relevance, s.Relevance)
		sentiment = append(sentiment, s.Sentiment)
		oneLines = append(oneLines, s.OneLine)
		models = append(models, s.Model)
		scoredAt = append(scoredAt, at.UTC())
	}

	_, err := d.db.ExecContext(ctx, `
UPDATE news_articles a SET
    relevance   = s.relevance,
    sentiment   = s.sentiment,
    one_line    = s.one_line,
    score_model = s.model,
    scored_at   = s.scored_at
FROM unnest($1::bigint[], $2::real[], $3::real[], $4::text[], $5::text[], $6::timestamptz[])
     AS s(id, relevance, sentiment, one_line, model, scored_at)
WHERE a.id = s.id`,
		ids, relevance, sentiment, oneLines, models, scoredAt)
	if err != nil {
		return fmt.Errorf("postgres: save scores: %w", err)
	}
	return nil
}

// PruneArticles deletes articles older than the cutoff.
func (d *DB) PruneArticles(ctx context.Context, before time.Time) (int, error) {
	res, err := d.db.ExecContext(ctx,
		`DELETE FROM news_articles WHERE fetched_at < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("postgres: prune articles: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Stats gathers storage counters for the health page.
func (d *DB) Stats(ctx context.Context) (storage.Stats, error) {
	var st storage.Stats
	// One round trip rather than eight. The counts are approximate the moment
	// they are read anyway, so there is no value in reading them separately.
	err := d.db.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM raw_items),
       (SELECT count(*) FROM raw_items WHERE processed_at IS NULL),
       (SELECT count(*) FROM events),
       (SELECT count(*) FROM events WHERE classified_at IS NOT NULL),
       (SELECT count(*) FROM event_evidence),
       (SELECT count(*) FROM event_entities),
       pg_database_size(current_database())`).Scan(
		&st.RawItems, &st.PendingItems, &st.Events, &st.Classified,
		&st.Evidence, &st.Entities, &st.SizeBytes)
	if err != nil {
		return st, fmt.Errorf("postgres: stats: %w", err)
	}
	return st, nil
}

// Compact reclaims space and refreshes planner statistics.
//
// VACUUM runs continuously in the background here, unlike SQLite where it had
// to be scheduled and took an exclusive lock for its duration. What remains
// useful is ANALYZE after a large prune, so the planner is not working from
// row counts taken before it.
func (d *DB) Compact(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `ANALYZE`); err != nil {
		return fmt.Errorf("postgres: analyze: %w", err)
	}
	return nil
}
