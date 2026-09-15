package research

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tradesys/dashboard/internal/news"
)

// SECFullTextScraper searches the text of every SEC filing since 2001 --
// not a headline about a filing, the filing itself. Nothing else in this
// research engine reads primary source material this directly: every other
// scraper reads what a publisher wrote about a company, and this one reads
// what the company itself filed under penalty of the securities laws.
//
// Free, keyless, and requires the same declared-contact User-Agent every
// other SEC endpoint in this app does -- see internal/config's SECUserAgent.
type SECFullTextScraper struct {
	Client    *http.Client
	UserAgent string
	// Forms restricts the search to these form types, comma-separated
	// (e.g. "8-K,10-K,10-Q"). Empty searches every form SEC indexes.
	Forms string
}

func (s *SECFullTextScraper) Name() string { return "sec_fulltext" }
func (s *SECFullTextScraper) Trust() int   { return news.TrustOfficial }

func (s *SECFullTextScraper) Configured() bool { return strings.TrimSpace(s.UserAgent) != "" }

func (s *SECFullTextScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	if !s.Configured() {
		return nil, fmt.Errorf("sec_fulltext: no contact user agent configured")
	}
	if limit <= 0 || limit > 40 {
		limit = 20
	}
	endpoint := "https://efts.sec.gov/LATEST/search-index?q=" + urlQueryEscape(query)
	if s.Forms != "" {
		endpoint += "&forms=" + urlQueryEscape(s.Forms)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("sec_fulltext: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.UserAgent)

	raw, err := doJSON(ctx, s.client(), req, "sec_fulltext")
	if err != nil {
		return nil, err
	}

	var out struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"` // "<accession>:<filename>"
				Source struct {
					CIKs         []string `json:"ciks"`
					DisplayNames []string `json:"display_names"`
					Form         string   `json:"form"`
					ADSH         string   `json:"adsh"`
					FileDate     string   `json:"file_date"`
					FileDesc     string   `json:"file_description"`
					Items        []string `json:"items"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("sec_fulltext: decode response: %w", err)
	}

	findings := make([]Finding, 0, len(out.Hits.Hits))
	for i, h := range out.Hits.Hits {
		if i >= limit {
			break
		}
		src := h.Source
		if len(src.CIKs) == 0 || src.ADSH == "" {
			continue
		}
		_, filename, hasFile := strings.Cut(h.ID, ":")
		if !hasFile {
			continue
		}
		cik, err := strconv.Atoi(strings.TrimLeft(src.CIKs[0], "0"))
		if err != nil || cik == 0 {
			continue
		}
		url := fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%d/%s/%s",
			cik, strings.ReplaceAll(src.ADSH, "-", ""), filename)

		company := "Unknown filer"
		if len(src.DisplayNames) > 0 {
			company = src.DisplayNames[0]
		}
		title := fmt.Sprintf("%s: %s", company, src.Form)
		if len(src.Items) > 0 {
			title += " (Item " + strings.Join(src.Items, ", ") + ")"
		}

		findings = append(findings, Finding{
			Title: title, URL: url, Publisher: "sec.gov",
			Snippet:     src.FileDesc,
			PublishedAt: parseLooseDate(src.FileDate),
			Scraper:     s.Name(), Trust: s.Trust(),
		})
	}
	return findings, nil
}

func (s *SECFullTextScraper) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

// FederalRegisterScraper searches the Federal Register: every proposed and
// final rule, executive order and agency notice the US government publishes.
// Free, keyless, no rate limit stated in its docs at the volume this app
// generates.
type FederalRegisterScraper struct {
	Client *http.Client
}

func (f *FederalRegisterScraper) Name() string { return "federal_register" }
func (f *FederalRegisterScraper) Trust() int   { return news.TrustOfficial }

func (f *FederalRegisterScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	if limit <= 0 || limit > 40 {
		limit = 20
	}
	endpoint := fmt.Sprintf(
		"https://www.federalregister.gov/api/v1/documents.json?conditions%%5Bterm%%5D=%s&per_page=%d&order=relevance",
		urlQueryEscape(query), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("federal_register: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	raw, err := doJSON(ctx, f.client(), req, "federal_register")
	if err != nil {
		return nil, err
	}

	var out struct {
		Results []struct {
			Title           string `json:"title"`
			HTMLURL         string `json:"html_url"`
			Type            string `json:"type"`
			PublicationDate string `json:"publication_date"`
			Excerpts        string `json:"excerpts"`
			Agencies        []struct {
				Name string `json:"name"`
			} `json:"agencies"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("federal_register: decode response: %w", err)
	}

	findings := make([]Finding, 0, len(out.Results))
	for _, r := range out.Results {
		if r.HTMLURL == "" || strings.TrimSpace(r.Title) == "" {
			continue
		}
		agency := ""
		if len(r.Agencies) > 0 {
			agency = r.Agencies[0].Name
		}
		findings = append(findings, Finding{
			Title: r.Title, URL: r.HTMLURL, Publisher: firstNonEmpty(agency, "Federal Register"),
			Snippet:     truncate(stripTags(r.Excerpts), 400),
			PublishedAt: parseLooseDate(r.PublicationDate),
			Scraper:     f.Name(), Trust: f.Trust(),
		})
	}
	return findings, nil
}

func (f *FederalRegisterScraper) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return http.DefaultClient
}
