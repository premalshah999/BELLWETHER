package brave

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradesys/dashboard/internal/search"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSearchParsesResponse(t *testing.T) {
	var gotQuery, gotToken, gotFreshness string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotFreshness = r.URL.Query().Get("freshness")
		gotToken = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, fixture(t, "response.json"))
	}))
	defer srv.Close()

	c := New("test-token", WithBaseURL(srv.URL))
	got, err := c.Search(context.Background(), search.Query{Text: "Apple news", MaxResults: 5, Days: 3})
	if err != nil {
		t.Fatal(err)
	}

	if gotQuery != "Apple news" || gotToken != "test-token" {
		t.Errorf("query = %q, token = %q", gotQuery, gotToken)
	}
	if gotFreshness != "pw" {
		t.Errorf("freshness = %q, want pw for a 3-day window", gotFreshness)
	}

	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	// The <strong> highlighting Brave inserts must be stripped, or it shows
	// up verbatim in an AI citation.
	if strings.Contains(got[0].Snippet, "<strong>") {
		t.Errorf("HTML tags survived into the snippet: %q", got[0].Snippet)
	}
	if !strings.Contains(got[0].Snippet, "shares") {
		t.Errorf("stripping removed the text as well: %q", got[0].Snippet)
	}
	if got[0].Source != "cnbc.com" {
		t.Errorf("source = %q", got[0].Source)
	}
	if got[0].Published.IsZero() {
		t.Error("page_age was not parsed")
	}
	// The second result has an empty hostname, so it falls back to the URL.
	if got[1].Source != "bloomberg.com" {
		t.Errorf("fallback source = %q, want bloomberg.com", got[1].Source)
	}
	// Its only date is relative, which must stay zero rather than becoming
	// the parse time.
	if !got[1].Published.IsZero() {
		t.Errorf("relative age produced %v, want the zero time", got[1].Published)
	}
}

func TestFreshnessBuckets(t *testing.T) {
	tests := []struct {
		days int
		want string
	}{
		{days: 0, want: ""},
		{days: 1, want: "pd"},
		{days: 5, want: "pw"},
		{days: 20, want: "pm"},
		{days: 200, want: "py"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("freshness")
				io.WriteString(w, `{"results":[]}`)
			}))
			defer srv.Close()

			New("k", WithBaseURL(srv.URL)).Search(context.Background(),
				search.Query{Text: "q", MaxResults: 3, Days: tc.days})
			if got != tc.want {
				t.Errorf("days %d -> freshness %q, want %q", tc.days, got, tc.want)
			}
		})
	}
}

func TestStripTags(t *testing.T) {
	tests := map[string]string{
		"plain text":                        "plain text",
		"<strong>bold</strong> text":        "bold text",
		"a <em>b</em> c <strong>d</strong>": "a b c d",
		"":                                  "",
		"<b>":                               "",
	}
	for in, want := range tests {
		if got := stripTags(in); got != want {
			t.Errorf("stripTags(%q) = %q, want %q", in, got, want)
		}
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
		{name: "structured error", status: 422, body: `{"error":{"code":"SUBSCRIPTION_TOKEN_INVALID","detail":"bad token"}}`, wantText: "bad token"},
		{name: "rate limited", status: 429, body: `{}`, wantText: "429"},
		{name: "html", status: 500, body: `<html>oops</html>`, wantText: "500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := New("k", WithBaseURL(srv.URL)).Search(context.Background(), search.Query{Text: "q"})
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
	const key = "BSA-SUPER-SECRET"
	_, err := New(key, WithBaseURL("http://127.0.0.1:1")).Search(
		context.Background(), search.Query{Text: "q"})
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "SUPER-SECRET") {
		t.Errorf("the API key leaked: %v", err)
	}
}
