// Package brave adapts the Brave Search API to search.Provider.
package brave

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/search"
)

// DefaultBaseURL is Brave's API host.
const DefaultBaseURL = "https://api.search.brave.com"

// Name identifies this provider.
const Name = "brave"

// Client is the Brave adapter.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at a different host, used by tests.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a Brave adapter.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name identifies this provider.
func (c *Client) Name() string { return Name }

// Configured reports whether an API key was supplied.
func (c *Client) Configured() bool { return c.apiKey != "" }

type newsResponse struct {
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
	Error *struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	} `json:"error"`
}

// Search runs a query against Brave's news endpoint.
func (c *Client) Search(ctx context.Context, q search.Query) ([]search.Result, error) {
	if !c.Configured() {
		return nil, search.ErrNotConfigured
	}

	params := url.Values{
		"q":     {q.Text},
		"count": {strconv.Itoa(q.MaxResults)},
	}
	if q.Days > 0 {
		// Brave expresses recency as a coarse bucket rather than a day count.
		switch {
		case q.Days <= 1:
			params.Set("freshness", "pd")
		case q.Days <= 7:
			params.Set("freshness", "pw")
		case q.Days <= 31:
			params.Set("freshness", "pm")
		default:
			params.Set("freshness", "py")
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/res/v1/news/search?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: request: %w", redact(err, c.apiKey))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("brave: read response: %w", err)
	}

	var parsed newsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("brave: http %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("brave: decode response: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("brave: %s: %s", parsed.Error.Code, parsed.Error.Detail)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("brave: http %d", resp.StatusCode)
	}

	out := make([]search.Result, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if r.URL == "" || r.Title == "" {
			continue
		}
		source := r.MetaURL.Hostname
		if source == "" {
			source = hostOf(r.URL)
		}
		out = append(out, search.Result{
			Title:     strings.TrimSpace(r.Title),
			URL:       r.URL,
			Snippet:   truncate(stripTags(r.Description), 400),
			Source:    source,
			Published: parseAge(firstNonEmpty(r.PageAge, r.Age)),
		})
	}
	return out, nil
}

// parseAge handles Brave's mix of absolute timestamps and relative ages such
// as "3 hours ago". An unrecognised value yields the zero time.
func parseAge(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	// Relative forms are deliberately not converted to an absolute time here:
	// doing so would bake in the moment of parsing and make a cached result
	// appear to age backwards.
	return time.Time{}
}

// stripTags removes the <strong> highlighting Brave wraps matched terms in.
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func hostOf(rawURL string) string {
	s := rawURL
	for _, prefix := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimPrefix(s, "www.")
	if i := strings.IndexByte(s, '/'); i > 0 {
		s = s[:i]
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func redact(err error, key string) error {
	if key == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), key, "[redacted]"))
}

var _ search.Provider = (*Client)(nil)
