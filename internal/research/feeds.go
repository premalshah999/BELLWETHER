package research

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// FeedScraper queries a cached copy of an official feed independently of the
// ingestion clock. This gives research direct links when web search is down.
type FeedScraper struct {
	Source news.Source
	Client *http.Client
	mu     sync.Mutex
	items  []Finding
	until  time.Time
}

func (f *FeedScraper) Name() string { return f.Source.ID }
func (f *FeedScraper) Trust() int   { return f.Source.Trust }

func (f *FeedScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Now().After(f.until) {
		client := f.Client
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Source.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", researchAgent)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", f.Name(), resp.StatusCode)
		}
		if len(body) > maxBody {
			return nil, fmt.Errorf("%s: feed exceeds size limit", f.Name())
		}
		if err != nil {
			return nil, err
		}
		items, _, err := news.ParseFeedItems(body)
		if err != nil {
			return nil, err
		}
		f.items = nil
		for _, item := range items {
			if item.URL == "" || item.Title == "" {
				continue
			}
			f.items = append(f.items, Finding{Title: item.Title, URL: item.URL, Publisher: f.Source.Name,
				Snippet: truncate(stripTags(item.Description), 600), PublishedAt: item.Published,
				Scraper: f.Name(), Trust: f.Trust()})
		}
		f.until = time.Now().Add(2 * time.Minute)
	}
	terms := QueryTerms(query)
	var out []Finding
	for _, item := range f.items {
		if termHits(item.Title+" "+item.Snippet, terms) == 0 {
			continue
		}
		out = append(out, item)
	}
	rankFindings(strings.Join(terms, " "), out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
