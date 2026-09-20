package research

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestArticleRejectsPrivateAddressesAndRedirects(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "::1", "::ffff:127.0.0.1", "fc00::1", "64:ff9b::a00:1"} {
		if publicIP(netip.MustParseAddr(address)) {
			t.Errorf("accepted private address %s", address)
		}
	}
	for _, raw := range []string{"http://localhost/a", "http://127.0.0.1/", "http://[::1]/", "http://private.local/a", "http://public.example:5432/", "file:///etc/passwd", "https://name:password@public.example/"} {
		if _, err := articleURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	f := NewArticleFetcher()
	var privateRequests atomic.Int32
	f.HTTP = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/robots.txt" {
			return testResponse(r, 404, ""), nil
		}
		if r.URL.Hostname() == "127.0.0.1" {
			privateRequests.Add(1)
		}
		resp := testResponse(r, 302, "")
		resp.Header.Set("Location", "http://127.0.0.1/secrets")
		return resp, nil
	})}
	if _, err := f.Fetch(context.Background(), "https://publisher.example/article"); err == nil {
		t.Fatal("private redirect accepted")
	}
	if privateRequests.Load() != 0 {
		t.Fatal("followed private redirect")
	}
}

func TestArticleRobotsAndCache(t *testing.T) {
	for _, denied := range []bool{true, false} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			f := NewArticleFetcher()
			var pages, robots atomic.Int32
			f.HTTP = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/robots.txt" {
					robots.Add(1)
					if denied {
						return testResponse(r, 200, "User-agent: *\nDisallow: /article\n"), nil
					}
					return testResponse(r, 404, ""), nil
				}
				pages.Add(1)
				return testResponse(r, 200, "<article><p>"+strings.Repeat("Revenue rose during the reporting period. ", 30)+"</p></article>"), nil
			})}
			a, err := f.Fetch(context.Background(), "https://publisher.example/article")
			if denied {
				if err == nil || pages.Load() != 0 {
					t.Fatal("robots restriction ignored")
				}
				return
			}
			if err != nil || a.Words < 120 || a.FetchedAt.IsZero() {
				t.Fatalf("extraction: %+v, %v", a, err)
			}
			cached, err := f.Fetch(context.Background(), "https://publisher.example/article")
			if err != nil || !cached.Cached || pages.Load() != 1 || robots.Load() != 1 {
				t.Fatal("second fetch did not reuse cache")
			}
		})
	}
}

func TestArticleRateLimitAndCancellation(t *testing.T) {
	f := NewArticleFetcher()
	var pages atomic.Int32
	f.HTTP = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/robots.txt" {
			return testResponse(r, 404, ""), nil
		}
		pages.Add(1)
		resp := testResponse(r, 429, "")
		resp.Header.Set("Retry-After", "300")
		return resp, nil
	})}
	_, _ = f.Fetch(context.Background(), "https://publisher.example/first")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.Fetch(ctx, "https://publisher.example/second"); err == nil {
		t.Fatal("expected canceled wait")
	}
	if pages.Load() != 1 {
		t.Fatal("ignored publisher Retry-After")
	}
}

func TestRelevantEvidenceAndStableCitations(t *testing.T) {
	findings := []Finding{
		{Title: "Routine unrelated filing", URL: "https://official.example/1", Trust: 100, Body: "unrelated"},
		{Title: "NVIDIA export restrictions revenue", URL: "https://press.example/2", Trust: 80, Body: "NVIDIA revenue"},
		{Title: "NVIDIA export restrictions", URL: "https://press.example/3", Trust: 80},
	}
	rankFindings("What changed in NVIDIA export restrictions?", findings)
	if findings[0].URL != "https://press.example/2" {
		t.Fatalf("unrelated authority outranked evidence: %+v", findings)
	}
	indexes := EvidenceIndexes(findings, 12)
	for _, i := range indexes {
		if findings[i].Body == "" {
			t.Fatal("headline-only finding selected for synthesis")
		}
	}
	text := strings.Repeat("Background information about ordinary operations. ", 80) + "NVIDIA export restrictions reduced revenue by 450 million dollars. " + strings.Repeat("Additional routine discussion. ", 80)
	excerpt := EvidenceExcerpt(text, "NVIDIA export restrictions revenue", 240)
	if !strings.Contains(excerpt, "450 million") || len(strings.Fields(excerpt)) > 250 {
		t.Fatal("lost the relevant passage or exceeded budget")
	}
}

type countingScraper struct{ calls atomic.Int32 }

func (s *countingScraper) Name() string { return "counter" }
func (s *countingScraper) Trust() int   { return 80 }
func (s *countingScraper) Search(context.Context, string, int) ([]Finding, error) {
	s.calls.Add(1)
	return []Finding{{Title: "Original", URL: "https://source.example/a"}}, nil
}

func TestDiscoveryCacheDoesNotShareMutableFindings(t *testing.T) {
	s := &countingScraper{}
	cached := cacheScraper(s)
	first, _ := cached.Search(context.Background(), "query", 12)
	first[0].Title = "changed"
	second, _ := cached.Search(context.Background(), "query", 12)
	if s.calls.Load() != 1 || second[0].Title != "Original" {
		t.Fatal("cache was missed or mutated by caller")
	}
}

type panickingScraper struct{ countingScraper }

func (*panickingScraper) Search(context.Context, string, int) ([]Finding, error) {
	panic("bad provider")
}
func TestProviderPanicIsReportedWithoutCrashing(t *testing.T) {
	result, err := NewEngine([]Scraper{&panickingScraper{}}).Search(context.Background(), "query", 12)
	if err != nil || len(result.Scrapers) != 1 || result.Scrapers[0].Error == "" {
		t.Fatalf("missing provider failure: %+v %v", result, err)
	}
}

func TestDiscoveryCooldownIsSharedAcrossAdapters(t *testing.T) {
	var calls atomic.Int32
	transport := &DiscoveryTransport{Base: testTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		resp := testResponse(r, 429, "")
		resp.Header.Set("Retry-After", "300")
		return resp, nil
	})}
	first, _ := http.NewRequest(http.MethodGet, "https://search.example/first", nil)
	resp, err := transport.RoundTrip(first)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://search.example/another-adapter", nil)
	if _, err := transport.RoundTrip(second); err == nil {
		t.Fatal("expected cooldown cancellation")
	}
	if calls.Load() != 1 {
		t.Fatal("another adapter ignored the shared rate limit")
	}
}
