package research

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// maxBody caps a scraper response. These are search results, not documents.
const maxBody = 4 << 20

// httpGet performs a fetch with the same header policy as the ingestion
// engine: no User-Agent at all, which is what NSE and several publishers
// actually accept. See news.Engine.get for the measurements behind that.
func httpGet(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "")
	req.Header.Set("Accept", "application/rss+xml, application/xml, application/json;q=0.9, */*;q=0.8")
	req.Header.Set("Accept-Language", "en-IN,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("returned %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// GoogleNewsScraper searches Google News for a free-text query.
//
// This is discovery, not content: what comes back is a publisher's headline
// and a link to their page, which is what gets displayed. The same usage
// caveat applies as in the source catalog — Google's feed terms describe RSS
// use as non-commercial — so results are marked for display as link and
// headline only.
type GoogleNewsScraper struct {
	Client *http.Client
	// Region scopes the search. Indian coverage is the default because that
	// is what this application is about.
	HL, GL string
}

func (g *GoogleNewsScraper) Name() string { return "google_news" }

// Trust reflects what this route typically surfaces: mainstream and wire
// coverage, one notch below a direct feed from the publisher because the
// aggregator's timestamp and attribution are second-hand.
func (g *GoogleNewsScraper) Trust() int { return news.TrustMajorFin }

func (g *GoogleNewsScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	hl, gl := g.HL, g.GL
	if hl == "" {
		hl, gl = "en-IN", "IN"
	}
	feedURL := news.GoogleNewsSearch(query, hl, gl)

	body, err := httpGet(ctx, g.client(), feedURL)
	if err != nil {
		return nil, fmt.Errorf("google news: %w", err)
	}
	items, feedTitle, err := news.ParseFeedItems(body)
	if err != nil {
		return nil, fmt.Errorf("google news: %w", err)
	}
	out := make([]Finding, 0, len(items))
	for i, it := range items {
		if i >= limit {
			break
		}
		publisher := it.Source
		if publisher == "" {
			publisher = feedTitle
		}
		out = append(out, Finding{
			Title: it.Title, URL: it.URL, Publisher: publisher,
			Snippet: it.Description, PublishedAt: it.Published,
			Scraper: g.Name(), Trust: g.Trust(),
		})
	}
	return out, nil
}

func (g *GoogleNewsScraper) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return http.DefaultClient
}

// GDELTScraper queries the GDELT DOC API.
//
// GDELT is a coverage-expansion layer: its job is to surface documents the
// curated feeds missed, not to be authoritative. Its results therefore carry
// aggregator-level trust until something better corroborates them. It also
// rate-limits aggressively from datacentre addresses, so a failure here is
// expected traffic rather than a fault.
type GDELTScraper struct {
	Client *http.Client
	// Country scopes results, e.g. "india". Empty searches worldwide.
	Country string
	// Timespan is the lookback window in GDELT's own syntax, e.g. "3d".
	Timespan string
}

func (g *GDELTScraper) Name() string { return "gdelt" }
func (g *GDELTScraper) Trust() int   { return news.TrustAggregator }

func (g *GDELTScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	if limit > 75 {
		limit = 75
	}
	q := query
	if g.Country != "" {
		q += " sourcecountry:" + g.Country
	}
	timespan := g.Timespan
	if timespan == "" {
		timespan = "7d"
	}
	params := url.Values{
		"query":      {q},
		"mode":       {"ArtList"},
		"format":     {"json"},
		"maxrecords": {fmt.Sprint(limit)},
		"timespan":   {timespan},
	}
	body, err := httpGet(ctx, g.client(), "https://api.gdeltproject.org/api/v2/doc/doc?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("gdelt: %w", err)
	}

	var doc struct {
		Articles []struct {
			URL      string `json:"url"`
			Title    string `json:"title"`
			SeenDate string `json:"seendate"`
			Domain   string `json:"domain"`
		} `json:"articles"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		// GDELT answers an over-eager client with a sentence rather than
		// JSON, so a decode failure here usually means a rate limit.
		return nil, fmt.Errorf("gdelt: unreadable response (%s)", firstLine(body))
	}
	out := make([]Finding, 0, len(doc.Articles))
	for _, a := range doc.Articles {
		if a.URL == "" || strings.TrimSpace(a.Title) == "" {
			continue
		}
		seen, _ := time.Parse("20060102T150405Z", a.SeenDate)
		out = append(out, Finding{
			Title: a.Title, URL: a.URL, Publisher: a.Domain,
			PublishedAt: seen.UTC(), Scraper: g.Name(), Trust: g.Trust(),
		})
	}
	return out, nil
}

func (g *GDELTScraper) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return http.DefaultClient
}

// PublisherScraper searches a single publisher through Google News.
//
// It exists because the publishers whose analysis is most worth reading —
// Reuters, Bloomberg, the FT — are exactly the ones that will not serve a
// search endpoint directly. Scoping a discovery query to one domain gets their
// headlines on a subject without pretending to have their content.
type PublisherScraper struct {
	Client *http.Client
	// Domain is the site to scope to, e.g. "reuters.com".
	Domain string
	// Label names the publisher in results.
	Label string
	// TrustLevel is the publisher's credibility tier.
	TrustLevel int
	// Window is a Google News recency filter, e.g. "7d".
	Window string
}

func (p *PublisherScraper) Name() string {
	if p.Label != "" {
		return p.Label
	}
	return p.Domain
}

func (p *PublisherScraper) Trust() int {
	if p.TrustLevel > 0 {
		return p.TrustLevel
	}
	return news.TrustMajorFin
}

func (p *PublisherScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	window := p.Window
	if window == "" {
		window = "14d"
	}
	scoped := fmt.Sprintf("site:%s %s when:%s", p.Domain, query, window)
	feedURL := news.GoogleNewsSearch(scoped, "en-IN", "IN")

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	body, err := httpGet(ctx, client, feedURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name(), err)
	}
	items, _, err := news.ParseFeedItems(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name(), err)
	}
	out := make([]Finding, 0, len(items))
	for i, it := range items {
		if i >= limit {
			break
		}
		publisher := it.Source
		if publisher == "" {
			publisher = p.Name()
		}
		out = append(out, Finding{
			Title: it.Title, URL: it.URL, Publisher: publisher,
			Snippet: it.Description, PublishedAt: it.Published,
			Scraper: p.Name(), Trust: p.Trust(),
		})
	}
	return out, nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 90 {
		s = s[:90] + "…"
	}
	return s
}
