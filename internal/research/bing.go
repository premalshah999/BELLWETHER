package research

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tradesys/dashboard/internal/news"
)

// BingNewsScraper searches Bing's news index through its RSS output. It is a
// second route to a major news index that does not depend on the SearXNG
// node, and unlike Google News it yields the publisher's own URL, which can
// be read and not only linked.
type BingNewsScraper struct {
	Client *http.Client
}

func (b *BingNewsScraper) Name() string { return "bing_news" }
func (b *BingNewsScraper) Trust() int   { return news.TrustSpecialist }

func (b *BingNewsScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	client := b.Client
	if client == nil {
		client = http.DefaultClient
	}
	params := url.Values{"q": {query}, "format": {"rss"}, "setlang": {"en-US"}, "cc": {"US"}}
	body, err := httpGet(ctx, client, "https://www.bing.com/news/search?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("bing news: %w", err)
	}
	items, _, err := news.ParseFeedItems(body)
	if err != nil {
		return nil, fmt.Errorf("bing news: %w", err)
	}
	out := make([]Finding, 0, min(len(items), limit))
	for _, it := range items {
		if len(out) >= limit {
			break
		}
		link := bingTarget(it.URL)
		out = append(out, Finding{
			Title: it.Title, URL: link, Publisher: hostOf(link),
			Snippet: truncate(stripTags(it.Description), 400), PublishedAt: it.Published,
			Scraper: b.Name(), Trust: b.Trust(),
		})
	}
	return out, nil
}

// bingTarget unwraps Bing's click-tracking link to the article it points at.
func bingTarget(link string) string {
	if u, err := url.Parse(link); err == nil && strings.Contains(u.Path, "apiclick") {
		if target := u.Query().Get("url"); strings.HasPrefix(target, "http") {
			return target
		}
	}
	return link
}
