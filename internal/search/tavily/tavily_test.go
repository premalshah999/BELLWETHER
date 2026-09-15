package tavily

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradesys/dashboard/internal/search"
)

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSearchParsesResponse(t *testing.T) {
	srv := serve(t, http.StatusOK, fixture(t, "response.json"))
	c := New("test-key", WithBaseURL(srv.URL))

	got, err := c.Search(context.Background(), search.Query{Text: "Reliance Industries news", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	// The third entry has no title and must be dropped rather than surfaced
	// as a blank citation.
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}

	first := got[0]
	if !strings.HasPrefix(first.Title, "Reliance Industries slips") {
		t.Errorf("title = %q", first.Title)
	}
	if first.Source != "reuters.com" {
		t.Errorf("source = %q, want reuters.com derived from the URL", first.Source)
	}
	if first.Published.IsZero() || first.Published.Format("2006-01-02") != "2026-08-24" {
		t.Errorf("published = %v, want 2026-08-24", first.Published)
	}
	if first.Snippet == "" {
		t.Error("snippet is empty")
	}

	// The second entry uses a different date format.
	if got[1].Published.IsZero() {
		t.Error("the RFC1123Z published date was not parsed")
	}
}

func TestUnparseableDateStaysZero(t *testing.T) {
	// A zero time must never be rendered as "now", so it has to stay zero
	// rather than defaulting to the parse moment.
	for _, in := range []string{"", "yesterday", "not a date", "13/45/2026"} {
		if got := parseDate(in); !got.IsZero() {
			t.Errorf("parseDate(%q) = %v, want the zero time", in, got)
		}
	}
}

func TestRequestShape(t *testing.T) {
	var captured searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &captured)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	c := New("test-key", WithBaseURL(srv.URL))
	c.Search(context.Background(), search.Query{Text: "q", MaxResults: 7, Days: 3})

	if captured.APIKey != "test-key" {
		t.Errorf("api_key = %q", captured.APIKey)
	}
	if captured.Query != "q" || captured.MaxResults != 7 || captured.Days != 3 {
		t.Errorf("request = %+v", captured)
	}
	if captured.Topic != "news" {
		t.Errorf("topic = %q, want news", captured.Topic)
	}
}

func TestUnconfigured(t *testing.T) {
	c := New("")
	if c.Configured() {
		t.Error("Configured = true without a key")
	}
	if _, err := c.Search(context.Background(), search.Query{Text: "q"}); err != search.ErrNotConfigured {
		t.Errorf("error = %v, want ErrNotConfigured", err)
	}
}

func TestErrorResponses(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantText string
	}{
		{name: "error field", status: 200, body: `{"error":"Invalid API key"}`, wantText: "Invalid API key"},
		{name: "detail error", status: 401, body: `{"detail":{"error":"Unauthorized"}}`, wantText: "Unauthorized"},
		{name: "html body", status: 502, body: `<html>bad gateway</html>`, wantText: "502"},
		{name: "plain 429", status: 429, body: `{}`, wantText: "429"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, tc.status, tc.body)
			c := New("k", WithBaseURL(srv.URL))
			_, err := c.Search(context.Background(), search.Query{Text: "q"})
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not mention %q", err, tc.wantText)
			}
		})
	}
}

func TestAPIKeyNotLeakedInErrors(t *testing.T) {
	const key = "tvly-SUPER-SECRET"
	c := New(key, WithBaseURL("http://127.0.0.1:1"))
	_, err := c.Search(context.Background(), search.Query{Text: "q"})
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "SUPER-SECRET") {
		t.Errorf("the API key leaked: %v", err)
	}
}
