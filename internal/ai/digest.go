package ai

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// digestBatchSize is how many articles are scored per call. Batching keeps the
// per-article overhead down; keeping the batch small keeps one bad response
// from wasting a large prompt.
const digestBatchSize = 8

type digestPayload struct {
	Scores []struct {
		ID        string  `json:"id"`
		Relevance float64 `json:"relevance"`
		Sentiment float64 `json:"sentiment"`
		OneLine   string  `json:"one_line"`
	} `json:"scores"`
}

// DigestSummary reports what one scoring pass did.
type DigestSummary struct {
	Considered int           `json:"considered"`
	Scored     int           `json:"scored"`
	Batches    int           `json:"batches"`
	Status     Status        `json:"status"`
	Duration   time.Duration `json:"duration_ns"`
}

// ScoreStore is the subset of the news store the digest writes back through.
type ScoreStore interface {
	ListUnscored(ctx context.Context, limit int) ([]news.Article, error)
	SaveScores(ctx context.Context, scores []news.Score) error
}

// WithScoreStore attaches the store the digest writes scores to.
func WithScoreStore(st ScoreStore) ServiceOption {
	return func(s *Service) { s.scores = st }
}

// RunNewsDigest scores unscored articles for relevance and sentiment.
//
// It runs on the cheap model tier: this is the highest-volume AI feature by
// far, and spending the good model on headline triage would exhaust the
// monthly budget in days.
func (s *Service) RunNewsDigest(ctx context.Context, maxArticles int) (DigestSummary, error) {
	start := s.now()
	summary := DigestSummary{}

	status, err := s.guard(ctx)
	summary.Status = status
	if err != nil {
		return summary, err
	}
	if s.scores == nil {
		return summary, fmt.Errorf("ai: no score store configured")
	}
	if maxArticles <= 0 {
		maxArticles = 40
	}

	pending, err := s.scores.ListUnscored(ctx, maxArticles)
	if err != nil {
		return summary, fmt.Errorf("ai: list unscored articles: %w", err)
	}
	summary.Considered = len(pending)
	if len(pending) == 0 {
		return summary, nil
	}

	// Group by symbol: relevance is a question about a specific company, so
	// scoring a mixed batch would force the model to switch subject per row.
	bySymbol := map[string][]news.Article{}
	for _, a := range pending {
		bySymbol[a.Symbol] = append(bySymbol[a.Symbol], a)
	}

	for symbol, articles := range bySymbol {
		for offset := 0; offset < len(articles); offset += digestBatchSize {
			end := offset + digestBatchSize
			if end > len(articles) {
				end = len(articles)
			}
			batch := articles[offset:end]

			scored, err := s.scoreBatch(ctx, symbol, batch)
			summary.Batches++
			if err != nil {
				// Budget exhaustion mid-pass stops the whole run; anything
				// else is per-batch and the next batch may still work.
				if statusFor(err) == StatusBudgetReached {
					// The budget is spent; further batches would all fail.
					summary.Status = StatusBudgetReached
					summary.Duration = s.now().Sub(start)
					return summary, nil
				}
				s.log.Warn("news digest batch failed", "symbol", symbol, "err", err)
				continue
			}
			if err := s.scores.SaveScores(ctx, scored); err != nil {
				s.log.Error("could not store news scores", "symbol", symbol, "err", err)
				continue
			}
			summary.Scored += len(scored)
		}
	}

	summary.Duration = s.now().Sub(start)
	if summary.Scored > 0 {
		s.log.Info("news digest complete",
			"considered", summary.Considered, "scored", summary.Scored, "batches", summary.Batches)
	}
	return summary, nil
}

func (s *Service) scoreBatch(ctx context.Context, symbol string, batch []news.Article) ([]news.Score, error) {
	type promptArticle struct {
		ID     string
		Title  string
		Source string
		Age    string
	}

	now := s.now()
	items := make([]promptArticle, 0, len(batch))
	byID := make(map[string]news.Article, len(batch))
	for _, a := range batch {
		id := strconv.FormatInt(a.ID, 10)
		items = append(items, promptArticle{
			ID: id, Title: a.Title, Source: a.Source, Age: a.Age(now),
		})
		byID[id] = a
	}

	prompt, err := UserPrompt(PromptNewsDigest, map[string]any{
		"Symbol":   symbol,
		"Company":  "",
		"Articles": items,
	})
	if err != nil {
		return nil, err
	}

	var payload digestPayload
	resp, err := s.client.CompleteJSON(ctx, Request{
		Feature: FeatureNewsDigest,
		// The cheap tier: this is triage, not analysis.
		Cheap:       true,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.1,
		MaxTokens:   3000,
	}, &payload)
	if err != nil {
		return nil, err
	}

	out := make([]news.Score, 0, len(payload.Scores))
	for _, sc := range payload.Scores {
		article, ok := byID[sc.ID]
		if !ok {
			// The model invented or mangled an id. Dropping it is right:
			// attaching a score to the wrong article would be worse than
			// leaving that article unscored.
			s.log.Debug("digest returned an unknown article id", "id", sc.ID, "symbol", symbol)
			continue
		}
		out = append(out, news.Score{
			ArticleID: article.ID,
			Relevance: clamp(sc.Relevance, 0, 1),
			Sentiment: clamp(sc.Sentiment, -1, 1),
			OneLine:   truncateRunes(sc.OneLine, 160),
			Model:     resp.Model,
			ScoredAt:  now,
		})
	}
	return out, nil
}

func clamp(v, lo, hi float64) float64 {
	if v != v { // NaN
		return 0
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
