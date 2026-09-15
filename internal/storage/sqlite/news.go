package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
)

// SaveArticles upserts articles, returning how many were new.
//
// Uniqueness is (symbol, url). Re-polling a feed hourly must converge on the
// same rows rather than accumulating duplicates, and an article already scored
// must keep its score — re-running the digest on unchanged text would be
// spending tokens to learn nothing.
func (s *DB) SaveArticles(ctx context.Context, articles []news.Article) (int, error) {
	if len(articles) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlite: begin save articles: %w", err)
	}
	defer tx.Rollback()

	// Ask which URLs we already hold before inserting. SQLite reports one
	// changed row for an insert and an update alike, so counting new articles
	// any other way means guessing.
	existing, err := existingURLs(ctx, tx, articles)
	if err != nil {
		return 0, err
	}

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO articles (symbol, title, url, source, published_at, fetched_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (symbol, url) DO UPDATE SET
    title = excluded.title,
    source = excluded.source,
    published_at = COALESCE(excluded.published_at, articles.published_at)`)
	if err != nil {
		return 0, fmt.Errorf("sqlite: prepare save articles: %w", err)
	}
	defer stmt.Close()

	added := 0
	for _, a := range articles {
		var published *int64
		if !a.PublishedAt.IsZero() {
			v := a.PublishedAt.UTC().Unix()
			published = &v
		}
		if _, err := stmt.ExecContext(ctx,
			a.Symbol, a.Title, a.URL, a.Source, published, a.FetchedAt.UTC().Unix()); err != nil {
			return added, fmt.Errorf("sqlite: save article %q: %w", a.URL, err)
		}
		if !existing[a.Symbol+"|"+a.URL] {
			added++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sqlite: commit save articles: %w", err)
	}
	return added, nil
}

// existingURLs reports which (symbol, url) pairs are already stored.
func existingURLs(ctx context.Context, tx *sql.Tx, articles []news.Article) (map[string]bool, error) {
	out := make(map[string]bool, len(articles))
	stmt, err := tx.PrepareContext(ctx, `SELECT 1 FROM articles WHERE symbol = ? AND url = ?`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: prepare article lookup: %w", err)
	}
	defer stmt.Close()

	for _, a := range articles {
		var one int
		err := stmt.QueryRowContext(ctx, a.Symbol, a.URL).Scan(&one)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Not stored yet.
		case err != nil:
			return nil, fmt.Errorf("sqlite: look up article %q: %w", a.URL, err)
		default:
			out[a.Symbol+"|"+a.URL] = true
		}
	}
	return out, nil
}

const articleColumns = `id, symbol, title, url, source, published_at, fetched_at,
                        relevance, sentiment, one_line, scored_at`

func scanArticle(r rowScanner) (news.Article, error) {
	var (
		a         news.Article
		published *int64
		fetchedAt int64
		scoredAt  *int64
	)
	err := r.Scan(&a.ID, &a.Symbol, &a.Title, &a.URL, &a.Source, &published, &fetchedAt,
		&a.Relevance, &a.Sentiment, &a.OneLine, &scoredAt)
	if err != nil {
		return news.Article{}, err
	}
	if published != nil {
		a.PublishedAt = time.Unix(*published, 0).UTC()
	}
	a.FetchedAt = time.Unix(fetchedAt, 0).UTC()
	if scoredAt != nil {
		t := time.Unix(*scoredAt, 0).UTC()
		a.ScoredAt = &t
	}
	return a, nil
}

// ListArticles returns recent articles for a symbol.
//
// Ordering puts scored, relevant articles first, then falls back to recency —
// so a prompt gets the most useful context rather than merely the newest.
func (s *DB) ListArticles(ctx context.Context, symbol string, limit int) ([]news.Article, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+articleColumns+`
FROM articles
WHERE symbol = ?
ORDER BY COALESCE(relevance, 0) DESC, COALESCE(published_at, fetched_at) DESC
LIMIT ?`, symbol, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list articles: %w", err)
	}
	defer rows.Close()

	var out []news.Article
	for rows.Next() {
		a, err := scanArticle(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan article: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListUnscored returns articles awaiting an AI score, oldest first so nothing
// starves at the back of the queue.
func (s *DB) ListUnscored(ctx context.Context, limit int) ([]news.Article, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+articleColumns+`
FROM articles WHERE scored_at IS NULL
ORDER BY fetched_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list unscored articles: %w", err)
	}
	defer rows.Close()

	var out []news.Article
	for rows.Next() {
		a, err := scanArticle(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan unscored article: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveScores records AI assessments.
func (s *DB) SaveScores(ctx context.Context, scores []news.Score) error {
	if len(scores) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin save scores: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
UPDATE articles SET relevance = ?, sentiment = ?, one_line = ?, score_model = ?, scored_at = ?
WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare save scores: %w", err)
	}
	defer stmt.Close()

	for _, sc := range scores {
		if _, err := stmt.ExecContext(ctx,
			sc.Relevance, sc.Sentiment, sc.OneLine, sc.Model,
			sc.ScoredAt.UTC().Unix(), sc.ArticleID); err != nil {
			return fmt.Errorf("sqlite: save score for article %d: %w", sc.ArticleID, err)
		}
	}
	return tx.Commit()
}

// PruneArticles deletes articles older than the cutoff.
func (s *DB) PruneArticles(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM articles WHERE COALESCE(published_at, fetched_at) < ?`, before.UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("sqlite: prune articles: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// WatchedSymbols returns the watchlist as parsed symbols, for the news poller
// and the morning brief.
func (s *DB) WatchedSymbols(ctx context.Context) ([]marketdata.Symbol, error) {
	entries, err := s.ListWatchlist(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]marketdata.Symbol, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Symbol)
	}
	return out, nil
}

// RecentAlertSummaries returns alerts that fired since a moment, for the
// morning brief. It returns the ai package's plain struct so that package need
// not depend on the alert pipeline.
func (s *DB) RecentAlertSummaries(ctx context.Context, since time.Time, limit int) ([]ai.RecentAlert, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT algorithm_name, symbol, summary
FROM alerts WHERE fired_at >= ?
ORDER BY fired_at DESC LIMIT ?`, since.UTC().Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recent alert summaries: %w", err)
	}
	defer rows.Close()

	var out []ai.RecentAlert
	for rows.Next() {
		var a ai.RecentAlert
		if err := rows.Scan(&a.AlgorithmName, &a.Symbol, &a.Summary); err != nil {
			return nil, fmt.Errorf("sqlite: scan alert summary: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
