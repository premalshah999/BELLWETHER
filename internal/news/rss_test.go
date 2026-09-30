package news

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseGoogleNewsRSS(t *testing.T) {
	items, title, err := ParseFeed(fixture(t, "google_news.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "Exxon") {
		t.Errorf("feed title = %q", title)
	}
	// Two entries are unusable — one with no link, one with no title — and
	// must be dropped rather than stored as blanks.
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}

	first := items[0]
	if !strings.HasPrefix(first.Title, "Exxon Mobil slips") {
		t.Errorf("title = %q", first.Title)
	}
	// Google News names the real publisher in <source>; that is far more
	// useful than "news.google.com".
	if first.Source != "Reuters" {
		t.Errorf("source = %q, want Reuters from the <source> element", first.Source)
	}
	if first.Published.Format("2006-01-02 15:04") != "2026-08-24 09:12" {
		t.Errorf("published = %v", first.Published)
	}

	// HTML entities in titles must be decoded.
	if !strings.Contains(items[1].Title, "Permian & leads") {
		t.Errorf("entity was not decoded: %q", items[1].Title)
	}
	if items[1].Source != "CNBC" {
		t.Errorf("source = %q", items[1].Source)
	}

	// An unparseable date stays zero rather than becoming "now".
	if !items[2].Published.IsZero() {
		t.Errorf("unparseable pubDate produced %v, want the zero time", items[2].Published)
	}
	if items[2].Source != "MarketWatch" {
		t.Errorf("source = %q, want MarketWatch", items[2].Source)
	}
}

func TestParseAtom(t *testing.T) {
	items, title, err := ParseFeed(fixture(t, "atom.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if title != "Apple Newsroom" {
		t.Errorf("title = %q", title)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Title != "Apple reports record services revenue" {
		t.Errorf("title = %q", items[0].Title)
	}
	if items[0].Published.Format(time.RFC3339) != "2026-08-24T10:15:00Z" {
		t.Errorf("published = %v, want the <published> value preferred over <updated>", items[0].Published)
	}
	// The alternate link is the article; the self link is the feed.
	if items[1].URL != "https://www.apple.com/newsroom/campus/" {
		t.Errorf("url = %q, want the alternate link", items[1].URL)
	}
	if items[1].Published.IsZero() {
		t.Error("the entry with only <updated> lost its date")
	}
}

func TestParseFeedMalformed(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "not xml", body: "<html>429 rate limited</html>", wantErr: false},
		{name: "empty", body: "", wantErr: true},
		{name: "truncated", body: `<?xml version="1.0"?><rss><channel><item>`, wantErr: true},
		{name: "valid but empty channel", body: `<?xml version="1.0"?><rss><channel></channel></rss>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			items, _, err := ParseFeed([]byte(tc.body))
			if tc.wantErr && err == nil {
				t.Error("want an error")
			}
			// Whatever happens, nothing usable may be invented.
			if len(items) != 0 {
				t.Errorf("got %d items from malformed input", len(items))
			}
		})
	}
}

func TestParseFeedTime(t *testing.T) {
	tests := map[string]bool{
		"Mon, 24 Aug 2026 09:12:00 GMT":   true,
		"Mon, 24 Aug 2026 04:30:00 +0530": true,
		"2026-08-24T10:15:00Z":            true,
		"2026-08-24":                      true,
		"2026-08-24 10:15:00":             true,
		"":                                false,
		"yesterday":                       false,
		"not a date":                      false,
	}
	for in, wantParsed := range tests {
		got := parseFeedTime(in)
		if got.IsZero() == wantParsed {
			t.Errorf("parseFeedTime(%q) = %v, parsed=%v want parsed=%v", in, got, !got.IsZero(), wantParsed)
		}
	}
}

func TestCleanText(t *testing.T) {
	tests := map[string]string{
		"plain":                          "plain",
		"a &amp; b":                      "a & b",
		"<b>bold</b> text":               "bold text",
		"  collapsed   \n  whitespace  ": "collapsed whitespace",
		"&lt;script&gt;":                 "",
		"":                               "",
	}
	for in, want := range tests {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGoogleNewsFeedURL(t *testing.T) {
	tests := []struct {
		name     string
		symbol   string
		company  string
		contains []string
	}{
		{
			name:   "us symbol with a company name",
			symbol: "AAPL", company: "Apple Inc",
			contains: []string{"Apple+Inc", "gl=US", "ceid=US%3Aen"},
		},
		{
			// Without a company name the bare ticker is ambiguous, so the
			// query is qualified with "stock".
			name:   "unknown company falls back to the ticker",
			symbol: "XYZ", company: "",
			contains: []string{"XYZ+stock"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GoogleNewsFeed(tc.symbol, tc.company)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("feed URL %q does not contain %q", got, want)
				}
			}
		})
	}
}

func TestArticleAge(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		published time.Time
		want      string
	}{
		{published: now.Add(-30 * time.Second), want: "just now"},
		{published: now.Add(-5 * time.Minute), want: "5m ago"},
		{published: now.Add(-3 * time.Hour), want: "3h ago"},
		{published: now.Add(-50 * time.Hour), want: "2d ago"},
		{published: time.Time{}, want: "undated"},
	}
	for _, tc := range tests {
		a := Article{PublishedAt: tc.published}
		if got := a.Age(now); got != tc.want {
			t.Errorf("Age(%v) = %q, want %q", tc.published, got, tc.want)
		}
	}
}

func TestArticleScored(t *testing.T) {
	if (Article{}).Scored() {
		t.Error("a fresh article should not report as scored")
	}
	at := time.Now()
	if !(Article{ScoredAt: &at}).Scored() {
		t.Error("an article with a score time should report as scored")
	}
}

// An official notice can arrive with an empty <link/> element and its whole
// substance in the description. Requiring a URL silently discarded those.
func TestParseFeedKeepsLinklessItems(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Exchange notices</title>
<item><title>Acme Corp</title><link/><description>Significant movement in price has been observed in Acme Corp. The Exchange has sought clarification. |SUBJECT: Price movement</description><pubDate>25-Aug-2026 17:54:00</pubDate></item>
<item><title>No substance at all</title><link/><description></description><pubDate>25-Aug-2026 17:55:00</pubDate></item>
</channel></rss>`)

	items, _, err := ParseFeed(body)
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1: a link-less item with substance is kept, one without is not", len(items))
	}
	if items[0].Title != "Acme Corp" {
		t.Errorf("title = %q", items[0].Title)
	}
	if items[0].URL != "" {
		t.Errorf("URL = %q, want empty", items[0].URL)
	}
	if items[0].Description == "" {
		t.Error("the description carries the whole substance of these items and must survive")
	}
}
