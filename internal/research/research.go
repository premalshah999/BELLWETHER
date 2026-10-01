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
	"net/url"
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
	Words      int       `json:"words,omitempty"`
	ReadStatus string    `json:"read_status,omitempty"`
	ReadError  string    `json:"read_error,omitempty"`
	FetchedAt  time.Time `json:"fetched_at,omitempty"`
	Cached     bool      `json:"cached,omitempty"`
	Relevance  float64   `json:"relevance,omitempty"`
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
	// Measurements are computed from price history, so they carry no URL: a
	// reader checks them by recomputing.
	Measurements []MarketStats `json:"measurements,omitempty"`
	// Analyses tie each subject company's price history to the news.
	Analyses []Analysis `json:"analyses,omitempty"`

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
	// analysisDeps enables price-and-news analysis. Optional.
	analysisDeps *AnalysisDeps
	// articles reads the pages behind the findings. Optional: without it the
	// engine works from headlines, which is what it did before and is far
	// thinner.
	articles *ArticleFetcher
	// valuation answers "is this expensive", which price history cannot.
	valuation   ValuationSource
	searchSlots chan struct{}
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
	wrapped := make([]Scraper, 0, len(scrapers))
	for _, sc := range scrapers {
		wrapped = append(wrapped, cacheScraper(sc))
	}
	e := &Engine{scrapers: wrapped, log: slog.Default(), searchSlots: make(chan struct{}, 6)}
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
		perScraper = 12
	}
	if perScraper > 30 {
		perScraper = 30
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	start := time.Now()

	// The open-web scrapers go deeper than the fixed publishers: they are the
	// reach beyond the curated catalogue, and most of what they return can
	// be read in full.
	var web, fixed []Scraper
	for _, sc := range e.scrapers {
		if openWeb[sc.Name()] {
			web = append(web, sc)
		} else {
			fixed = append(fixed, sc)
		}
	}
	var (
		webFindings []Finding
		webReports  []ScraperReport
		wg          sync.WaitGroup
	)
	var (
		officialFound  []Finding
		officialReport *ScraperReport
	)
	wg.Go(func() { webFindings, webReports = e.fanOut(ctx, web, query, max(perScraper, webDepth), 35*time.Second) })
	wg.Go(func() {
		octx, cancel := context.WithTimeout(ctx, 35*time.Second)
		defer cancel()
		officialFound, officialReport = e.officialFindings(octx, query)
	})
	findings, reports := e.fanOut(ctx, fixed, query, perScraper, 35*time.Second)
	wg.Wait()
	findings, reports = append(findings, webFindings...), append(reports, webReports...)
	findings = append(findings, officialFound...)
	if officialReport != nil {
		reports = append(reports, *officialReport)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Name < reports[j].Name })

	findings = dedupeFindings(usMarketOnly(findings))
	var named []string
	if e.resolve != nil {
		named = e.resolve(query)
		for i := range findings {
			text := findings[i].Title
			if findings[i].Snippet != "" {
				text += ". " + findings[i].Snippet
			}
			if len(findings[i].Symbols) == 0 {
				findings[i].Symbols = e.resolve(text)
			}
		}
	}
	rankFindingsFor(query, named, findings)

	var universe []UniverseNote
	if e.universe != nil {
		universe = e.universe(query)
	}

	// Read the pages, not just their headlines. This is what separates a
	// research answer from a list of links, and it happens before measurement
	// because it is the slower of the two and the one worth waiting for.
	reportProgress(ctx, "reading", fmt.Sprintf("Checking publisher access and reading up to %d relevant pages", bodyFetchLimit))
	e.readBodies(ctx, findings)

	rankFindingsFor(query, named, findings)
	findings = relevantOnly(findings)

	// Measured last, and only for what the question is about: the companies
	// it names, or failing that one or two its sources are plainly about.
	//
	// Measuring whatever the articles happened to mention is how "how has
	// XOM performed" came to price AAPL and ABBV ahead of it, and how a
	// question about 401(k) strategy came back with Apple's reaction to news.
	subjects := subjectsOf(named, findings)
	symbols := mentioned(findings, subjects)
	var measurements []MarketStats
	var valuations []Valuation
	var analyses []Analysis
	if len(subjects) > 0 {
		reportProgress(ctx, "measuring", "Computing market context for the companies in this question")
		measurements = e.measure(ctx, subjects)
		valuations = e.valuations(ctx, subjects)
		reportProgress(ctx, "measuring", "Relating price moves to earnings, the news and insider trading")
		analyses = e.analyse(ctx, subjects)
	}

	return Result{
		Query: query, Findings: findings, Scrapers: reports, Symbols: symbols,
		Measurements: measurements,
		Analyses:     analyses,
		Valuations:   valuations,
		Universe:     universe,
		Elapsed:      time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

// fanOut runs scrapers in parallel, each under its own deadline, and
// collects what they return with a report per scraper. One failing or
// returning nothing degrades the answer rather than failing it.
func (e *Engine) fanOut(ctx context.Context, scrapers []Scraper, query string, per int, timeout time.Duration) ([]Finding, []ScraperReport) {
	var (
		mu       sync.Mutex
		findings []Finding
		reports  []ScraperReport
		wg       sync.WaitGroup
	)
	for _, sc := range scrapers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			providerCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			var got []Finding
			var err error
			select {
			case e.searchSlots <- struct{}{}:
				got, err = searchProvider(providerCtx, sc, query, per)
				<-e.searchSlots
			case <-providerCtx.Done():
				err = providerCtx.Err()
			}
			mu.Lock()
			defer mu.Unlock()
			rep := ScraperReport{Name: sc.Name(), Count: len(got)}
			if err != nil {
				rep.Error, rep.Count = err.Error(), 0
			}
			reportProgress(ctx, "searching", fmt.Sprintf("%s: %d results", rep.Name, rep.Count))
			reports = append(reports, rep)
			findings = append(findings, got...)
		}()
	}
	wg.Wait()
	sort.Slice(reports, func(i, j int) bool { return reports[i].Name < reports[j].Name })
	return findings, reports
}

