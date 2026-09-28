package research

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// Commercial search providers.
//
// These sit behind the same Scraper interface as the free feed-based ones, so
// adding a key changes what the research engine can reach without changing how
// it works. That is the point of the interface: the engine fans out across
// whatever is configured, merges by canonical URL, and keeps the
// highest-trust copy of anything two providers both return.

// TavilyScraper queries the Tavily search API.
//
// Tavily returns extracted page content rather than only a snippet, which is
// materially better input for synthesis than a headline — a summary written
// from two sentences of real text beats one written from a title.
type TavilyScraper struct {
	Client *http.Client
	APIKey string
	// Depth is "basic" or "advanced". Advanced costs more credits and reads
	// more of each page.
	Depth string
	// Domains, when set, restricts results. Left empty for general queries.
	Domains []string
}

func (t *TavilyScraper) Name() string { return "tavily" }

// Trust reflects that Tavily is a retrieval layer over the open web: what it
// returns is only as good as the page it found, so results carry
// specialist-tier trust until the publisher is recognised.
func (t *TavilyScraper) Trust() int { return news.TrustSpecialist }

// Configured reports whether this provider has what it needs to run.
func (t *TavilyScraper) Configured() bool { return strings.TrimSpace(t.APIKey) != "" }

type tavilyRequest struct {
	Query             string   `json:"query"`
	SearchDepth       string   `json:"search_depth"`
	MaxResults        int      `json:"max_results"`
	IncludeAnswer     bool     `json:"include_answer"`
	IncludeRawContent bool     `json:"include_raw_content"`
	IncludeDomains    []string `json:"include_domains,omitempty"`
	Topic             string   `json:"topic,omitempty"`
}

type tavilyResponse struct {
	Results []struct {
		Title         string  `json:"title"`
		URL           string  `json:"url"`
		Content       string  `json:"content"`
		Score         float64 `json:"score"`
		PublishedDate string  `json:"published_date"`
	} `json:"results"`
}

func (t *TavilyScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	if !t.Configured() {
		return nil, fmt.Errorf("tavily: no API key")
	}
	depth := t.Depth
	if depth == "" {
		depth = "basic"
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}

	body, err := json.Marshal(tavilyRequest{
		Query: query, SearchDepth: depth, MaxResults: limit,
		// The answer field is deliberately not requested. Synthesis happens
		// here, against sources this system chose and can cite; taking a
		// pre-written answer would import a conclusion whose sources we
		// cannot show the reader.
		IncludeAnswer: false, IncludeRawContent: false,
		IncludeDomains: t.Domains,
		// "news" biases towards recent reporting over reference pages, which
		// is what a market question almost always wants.
		Topic: "news",
	})
	if err != nil {
		return nil, fmt.Errorf("tavily: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)

	raw, err := doJSON(ctx, t.client(), req, "tavily")
	if err != nil {
		return nil, err
	}
	var out tavilyResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("tavily: decode response: %w", err)
	}

	findings := make([]Finding, 0, len(out.Results))
	for _, r := range out.Results {
		if r.URL == "" || strings.TrimSpace(r.Title) == "" {
			continue
		}
		findings = append(findings, Finding{
			Title: r.Title, URL: r.URL, Publisher: hostOf(r.URL),
			Snippet: truncate(r.Content, 400), PublishedAt: parseLooseDate(r.PublishedDate),
			Scraper: t.Name(), Trust: t.Trust(),
		})
	}
	return findings, nil
}

func (t *TavilyScraper) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

// BraveScraper queries the Brave Search API.
//
// Brave indexes independently rather than reselling another engine's results,
// so it surfaces documents the discovery feeds and Tavily both miss. That
// independence is the reason to run both: two providers agreeing is
// corroboration, whereas two resellers of one index agreeing is not.
type BraveScraper struct {
	Client *http.Client
	APIKey string
	// Country and SearchLang scope results; "US" and "en" for this app.
	Country    string
	SearchLang string
	// Freshness is Brave's recency filter: "pd" past day, "pw" past week,
	// "pm" past month.
	Freshness string
}

func (b *BraveScraper) Name() string { return "brave" }
func (b *BraveScraper) Trust() int   { return news.TrustSpecialist }

func (b *BraveScraper) Configured() bool { return strings.TrimSpace(b.APIKey) != "" }

func (b *BraveScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	if !b.Configured() {
		return nil, fmt.Errorf("brave: no API key")
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	country := b.Country
	if country == "" {
		country = "US"
	}
	lang := b.SearchLang
	if lang == "" {
		lang = "en"
	}
	freshness := b.Freshness
	if freshness == "" {
		freshness = "pw"
	}

	endpoint := fmt.Sprintf(
		"https://api.search.brave.com/res/v1/news/search?q=%s&count=%d&country=%s&search_lang=%s&freshness=%s",
		urlQueryEscape(query), limit, country, lang, freshness)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.APIKey)

	raw, err := doJSON(ctx, b.client(), req, "brave")
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Age         string `json:"age"`
			PageAge     string `json:"page_age"`
			MetaURL     struct {
				Hostname string `json:"hostname"`
			} `json:"meta_url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("brave: decode response: %w", err)
	}

	findings := make([]Finding, 0, len(out.Results))
	for _, r := range out.Results {
		if r.URL == "" || strings.TrimSpace(r.Title) == "" {
			continue
		}
		publisher := r.MetaURL.Hostname
		if publisher == "" {
			publisher = hostOf(r.URL)
		}
		findings = append(findings, Finding{
			Title: stripTags(r.Title), URL: r.URL, Publisher: publisher,
			Snippet:     truncate(stripTags(r.Description), 400),
			PublishedAt: parseLooseDate(firstNonEmpty(r.PageAge, r.Age)),
			Scraper:     b.Name(), Trust: b.Trust(),
		})
	}
	return findings, nil
}

func (b *BraveScraper) client() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return http.DefaultClient
}

// doJSON performs a request and returns the body, mapping the status codes
// these APIs actually use into errors a caller can act on.
func doJSON(ctx context.Context, client *http.Client, req *http.Request, name string) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%s: rate limited", name)
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// Worth distinguishing: a bad key is a configuration problem the
		// operator can fix, whereas a 500 is somebody else's outage.
		return nil, fmt.Errorf("%s: rejected the API key (%s)", name, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: returned %s", name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

func hostOf(rawURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "www.")
}

func truncate(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// stripTags removes the <strong> markers search APIs wrap query terms in.
func stripTags(s string) string {
	for {
		open := strings.IndexByte(s, '<')
		if open < 0 {
			return s
		}
		close := strings.IndexByte(s[open:], '>')
		if close < 0 {
			return s
		}
		s = s[:open] + s[open+close+1:]
	}
}

// parseLooseDate accepts the several formats these APIs use, and gives up
// quietly rather than inventing a timestamp.
func parseLooseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05",
		"2006-01-02", "January 2, 2006", "Jan 2, 2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('+')
		default:
			for _, c := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
	}
	return b.String()
}
