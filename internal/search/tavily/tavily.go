// Package tavily adapts the Tavily search API to search.Provider.
package tavily

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/search"
)

// DefaultBaseURL is Tavily's API host.
const DefaultBaseURL = "https://api.tavily.com"

// Name identifies this provider.
const Name = "tavily"

// Client is the Tavily adapter.
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

// New builds a Tavily adapter.
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

type searchRequest struct {
	APIKey      string `json:"api_key"`
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
	Topic       string `json:"topic"`
	Days        int    `json:"days,omitempty"`
}

type searchResponse struct {
	Results []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		PublishedDate string `json:"published_date"`
	} `json:"results"`
	// Tavily reports errors in a plain string field.
	Error  string `json:"error"`
	Detail struct {
		Error string `json:"error"`
	} `json:"detail"`
}

// Search runs a query against Tavily's news topic.
func (c *Client) Search(ctx context.Context, q search.Query) ([]search.Result, error) {
	if !c.Configured() {
		return nil, search.ErrNotConfigured
	}

	body, err := json.Marshal(searchRequest{
		APIKey:     c.apiKey,
		Query:      q.Text,
		MaxResults: q.MaxResults,
		// "basic" is markedly cheaper and enough for headline context.
		SearchDepth: "basic",
		Topic:       "news",
		Days:        q.Days,
	})
	if err != nil {
		return nil, fmt.Errorf("tavily: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: request: %w", redact(err, c.apiKey))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("tavily: read response: %w", err)
	}

	var parsed searchResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("tavily: http %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("tavily: decode response: %w", err)
	}
	if msg := firstNonEmpty(parsed.Error, parsed.Detail.Error); msg != "" {
		return nil, fmt.Errorf("tavily: %s", msg)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tavily: http %d", resp.StatusCode)
	}

	out := make([]search.Result, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if r.URL == "" || r.Title == "" {
			continue
		}
		out = append(out, search.Result{
			Title:     strings.TrimSpace(r.Title),
			URL:       r.URL,
			Snippet:   truncate(strings.TrimSpace(r.Content), 400),
			Source:    hostOf(r.URL),
			Published: parseDate(r.PublishedDate),
		})
	}
	return out, nil
}

// parseDate accepts the several formats Tavily has been seen to emit. An
// unparseable date yields the zero time, which callers must not render as
// "now".
func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		"2006-01-02",
		"Mon, 02 Jan 2006 15:04:05 -0700",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
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