// webScrapers are the scrapers that search the open web rather than a fixed
// publisher or archive, by the kind of result wanted.
var webScrapers = map[string][]string{
	"news": {"searxng_news", "bing_news", "google_news"},
	"web":  {"searxng_web"},
}

// openWeb is every open-web scraper, and webDepth how many results a research
// question asks each of them for.
var openWeb = map[string]bool{
	"searxng_news": true, "searxng_web": true, "searxng_archive": true, "bing_news": true, "google_news": true,
}

const webDepth = 30

// Web searches the open web: recent reporting (kind "news", newest first) or
// pages of any age (kind "web", most relevant first). It is the quick path a
// panel uses -- headlines and links, no page reading or measurement.
func (e *Engine) Web(ctx context.Context, query, kind string, limit int) ([]Finding, []ScraperReport, error) {
	query = strings.TrimSpace(query)
	names, ok := webScrapers[kind]
	if query == "" || !ok {
		return nil, nil, fmt.Errorf("research: web search needs a query and a kind of news or web")
	}
	var scrapers []Scraper
	for _, sc := range e.scrapers {
		for _, n := range names {
			if sc.Name() == n {
				scrapers = append(scrapers, sc)
			}
		}
	}
	if len(scrapers) == 0 {
		return nil, nil, fmt.Errorf("research: no web search provider is configured")
	}
	limit = max(1, min(limit, 40))
	findings, reports := e.fanOut(ctx, scrapers, query, limit, 12*time.Second)
	findings = dedupeFindings(usMarketOnly(findings))
	if kind == "news" {
		// Newest first; an undated result sorts last rather than being
		// assumed recent.
		sort.SliceStable(findings, func(i, j int) bool { return findings[i].PublishedAt.After(findings[j].PublishedAt) })
	} else {
		rankFindings(query, findings)
	}
	if len(findings) > limit {
		findings = findings[:limit]
	}
	if e.resolve != nil {
		for i := range findings {
			if len(findings[i].Symbols) == 0 {
				findings[i].Symbols = e.resolve(findings[i].Title + ". " + findings[i].Snippet)
			}
		}
	}
	return findings, reports, nil
}

// usMarketOnly drops findings from publishers writing for another country's
// investors: their tax rules, savings products and listings do not apply here.
func usMarketOnly(findings []Finding) []Finding {
	out := findings[:0]
	for _, f := range findings {
		if !news.OutsideUSMarket(f.URL, f.Publisher) {
			out = append(out, f)
		}
	}
	return out
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
			if f.Body == "" {
				f.Body, f.Words = prev.Body, prev.Words
			}
			best[key] = f
		} else if prev.Body == "" && f.Body != "" {
			prev.Body, prev.Words = f.Body, f.Words
			best[key] = prev
		}
	}
	// A Google News link is an opaque redirect that cannot be read. When
	// the same headline arrived with the publisher's own address, keep that
	// one and drop the redirect.
	direct := map[string]bool{}
	for _, k := range order {
		if f := best[k]; !isAggregatorLink(f.URL) {
			direct[headlineKey(f.Title)] = true
		}
	}
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		f := best[k]
		if isAggregatorLink(f.URL) && direct[headlineKey(f.Title)] {
			continue
		}
		out = append(out, f)
	}
	return out
}

func isAggregatorLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && unreadableHosts[strings.ToLower(u.Hostname())]
}

// headlineKey is a headline without the " - Publisher" an aggregator adds.
func headlineKey(title string) string {
	if i := strings.LastIndex(title, " - "); i > 0 {
		title = title[:i]
	}
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}
