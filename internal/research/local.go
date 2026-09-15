package research

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// EventSearcher is the archive this scraper reads.
//
// Declared as the narrow operation actually needed rather than as the storage
// package's general filter type. That is not only tidier — importing storage
// here would close a cycle, because storage imports ai and ai imports this
// package.
type EventSearcher interface {
	SearchEvents(ctx context.Context, query string, since time.Time, limit int) ([]news.Event, error)
}

// LocalScraper searches the events this system has already collected.
//
// Every other provider goes out to the open web and comes back with whatever
// a general-purpose index thinks is relevant. This one searches an archive
// built for exactly this domain: exchange filings resolved to NSE symbols,
// deduplicated across outlets, with importance and direction attached and a
// trustworthy discovery time on every row.
//
// It is also the only provider that can answer "what have we seen about this"
// rather than "what does the web say", and the only one that costs nothing and
// returns in milliseconds. When a question concerns an Indian listed company,
// this is usually the best material available.
type LocalScraper struct {
	Store EventSearcher
	// Window bounds how far back to look. Research questions are usually
	// about the present; an unbounded search would surface a filing from
	// March against a question about this week.
	Window time.Duration
}

func (l *LocalScraper) Name() string { return "collected" }

// Trust reflects what this archive mostly contains. Exchange filings dominate
// it, and those are ground truth — but the archive also holds ordinary press,
// so the tier is set for the mixture rather than the best case. Per-finding
// trust is raised below for anything an exchange confirmed.
func (l *LocalScraper) Trust() int { return news.TrustMajorFin }

func (l *LocalScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	if l.Store == nil {
		return nil, fmt.Errorf("collected: no event store")
	}
	if limit <= 0 || limit > 40 {
		limit = 15
	}
	window := l.Window
	if window <= 0 {
		window = 45 * 24 * time.Hour
	}

	events, err := l.Store.SearchEvents(ctx, query, time.Now().Add(-window), limit)
	if err != nil {
		return nil, fmt.Errorf("collected: %w", err)
	}

	out := make([]Finding, 0, len(events))
	for _, e := range events {
		trust := l.Trust()
		if e.Official {
			// An exchange filing in the archive is the event itself, not
			// coverage of it, and outranks anything the web returns.
			trust = news.TrustOfficial
		}

		// The symbols are already resolved against the listed master, so
		// they are far more reliable than re-resolving from the headline.
		symbols := make([]string, 0, len(e.Entities))
		for _, ent := range e.Entities {
			symbols = append(symbols, ent.Symbol)
		}

		snippet := e.Summary
		if snippet == e.Headline {
			snippet = ""
		}
		// The classification is worth handing to the model: it says what kind
		// of event this is and how important the pipeline judged it, which
		// the raw headline does not.
		meta := strings.ToLower(strings.ReplaceAll(e.Type, "_", " "))
		if e.Importance != nil {
			meta = fmt.Sprintf("%s, importance %d/10", meta, *e.Importance)
		}
		if e.SourceCount > 1 {
			meta = fmt.Sprintf("%s, %d sources", meta, e.SourceCount)
		}
		if snippet == "" {
			snippet = meta
		} else {
			snippet = meta + ". " + snippet
		}

		published := e.PublishedAt
		if published.IsZero() {
			published = e.DiscoveredAt
		}
		out = append(out, Finding{
			Title:       e.Headline,
			URL:         e.PrimaryURL,
			Publisher:   publisherFor(e),
			Snippet:     snippet,
			PublishedAt: published,
			Scraper:     l.Name(),
			Trust:       trust,
			Symbols:     symbols,
		})
	}
	return out, nil
}

func publisherFor(e news.Event) string {
	if e.Official {
		return "exchange filing"
	}
	if len(e.Evidence) > 0 && e.Evidence[0].Publisher != "" {
		return e.Evidence[0].Publisher
	}
	return "collected"
}
