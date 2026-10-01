package research

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
)

// WebRef is a headline a dated news search found for a big day the archive
// does not explain. It is retrieved now, so it can explain a move after the
// fact; it is never used to measure a reaction.
type WebRef struct {
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Publisher   string    `json:"publisher"`
	PublishedAt time.Time `json:"published_at"`
}

// datedSearcher is the scraper a dated search goes through: Google News,
// whose search honours after: and before: and returns that day's coverage.
func (e *Engine) datedSearcher() Scraper {
	for _, sc := range e.scrapers {
		if sc.Name() == "google_news" {
			return sc
		}
	}
	return nil
}

// explainingWords mark a headline that gives a reason rather than only
// reporting the move.
var explainingWords = []string{"why", "because", " after ", " as ", " on ", "amid", "following", "despite",
	"earnings", "results", "guidance", "outlook", "downgrade", "upgrade", "deal", "lawsuit", "probe",
	"tariff", "forecast", "warns", "cut", "raise", "launch", "approval", "ruling", "acquire", "merger"}

// datedHeadlines asks what was published about a company on one trading
// day, and keeps the two headlines that most look like an explanation.
func datedHeadlines(ctx context.Context, sc Scraper, name, ticker string, day time.Time) []WebRef {
	d := day.In(marketdata.Market)
	query := fmt.Sprintf("%q stock after:%s before:%s", name,
		d.AddDate(0, 0, -1).Format("2006-01-02"), d.AddDate(0, 0, 1).Format("2006-01-02"))
	found, err := sc.Search(ctx, query, 40)
	if err != nil {
		return nil
	}
	// The previous evening through the end of the day: news after the
	// prior close moved this session, and stories explaining the move run
	// that afternoon and evening.
	from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, marketdata.Market).Add(-8 * time.Hour)
	to := time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, marketdata.Market)
	names := nameForms(name)
	type scored struct {
		ref   WebRef
		score int
	}
	var keep []scored
	seen := map[string]bool{}
	for _, f := range found {
		if f.PublishedAt.Before(from) || f.PublishedAt.After(to) || news.OutsideUSMarket(f.URL, f.Publisher) {
			continue
		}
		title := f.Title
		if i := strings.LastIndex(title, " - "); i > 0 {
			title = title[:i]
		}
		lt := " " + strings.ToLower(title) + " "
		if !mentionsCompany(lt, names, ticker) {
			continue
		}
		if seen[lt] {
			continue
		}
		seen[lt] = true
		s := 0
		for _, w := range explainingWords {
			if strings.Contains(lt, w) {
				s++
			}
		}
		keep = append(keep, scored{WebRef{Title: title, URL: f.URL, Publisher: f.Publisher, PublishedAt: f.PublishedAt}, s})
	}
	sort.SliceStable(keep, func(i, j int) bool {
		if keep[i].score != keep[j].score {
			return keep[i].score > keep[j].score
		}
		return keep[i].ref.PublishedAt.Before(keep[j].ref.PublishedAt)
	})
	out := make([]WebRef, 0, 2)
	for _, k := range keep[:min(2, len(keep))] {
		out = append(out, k.ref)
	}
	return out
}

// explainFromWeb fills in coverage for the big days nothing on record
// explains, a few searches at a time and within a fixed budget: this is an
// enrichment and must not hold up the answer.
func (e *Engine) explainFromWeb(ctx context.Context, a *Analysis, name string) {
	sc := e.datedSearcher()
	if sc == nil || name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i := range a.BigMoves {
		m := &a.BigMoves[i]
		if m.Explained() {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", m.Date, marketdata.Market)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			m.Web = datedHeadlines(ctx, sc, name, a.Symbol, day)
		}()
	}
	wg.Wait()
}

// genericFirstWords cannot stand for a company on their own: "Bank" is not
// Bank of America, nor "General" General Motors.
var genericFirstWords = map[string]bool{"bank": true, "first": true, "general": true, "american": true,
	"united": true, "national": true, "global": true, "international": true, "energy": true, "the": true,
	"new": true, "west": true, "east": true, "south": true, "north": true, "southern": true, "western": true,
	"royal": true, "capital": true, "digital": true, "health": true, "texas": true, "pacific": true}

// nameForms are the ways a headline may name a company: its short name, and
// its first word where that word is distinctive ("Meta" for Meta Platforms).
func nameForms(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	out := []string{name}
	if f := strings.Fields(name); len(f) > 1 && len(f[0]) >= 4 && !genericFirstWords[f[0]] {
		out = append(out, f[0])
	}
	return out
}

func mentionsCompany(title string, names []string, ticker string) bool {
	for _, n := range names {
		if strings.Contains(title, n) {
			return true
		}
	}
	return len(ticker) >= 2 && wordSet(title)[strings.ToLower(ticker)]
}
