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

// TestQuestionTermsOutweighFiller is the 401(k) question that once ranked a
// Prime Day deals list and a footballer's biography above Fidelity's guide:
// "401(k)" tokenised to "401" and a dropped "k", so the one specific term in
// the question matched nothing, and "best" and "early" did all the work.
func TestQuestionTermsOutweighFiller(t *testing.T) {
	q := "Best strategies for early career 401k"
	findings := []Finding{
		{Title: "20+ of the best early Prime Day deals on trading cards", URL: "https://deals.example/1"},
		{Title: "Biography: Age, Early Life, Education and Career of the incoming chief", URL: "https://bio.example/2"},
		{Title: "How to max out your 401(k) and retirement savings | Fidelity", URL: "https://fidelity.example/3"},
		{Title: "Making the Most of Your 401(k) in Your 20s", URL: "https://schwab.example/4", Snippet: "Starting your career? Your employer's match is the first strategy."},
	}
	rankFindings(q, findings)
	if !strings.Contains(findings[0].URL, "schwab") || !strings.Contains(findings[1].URL, "fidelity") {
		t.Fatalf("ranking = %s, %s", findings[0].URL, findings[1].URL)
	}
	for _, f := range findings[2:] {
		if f.Relevance >= 10 {
			t.Fatalf("%q counted as relevant (%.1f)", f.Title, f.Relevance)
		}
	}
	if got := relevantOnly(findings); len(got) != 4 {
		t.Fatalf("with fewer than eight relevant sources nothing is dropped, got %d", len(got))
	}
}

// TestSubjectCompanyCountsAsAMatch: an article resolved to the company the
// question names is about it, whatever words the headline uses.
func TestSubjectCompanyCountsAsAMatch(t *testing.T) {
	findings := []Finding{
		{Title: "Cupertino's quarter: services carry the year", URL: "https://a.example/1", Symbols: []string{"AAPL"}},
		{Title: "Launches of the week in gadgets", URL: "https://b.example/2"},
	}
	rankFindingsFor("How is apple doing after the new Product Launches?", []string{"AAPL"}, findings)
	if findings[0].URL != "https://a.example/1" || findings[0].Relevance < 10 {
		t.Fatalf("subject article not ranked first as relevant: %+v", findings)
	}
}

func TestUSMarketOnly(t *testing.T) {
	in := []Finding{
		{URL: "https://www.livemint.com/money/personal-finance/x.html", Publisher: "collected"},
		{URL: "https://news.google.com/rss/articles/abc", Publisher: "The Economic Times"},
		{URL: "https://www.businesstoday.in/markets/x", Publisher: "Business Today"},
		{URL: "https://www.fidelity.com/learning-center/x", Publisher: "fidelity.com"},
	}
	out := usMarketOnly(in)
	if len(out) != 1 || !strings.Contains(out[0].URL, "fidelity") {
		t.Fatalf("kept %+v", out)
	}
}

// TestRefusingPublisherIsSkipped: a publisher that answered 403 is not
// tried again for a while, so the next question's reading goes elsewhere.
func TestRefusingPublisherIsSkipped(t *testing.T) {
	f := &ArticleFetcher{}
	f.noteRefusal("https://www.investopedia.com/a", fmt.Errorf("article: www.investopedia.com returned HTTP 403"))
	if !f.Blocked("https://investopedia.com/b") {
		t.Fatal("a 403 should block the publisher")
	}
	f.noteRefusal("https://js.example/a", fmt.Errorf("article: only 4 words extracted; full text unavailable"))
	if f.Blocked("https://js.example/b") {
		t.Fatal("one empty page should not block a publisher")
	}
	f.noteRefusal("https://js.example/c", fmt.Errorf("article: only 0 words extracted; full text unavailable"))
	if !f.Blocked("https://js.example/d") {
		t.Fatal("two empty pages should block it")
	}
	f.noteRefusal("https://slow.example/a", fmt.Errorf("context deadline exceeded"))
	if f.Blocked("https://slow.example/b") {
		t.Fatal("a timeout is not a refusal")
	}
}

// TestAggregatorDuplicateDropped: the Google News redirect for a headline
// already held with the publisher's own address adds nothing readable.
func TestAggregatorDuplicateDropped(t *testing.T) {
	out := dedupeFindings([]Finding{
		{Title: "Apple Stock Surges After Its Biggest Launch - MarketBeat", URL: "https://news.google.com/rss/articles/x"},
		{Title: "Apple Stock Surges After Its Biggest Launch", URL: "https://www.marketbeat.com/a"},
		{Title: "Only on Google News - Reuters", URL: "https://news.google.com/rss/articles/y"},
	})
	if len(out) != 2 || out[0].URL != "https://www.marketbeat.com/a" {
		t.Fatalf("kept %+v", out)
	}
}

func TestOfficialSitesForPersonalFinance(t *testing.T) {
	for q, want := range map[string]string{
		"Best strategies for early career 401k":         "irs.gov dol.gov",
		"When should I claim Social Security benefits?": "irs.gov ssa.gov",
		"Roth IRA or brokerage account for a first job": "irs.gov investor.gov",
	} {
		if !personalFinance.MatchString(q) {
			t.Errorf("%q not recognised as personal finance", q)
		}
		if got := strings.Join(officialSites(q), " "); got != want {
			t.Errorf("officialSites(%q) = %s, want %s", q, got, want)
		}
	}
	for _, q := range []string{"How has Apple reacted to earnings?", "What is driving Caterpillar shares this quarter?"} {
		if personalFinance.MatchString(q) {
			t.Errorf("%q matched as personal finance", q)
		}
	}
}

func TestAccountKind(t *testing.T) {
	for q, want := range map[string]string{
		"Best strategies for early career 401k": "401(k)",
		"how much can I put in a 403(b)":        "403(b)",
		"Roth IRA or brokerage account":         "Roth IRA",
		"HSA as a retirement account":           "HSA",
		"When should I claim Social Security?":  "",
	} {
		if got := accountKind(q); got != want {
			t.Errorf("accountKind(%q) = %q, want %q", q, got, want)
		}
	}
}

// TestPinnedRuleRanksFirst: this year's limit announcement shares few words
// with "best strategies for early career 401k", but it is the fact the
// advice turns on, and must be read.
func TestPinnedRuleRanksFirst(t *testing.T) {
	findings := []Finding{
		{Title: "Making the most of your 401(k) in your 20s and early career", URL: "https://schwab.example/1", Trust: 70},
		{Title: "Limit increases to $24,500 for 2026, IRA limit increases to $7,500", URL: "https://www.irs.gov/newsroom/x", Trust: 100, Pinned: true},
	}
	rankFindings("Best strategies for early career 401k", findings)
	if !findings[0].Pinned || findings[0].Relevance < 10 {
		t.Fatalf("pinned rule not first and relevant: %+v", findings)
	}
	if perHostCap(findings[0]) <= 3 {
		t.Fatal("an agency should be allowed more than three sources")
	}
}
