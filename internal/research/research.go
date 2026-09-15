// Package research runs on-demand searches across live sources.
//
// It is deliberately separate from the scheduled ingestion engine. That engine
// polls a fixed catalog on a fixed cadence and is optimised for never missing
// anything; this one answers a question the operator just asked, and is
// optimised for breadth right now. They share the source registry's idea of
// trust, and nothing else.
package research

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// Finding is one document a scraper returned.
type Finding struct {
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Publisher   string    `json:"publisher"`
	Snippet     string    `json:"snippet,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	Scraper     string    `json:"scraper"`
	Trust       int       `json:"trust"`
	// Symbols are the instruments resolved from the title and snippet.
	Symbols []string `json:"symbols,omitempty"`
	// Body is the article's readable text, when it could be fetched.
	//
	// Not serialised to the client: the source catalog's discovery-only policy
	// allows us to read a page and link to it, not to redisplay it. The model
	// reads this; the reader gets the headline and the link.
	Body string `json:"-"`
	// Words is the length of Body, which the UI does surface so a reader can
	// see which sources were actually read rather than merely listed.
	Words int `json:"words,omitempty"`
}

// Scraper fetches results for a free-text query.
//
// Implementations must be safe to call concurrently and must respect the
// context deadline: a research request is something a person is waiting on,
// so a slow scraper has to be abandoned rather than allowed to hold up the
// answer from the others.
type Scraper interface {
	// Name identifies the scraper in results and in errors.
	Name() string
	// Search returns findings for a query.
	Search(ctx context.Context, query string, limit int) ([]Finding, error)
	// Trust is the baseline credibility of what this scraper returns.
	Trust() int
}

// Result is the outcome of one research request.
type Result struct {
	Query    string    `json:"query"`
	Findings []Finding `json:"findings"`
	// Universe is what the listed master knows about the subject: the
	// companies in an industry the question named. It is context rather than
	// a source, and is kept separate from Findings for that reason — it has
	// no URL and nothing to cite.
	//
	// It exists because a question like "which Indian cement companies are
	// exposed to the coal price" has two halves, and the web only answers
	// one. No article enumerates the listed cement companies; our own
	// industry mapping does, for 752 of them.
	// Measurements are computed from price history rather than read from a
	// page. They carry no URL because there is nothing to cite: they are
	// arithmetic over the price series, and a reader checks them by
	// recomputing rather than by following a link.
	Measurements []MarketStats `json:"measurements,omitempty"`

	// Valuations are what these companies are worth relative to their peers.
	// Price history says how a stock has moved; this says whether it is
	// expensive, which is the other half of any decision to own it.
	Valuations []Valuation `json:"valuations,omitempty"`

	Universe []UniverseNote `json:"universe,omitempty"`
	// Scrapers reports what each scraper contributed, including failures.
	// A research answer that quietly rests on two of five sources is
	// misleading, so the composition travels with the result.
	Scrapers []ScraperReport `json:"scrapers"`
	Symbols  []string        `json:"symbols,omitempty"`
	Elapsed  string          `json:"elapsed"`
}

// UniverseNote is what the listed master knows about an industry.
type UniverseNote struct {
	Industry string   `json:"industry"`
	Symbols  []string `json:"symbols"`
}

// ScraperReport is one scraper's contribution.
type ScraperReport struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Error   string `json:"error,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

// company_Match mirrors the resolver's match type structurally, so that the
// dependency stays one-way.
type company_Match = struct {
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Method     string  `json:"method"`
	Matched    string  `json:"matched"`
}

// Engine runs scrapers concurrently and merges what they return.
type Engine struct {
	scrapers []Scraper
	log      *slog.Logger
	resolve  func(text string) []string
	// universe answers "which listed companies are in this industry".
	universe func(query string) []UniverseNote
	// prices supplies the daily bars behind measured statistics. Optional:
	// without it, research still answers from text, which is what it did
	// before and is better than refusing.
	prices PriceSource
	// articles reads the pages behind the findings. Optional: without it the
	// engine works from headlines, which is what it did before and is far
	// thinner.
	articles *ArticleFetcher
	// valuation answers "is this expensive", which price history cannot.
	valuation ValuationSource
}

// Option configures an Engine.
type Option func(*Engine)

func WithLogger(l *slog.Logger) Option { return func(e *Engine) { e.log = l } }

// WithSymbolResolver supplies the function that names companies in text.
func WithSymbolResolver(f func(text string) []string) Option {
	return func(e *Engine) { e.resolve = f }
}

// WithValuations supplies the fundamentals behind a research answer.
func WithValuations(v ValuationSource) Option {
	return func(e *Engine) { e.valuation = v }
}

// WithArticleFetcher enables reading the text of retrieved pages.
func WithArticleFetcher(f *ArticleFetcher) Option {
	return func(e *Engine) { e.articles = f }
}

// WithPrices supplies the price history behind measured statistics.
func WithPrices(p PriceSource) Option {
	return func(e *Engine) { e.prices = p }
}

// WithUniverseLookup supplies the function that enumerates an industry.
func WithUniverseLookup(f func(query string) []UniverseNote) Option {
	return func(e *Engine) { e.universe = f }
}

// NewEngine builds a research engine over a set of scrapers.
func NewEngine(scrapers []Scraper, opts ...Option) *Engine {
	e := &Engine{scrapers: scrapers, log: slog.Default()}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Scrapers lists the configured scraper names.
func (e *Engine) Scrapers() []string {
	out := make([]string, 0, len(e.scrapers))
	for _, s := range e.scrapers {
		out = append(out, s.Name())
	}
	return out
}

// Search runs every scraper and merges the results.
//
// Scrapers run in parallel and are individually recoverable: one returning an
// error, or nothing, degrades the answer rather than failing it. The alternative
// — failing the request when any source is down — would make the feature
// unavailable whenever any one of several public services had a bad minute.
func (e *Engine) Search(ctx context.Context, query string, perScraper int) (Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Result{}, fmt.Errorf("research: empty query")
	}
	if perScraper <= 0 {
		// Raised alongside article reading. When a source was a headline,
		// twelve per scraper was plenty; now that the useful ones are read in
		// full, the binding constraint is how many readable pages a question
		// can gather, and most scrapers return far fewer than they are asked
		// for.
		perScraper = 30
	}
	start := time.Now()

	var (
		mu       sync.Mutex
		findings []Finding
		reports  []ScraperReport
		wg       sync.WaitGroup
	)
	for _, sc := range e.scrapers {
		wg.Add(1)
		go func(sc Scraper) {
			defer wg.Done()
			got, err := sc.Search(ctx, query, perScraper)
			mu.Lock()
			defer mu.Unlock()
			rep := ScraperReport{Name: sc.Name(), Count: len(got)}
			if err != nil {
				rep.Error = err.Error()
				rep.Count = 0
			}
			reports = append(reports, rep)
			findings = append(findings, got...)
		}(sc)
	}
	wg.Wait()

	findings = dedupeFindings(findings)
	symbolSet := map[string]bool{}
	if e.resolve != nil {
		for i := range findings {
			text := findings[i].Title
			if findings[i].Snippet != "" {
				text += ". " + findings[i].Snippet
			}
			findings[i].Symbols = e.resolve(text)
			for _, s := range findings[i].Symbols {
				symbolSet[s] = true
			}
		}
	}

	// Rank by trust first, then recency. Trust leads because the point of
	// research is to find out what is true, and a wire report outranks an
	// aggregator's copy of it however much fresher the copy is.
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Trust != findings[j].Trust {
			return findings[i].Trust > findings[j].Trust
		}
		return findings[i].PublishedAt.After(findings[j].PublishedAt)
	})
	sort.Slice(reports, func(i, j int) bool { return reports[i].Name < reports[j].Name })

	symbols := make([]string, 0, len(symbolSet))
	for s := range symbolSet {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)

	var universe []UniverseNote
	if e.universe != nil {
		universe = e.universe(query)
	}

	// Read the pages, not just their headlines. This is what separates a
	// research answer from a list of links, and it happens before measurement
	// because it is the slower of the two and the one worth waiting for.
	e.readBodies(ctx, findings)

	// Re-ranked once the bodies are in: a source that was read outranks one
	// that was not, and trust orders each group.
	//
	// This matters because the synthesis prompt takes a fixed number of
	// sources from the top. Ranking on trust alone put seventeen Google News
	// headlines and ten MSN shells — none of them readable — ahead of the
	// article text that answers the question, and the report was written from
	// whatever survived the truncation. A headline is a pointer to evidence;
	// the article is the evidence.
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := findings[i].Body != "", findings[j].Body != ""
		if ri != rj {
			return ri
		}
		return findings[i].Trust > findings[j].Trust
	})

	// Measured last, because it needs the resolved symbols — but the question's
	// own subject leads.
	//
	// Ranking by what the articles happened to mention and then truncating is
	// how "how has RELIANCE performed" came to price ADANIPORTS, BANKINDIA and
	// four other names alphabetically ahead of it, and answer that it had no
	// data on the company the reader asked about. What a question names is the
	// thing to measure; what its sources mention is context.
	ranked := e.rankForMeasurement(query, symbols)
	measurements := e.measure(ctx, ranked)
	valuations := e.valuations(ctx, ranked)

	return Result{
		Query: query, Findings: findings, Scrapers: reports, Symbols: symbols,
		Measurements: measurements,
		Valuations:   valuations,
		Universe:     universe,
		Elapsed:      time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

// dedupeFindings collapses the same document arriving from several scrapers.
//
// Keeping the highest-trust copy matters more than keeping the first: the same
// Reuters story reached through an aggregator and through Reuters itself
// should be attributed to Reuters.
func dedupeFindings(in []Finding) []Finding {
	best := map[string]Finding{}
	var order []string
	for _, f := range in {
		key := news.CanonicalURL(f.URL)
		if key == "" {
			key = strings.ToLower(strings.Join(strings.Fields(f.Title), " "))
		}
		if key == "" {
			continue
		}
		prev, seen := best[key]
		if !seen {
			best[key] = f
			order = append(order, key)
			continue
		}
		if f.Trust > prev.Trust || (f.Trust == prev.Trust && len(f.Snippet) > len(prev.Snippet)) {
			best[key] = f
		}
	}
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
}
