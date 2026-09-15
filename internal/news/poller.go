package news

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// maxItemsPerFeed caps what one poll ingests. Google News returns up to a
// hundred items; keeping the most recent handful is enough for context and
// keeps the AI scoring bill bounded.
const maxItemsPerFeed = 12

// maxArticleAge is how far back a freshly polled item may be. Older stories
// are dropped: a six-month-old article surfacing as "news" is worse than no
// news.
const maxArticleAge = 14 * 24 * time.Hour

// retention is how long articles are kept before pruning.
const retention = 30 * 24 * time.Hour

// SymbolSource supplies the symbols to collect news for.
type SymbolSource interface {
	WatchedSymbols(ctx context.Context) ([]marketdata.Symbol, error)
}

// Poller fetches feeds and stores what it finds.
type Poller struct {
	store   Store
	symbols SymbolSource
	http    *http.Client
	log     *slog.Logger
	now     func() time.Time
	// companyNames improves feed queries; a bare ticker collides with
	// unrelated acronyms.
	companyNames map[string]string
	observe      func(ctx context.Context, provider string, ok, skipped bool, err error)
}

// PollerOption configures a Poller.
type PollerOption func(*Poller)

// WithPollerLogger sets the logger.
func WithPollerLogger(l *slog.Logger) PollerOption { return func(p *Poller) { p.log = l } }

// WithPollerClock replaces the time source.
func WithPollerClock(now func() time.Time) PollerOption { return func(p *Poller) { p.now = now } }

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) PollerOption { return func(p *Poller) { p.http = h } }

// WithCompanyNames supplies ticker-to-company mappings for better queries.
func WithCompanyNames(names map[string]string) PollerOption {
	return func(p *Poller) { p.companyNames = names }
}

// WithObserver registers a health reporter.
func WithObserver(f func(ctx context.Context, provider string, ok, skipped bool, err error)) PollerOption {
	return func(p *Poller) { p.observe = f }
}

// NewPoller builds a news poller.
func NewPoller(store Store, symbols SymbolSource, opts ...PollerOption) *Poller {
	p := &Poller{
		store:        store,
		symbols:      symbols,
		http:         &http.Client{Timeout: 20 * time.Second},
		log:          slog.Default(),
		now:          func() time.Time { return time.Now().UTC() },
		companyNames: defaultCompanyNames(),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// defaultCompanyNames seeds a few well-known Indian tickers whose bare symbol
// is ambiguous in a news search.
func defaultCompanyNames() map[string]string {
	return map[string]string{
		"RELIANCE":  "Reliance Industries",
		"TCS":       "Tata Consultancy Services",
		"INFY":      "Infosys",
		"HDFCBANK":  "HDFC Bank",
		"ICICIBANK": "ICICI Bank",
		"SBIN":      "State Bank of India",
		"ITC":       "ITC Limited",
		"WIPRO":     "Wipro",
		"AAPL":      "Apple Inc",
		"MSFT":      "Microsoft",
		"NVDA":      "Nvidia",
		"GOOGL":     "Alphabet Inc",
		"AMZN":      "Amazon.com",
		"TSLA":      "Tesla Inc",
		"META":      "Meta Platforms",
	}
}

// PollSummary reports what one pass collected.
type PollSummary struct {
	Symbols  int           `json:"symbols"`
	Fetched  int           `json:"fetched"`
	Added    int           `json:"added"`
	Errors   int           `json:"errors"`
	Duration time.Duration `json:"duration_ns"`
}

// PollAll collects news for every watched symbol.
func (p *Poller) PollAll(ctx context.Context) (PollSummary, error) {
	start := p.now()
	summary := PollSummary{}

	symbols, err := p.symbols.WatchedSymbols(ctx)
	if err != nil {
		return summary, fmt.Errorf("news: list symbols: %w", err)
	}
	summary.Symbols = len(symbols)

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	// Modest concurrency: enough to keep the pass short, few enough that we
	// do not look like a scraper to Google News.
	sem := make(chan struct{}, 3)

	for _, sym := range symbols {
		wg.Add(1)
		go func(sym marketdata.Symbol) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			added, fetched, err := p.pollSymbol(ctx, sym)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				summary.Errors++
				return
			}
			summary.Fetched += fetched
			summary.Added += added
		}(sym)
	}
	wg.Wait()

	// Housekeeping: without this the articles table grows without bound.
	if pruned, err := p.store.PruneArticles(ctx, p.now().Add(-retention)); err != nil {
		p.log.Warn("could not prune old articles", "err", err)
	} else if pruned > 0 {
		p.log.Debug("pruned old articles", "count", pruned)
	}

	summary.Duration = p.now().Sub(start)
	if summary.Symbols > 0 {
		p.log.Info("news poll complete",
			"symbols", summary.Symbols, "fetched", summary.Fetched,
			"added", summary.Added, "errors", summary.Errors,
			"duration", summary.Duration.Round(time.Millisecond))
	}
	return summary, nil
}

func (p *Poller) pollSymbol(ctx context.Context, sym marketdata.Symbol) (added, fetched int, err error) {
	feedURL := GoogleNewsFeed(sym.String(), p.companyNames[sym.Ticker], sym.IsIndian())

	body, err := p.fetch(ctx, feedURL)
	if err != nil {
		p.log.Warn("news feed fetch failed", "symbol", sym, "err", err)
		p.report(ctx, false, err)
		return 0, 0, err
	}

	items, _, err := ParseFeed(body)
	if err != nil {
		p.log.Warn("news feed did not parse", "symbol", sym, "err", err)
		p.report(ctx, false, err)
		return 0, 0, err
	}
	p.report(ctx, true, nil)

	now := p.now()
	cutoff := now.Add(-maxArticleAge)

	articles := make([]Article, 0, len(items))
	for _, item := range items {
		if len(articles) >= maxItemsPerFeed {
			break
		}
		// An undated item is kept — many feeds omit dates — but a clearly old
		// one is not.
		if !item.Published.IsZero() && item.Published.Before(cutoff) {
			continue
		}
		articles = append(articles, Article{
			Symbol:      sym.String(),
			Title:       item.Title,
			URL:         item.URL,
			Source:      item.Source,
			PublishedAt: item.Published,
			FetchedAt:   now,
		})
	}

	if len(articles) == 0 {
		return 0, 0, nil
	}
	added, err = p.store.SaveArticles(ctx, articles)
	if err != nil {
		return 0, len(articles), fmt.Errorf("news: save articles for %s: %w", sym, err)
	}
	return added, len(articles), nil
}

func (p *Poller) fetch(ctx context.Context, feedURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("news: build request: %w", err)
	}
	req.Header.Set("User-Agent", "tradesys-dashboard/1.0 (+self-hosted market dashboard)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("news: fetch feed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("news: read feed: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("news: feed returned http %d: %s", resp.StatusCode, snippet(body))
	}
	return body, nil
}

func (p *Poller) report(ctx context.Context, ok bool, err error) {
	if p.observe != nil {
		p.observe(ctx, "news", ok, false, err)
	}
}

func snippet(b []byte) string {
	const max = 160
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
