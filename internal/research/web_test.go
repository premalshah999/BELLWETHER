package research

import (
	"context"
	"testing"
	"time"
)

type fakeScraper struct {
	name string
	out  []Finding
}

func (f fakeScraper) Name() string { return f.name }
func (f fakeScraper) Trust() int   { return 50 }
func (f fakeScraper) Search(context.Context, string, int) ([]Finding, error) {
	return f.out, nil
}

// Web asks only the open-web scrapers, merges the same story arriving from
// two of them, and lists news newest first with undated results last.
func TestWebSearch(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	e := NewEngine([]Scraper{
		fakeScraper{"searxng_news", []Finding{
			{Title: "Old story", URL: "https://a.example/old", PublishedAt: day(1)},
			{Title: "Undated", URL: "https://a.example/undated"},
		}},
		fakeScraper{"bing_news", []Finding{
			{Title: "New story", URL: "https://b.example/new", PublishedAt: day(20)},
			{Title: "Old story", URL: "https://a.example/old?utm_source=x", PublishedAt: day(1)},
		}},
		fakeScraper{"reuters", []Finding{{Title: "Not a web scraper", URL: "https://reuters.example/x", PublishedAt: day(25)}}},
	})

	got, reports, err := e.Web(context.Background(), "anything", "news", 10)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, f := range got {
		titles = append(titles, f.Title)
	}
	if want := []string{"New story", "Old story", "Undated"}; len(titles) != 3 || titles[0] != want[0] || titles[1] != want[1] || titles[2] != want[2] {
		t.Errorf("titles = %v, want %v", titles, want)
	}
	if len(reports) != 2 {
		t.Errorf("reports = %+v, want one per open-web scraper", reports)
	}
	if _, _, err := e.Web(context.Background(), "anything", "web", 10); err == nil {
		t.Error("kind web with no web scraper configured should report that")
	}
	if _, _, err := e.Web(context.Background(), " ", "news", 10); err == nil {
		t.Error("an empty query should be refused")
	}
}

func TestBingTarget(t *testing.T) {
	wrapped := "http://www.bing.com/news/apiclick.aspx?ref=FexRss&url=https%3a%2f%2fwww.reuters.com%2fmarkets%2fstory&c=1"
	if got := bingTarget(wrapped); got != "https://www.reuters.com/markets/story" {
		t.Errorf("bingTarget = %q", got)
	}
	if got := bingTarget("https://example.com/a"); got != "https://example.com/a" {
		t.Errorf("a direct link must pass through, got %q", got)
	}
}
