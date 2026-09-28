// Package news polls RSS feeds, normalises what it finds, and hands the
// articles to the AI digest scorer.
package news

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// Article is one normalised news item.
type Article struct {
	ID int64 `json:"id"`
	// Symbol is the watchlist symbol this article was collected for. The same
	// story reached through two symbols' feeds is stored once per symbol,
	// because relevance is per-symbol and so is the score.
	Symbol      string    `json:"symbol"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Source      string    `json:"source"`
	PublishedAt time.Time `json:"published_at"`
	FetchedAt   time.Time `json:"fetched_at"`

	// Scored fields, filled by the AI digest. Relevance and Sentiment are
	// pointers so "not yet scored" is distinguishable from "scored zero".
	Relevance *float64   `json:"relevance,omitempty"`
	Sentiment *float64   `json:"sentiment,omitempty"`
	OneLine   string     `json:"one_line,omitempty"`
	ScoredAt  *time.Time `json:"scored_at,omitempty"`
}

// Scored reports whether the AI digest has evaluated this article.
func (a Article) Scored() bool { return a.ScoredAt != nil }

// Age renders how long ago the article was published, for prompts and the UI.
func (a Article) Age(now time.Time) string {
	if a.PublishedAt.IsZero() {
		return "undated"
	}
	d := now.Sub(a.PublishedAt)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h ago"
	default:
		return itoa(int(d.Hours()/24)) + "d ago"
	}
}

// Score is one article's AI assessment.
type Score struct {
	ArticleID int64
	Relevance float64
	Sentiment float64
	OneLine   string
	Model     string
	ScoredAt  time.Time
}

// Store persists articles and their scores. Declared at the point of use so
// this package does not depend on the storage package.
type Store interface {
	// SaveArticles upserts by URL within a symbol, returning how many were
	// new. Re-polling a feed must converge rather than duplicate.
	SaveArticles(ctx context.Context, articles []Article) (added int, err error)
	// ListArticles returns recent articles for a symbol, newest first.
	ListArticles(ctx context.Context, symbol string, limit int) ([]Article, error)
	// ListUnscored returns articles awaiting an AI score.
	ListUnscored(ctx context.Context, limit int) ([]Article, error)
	// SaveScores records AI assessments.
	SaveScores(ctx context.Context, scores []Score) error
	// PruneArticles deletes articles older than the cutoff.
	PruneArticles(ctx context.Context, before time.Time) (int, error)
}

// GoogleNewsFeed builds a Google News RSS query URL for a symbol.
//
// Google News is the seed source because it needs no key and covers both
// Indian and US press. The query includes the company name when known, since
// a bare ticker like "TCS" collides with unrelated acronyms.
func GoogleNewsFeed(symbol, company string, indian bool) string {
	ticker := symbol
	if i := strings.IndexByte(ticker, '.'); i > 0 {
		ticker = ticker[:i]
	}

	query := ticker + " stock"
	if company != "" {
		query = "\"" + company + "\""
	}

	params := url.Values{
		"q":    {query},
		"hl":   {"en-US"},
		"gl":   {"US"},
		"ceid": {"IN:en"},
	}
	if !indian {
		params.Set("hl", "en-US")
		params.Set("gl", "US")
		params.Set("ceid", "US:en")
	}
	return "https://news.google.com/rss/search?" + params.Encode()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
