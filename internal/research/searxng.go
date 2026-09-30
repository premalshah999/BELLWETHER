package research

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tradesys/dashboard/internal/news"
)

// SearXNGScraper queries the private metasearch node: many search engines
// behind one JSON interface, on our own host, with no key and no cost per
// query. It works by scraping engines that would rather it did not (from here
// it reaches Google, DuckDuckGo News and Bing News; Brave and Startpage
// refuse), so any one engine may stop answering, and the per-engine failures
// come back in the response.
type SearXNGScraper struct {
	Client *http.Client
	// BaseURL of the node, e.g. http://searxng:8080.
	BaseURL string
	// Categories restricts which engines answer: "news" for reporting,
	// empty for the general web. News is narrower and fresher; general
	// reaches analysis, sector reports and primary documents that never
	// appear in a news index.
	Categories string
	// TimeRange is SearXNG's recency filter: day, week, month, year.
	TimeRange string
	// Pages is how many result pages to request, about ten results each. Worth
	// paging because SearXNG returns the publisher's own URL, which can be
	// read.
	Pages int
	// Language scopes results.
	Language string
	// Label distinguishes several configured instances in results.
	Label string
}

func (s *SearXNGScraper) Name() string {
	if s.Label != "" {
		return s.Label
	}
	return "searxng"
}

// Trust reflects what a metasearch aggregate is: a mixture of whatever the
// underlying engines ranked highly, which spans wire reports and content
// farms. Specialist tier until the publisher itself is recognised.
func (s *SearXNGScraper) Trust() int { return news.TrustSpecialist }

// Configured reports whether this provider can run.
func (s *SearXNGScraper) Configured() bool { return strings.TrimSpace(s.BaseURL) != "" }

type searxngResponse struct {
	Results []struct {
		Title         string   `json:"title"`
		URL           string   `json:"url"`
		Content       string   `json:"content"`
		PublishedDate string   `json:"publishedDate"`
		Engine        string   `json:"engine"`
		Engines       []string `json:"engines"`
		Score         float64  `json:"score"`
	} `json:"results"`
	// Per-engine failures, reported rather than silently dropped. A query
	// answered by one engine instead of four is a materially thinner answer,
	// and the difference should be visible.
	UnresponsiveEngines [][]any `json:"unresponsive_engines"`
}

func (s *SearXNGScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	if !s.Configured() {
		return nil, fmt.Errorf("%s: no base URL", s.Name())
	}
	if limit <= 0 {
		limit = 15
	}
	// Capped high rather than at 15: this scraper is the main supplier of
	// pages that can actually be read, so its ceiling should be set by what
	// the synthesis prompt can hold, not by what suited a headline list.
	if limit > 80 {
		limit = 80
	}

	pages := s.Pages
	if pages <= 0 {
		pages = 1
	}

	var (
		findings []Finding
		seen     = map[string]bool{}
		lastErr  error
	)
	for page := 1; page <= pages && len(findings) < limit; page++ {
		got, err := s.searchPage(ctx, query, limit-len(findings), page)
		if err != nil {
			lastErr = err
			// A later page failing is not the query failing. Whatever the
			// earlier pages returned is still a real result.
			break
		}
		if len(got) == 0 {
			break
		}
		for _, f := range got {
			if seen[f.URL] {
				continue
			}
			seen[f.URL] = true
			findings = append(findings, f)
		}
	}
	if len(findings) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return findings, nil
}

func (s *SearXNGScraper) searchPage(ctx context.Context, query string, limit, page int) ([]Finding, error) {
	params := url.Values{
		"q":      {query},
		"format": {"json"},
	}
	if s.Language != "" {
		params.Set("language", s.Language)
	}
	if s.Categories != "" {
		params.Set("categories", s.Categories)
	}
	if s.TimeRange != "" {
		params.Set("time_range", s.TimeRange)
	}
	if page > 1 {
		params.Set("pageno", strconv.Itoa(page))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(s.BaseURL, "/")+"/search?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", s.Name(), err)
	}
	req.Header.Set("Accept", "application/json")

	raw, err := doJSON(ctx, s.client(), req, s.Name())
	if err != nil {
		return nil, err
	}
	var out searxngResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", s.Name(), err)
	}

	findings := make([]Finding, 0, min(len(out.Results), limit))
	for _, r := range out.Results {
		if len(findings) >= limit {
			break
		}
		if r.URL == "" || strings.TrimSpace(r.Title) == "" {
			continue
		}
		findings = append(findings, Finding{
			Title:     strings.TrimSpace(r.Title),
			URL:       r.URL,
			Publisher: hostOf(r.URL),
			Snippet:   truncate(r.Content, 400),
			// Frequently absent, and never invented when it is. A result with
			// no date is undated rather than assumed recent; the alternative
			// puts an undated document at the top of a list sorted by time.
			PublishedAt: parseLooseDate(r.PublishedDate),
			Scraper:     s.Name(),
			Trust:       s.Trust(),
		})
	}

	// A query that reached one engine instead of four is a thinner answer,
	// not a successful one. Reporting the failures lets the research trace
	// show it rather than leaving the reader to wonder why coverage varies.
	if len(out.UnresponsiveEngines) > 0 && len(findings) == 0 {
		return nil, fmt.Errorf("%s: every engine refused (%s)",
			s.Name(), summariseFailures(out.UnresponsiveEngines))
	}
	return findings, nil
}

func (s *SearXNGScraper) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

// summariseFailures renders SearXNG's per-engine error pairs readably.
func summariseFailures(entries [][]any) string {
	var parts []string
	for _, e := range entries {
		if len(e) == 0 {
			continue
		}
		name := fmt.Sprint(e[0])
		if len(e) > 1 {
			parts = append(parts, name+": "+fmt.Sprint(e[1]))
		} else {
			parts = append(parts, name)
		}
		if len(parts) >= 4 {
			break
		}
	}
	return strings.Join(parts, "; ")
}
