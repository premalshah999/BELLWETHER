package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/research"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// Assembly of the on-demand research scrapers.

// buildResearchEngine assembles the on-demand scrapers.
//
// These are separate from the scheduled catalog on purpose. The catalog exists
// to never miss anything on a fixed beat; this exists to answer a question
// somebody just typed, so it favours breadth and accepts that any individual
// scraper may be slow or rate-limited. Every one of them degrades
// independently.
func buildResearchEngine(cfg *config.Config, store *postgres.DB, archive *postgres.Archive, master *company.Master, router *marketdata.Router, log *slog.Logger) *research.Engine {
	// The same transport reasoning as the ingestion engine: Go's default TLS
	// handshake timeout of ten seconds is shorter than some of these
	// endpoints take to negotiate at all. GDELT measured about 25 seconds
	// from this host and failed every single research call on the handshake,
	// which reads as "GDELT is down" rather than "our client gives up early".
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 45 * time.Second,
			ExpectContinueTimeout: 2 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   4,
			ForceAttemptHTTP2:     true,
		},
	}

	client.Transport = &research.DiscoveryTransport{Base: client.Transport}

	scrapers := []research.Scraper{
		// Our own archive first. It is the only source built for this
		// domain -- filings resolved to symbols, deduplicated
		// and classified -- and the only one that costs nothing and answers in
		// milliseconds.
		&research.LocalScraper{Store: store, Window: 45 * 24 * time.Hour},

		// Discovery, scoped to the US edition: a foreign news index is where a
		// research answer picks up a company this app cannot price.
		&research.GoogleNewsScraper{Client: client},
		// Bing's news index: a second route to the open web that does not
		// depend on the SearXNG node.
		&research.BingNewsScraper{Client: client},
		// GDELT's sourcecountry takes a country name, not a two-letter
		// code, as one token with no internal space -- see the comment on
		// gdelt-us-business in internal/news/catalog.go for what was and
		// was not confirmed live.
		&research.GDELTScraper{Client: client, Country: "unitedstates", Timespan: "7d"},

		// Publishers with no search endpoint, reached by scoping discovery to
		// their domain. Several also have direct feeds in the catalogue; a
		// research question needs their archive, which this reaches.
		&research.PublisherScraper{Client: client, Domain: "reuters.com", Label: "reuters", TrustLevel: news.TrustWire},
		&research.PublisherScraper{Client: client, Domain: "bloomberg.com", Label: "bloomberg", TrustLevel: news.TrustWire},
		&research.PublisherScraper{Client: client, Domain: "apnews.com", Label: "ap", TrustLevel: news.TrustWire},
		&research.PublisherScraper{Client: client, Domain: "wsj.com", Label: "wsj", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "ft.com", Label: "ft", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "cnbc.com", Label: "cnbc", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "marketwatch.com", Label: "marketwatch", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "barrons.com", Label: "barrons", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "fortune.com", Label: "fortune", TrustLevel: news.TrustMajorFin},
		// Specialist rather than major: a useful archive on individual US
		// names, and openly a mix of staff reporting and contributor pieces,
		// which is what the lower trust records.
		&research.PublisherScraper{Client: client, Domain: "seekingalpha.com", Label: "seeking_alpha", TrustLevel: news.TrustSpecialist},
		&research.PublisherScraper{Client: client, Domain: "investors.com", Label: "ibd", TrustLevel: news.TrustSpecialist},
	}

	// The Federal Register: every proposed and final rule, executive order
	// and agency notice the US government publishes, free and keyless --
	// unrelated to SEC and so not gated on SEC_USER_AGENT.
	scrapers = append(scrapers, &research.FederalRegisterScraper{Client: client})

	officialFeeds := append(news.AdditionalOfficialSources(),
		news.ContactGatedOfficialSources(cfg.SECUserAgent)...)
	for _, src := range officialFeeds {
		scrapers = append(scrapers, &research.FeedScraper{Source: src, Client: client})
	}

	// SEC's own text, searched directly -- not a headline about a filing,
	// the filing itself. Gated on the same declared contact every other SEC
	// endpoint in this app requires; omitted rather than registered to fail
	// every call when SEC_USER_AGENT is unset.
	if cfg.SECUserAgent != "" {
		scrapers = append(scrapers, &research.SECFullTextScraper{Client: client, UserAgent: cfg.SECUserAgent})
	} else {
		log.Warn("SEC_USER_AGENT is not set; the SEC full-text research scraper is disabled")
	}

	// The private metasearch node, when one is running. Two configurations
	// of it rather than one: the news category is narrow and fresh, the
	// general web reaches sector reports, regulator pages and primary
	// documents that never appear in a news index. Asking both is cheap —
	// there is no per-query cost — and they return materially different
	// material for the same question.
	if u := cfg.SearXNGURL; u != "" {
		scrapers = append(scrapers,
			// Paged more deeply than the other scrapers, because these are
			// the results that can actually be read. SearXNG returns the
			// publisher's own URL; Google News returns an opaque token that
			// resolves to a JavaScript shim, so a research question that
			// leans on Google News gets forty headlines and no article text.
			&research.SearXNGScraper{
				Client: client, BaseURL: u, Label: "searxng_news",
				Categories: "news", TimeRange: "month", Language: "en", Pages: 2,
			},
			&research.SearXNGScraper{
				Client: client, BaseURL: u, Label: "searxng_web",
				Language: "en", Pages: 2,
			},
			// The same index without the recency filter. A question about a
			// company's history, a past regulatory action or a multi-year
			// trend is answered by material the month-bounded news query
			// cannot see at all.
			&research.SearXNGScraper{
				Client: client, BaseURL: u, Label: "searxng_archive",
				Categories: "news", Language: "en", Pages: 2,
			},
		)
	}

	// Entity resolution runs over every finding, so a research result says
	// which listed companies it concerns rather than leaving the reader to
	// spot them. The threshold is high: a wrong symbol on a research answer
	// is a wrong answer.
	resolve := func(text string) []string {
		matches := master.ResolveAbove(text, 0.9)
		out := make([]string, 0, len(matches))
		for _, m := range matches {
			out = append(out, m.Symbol)
		}
		return out
	}

	// A question naming a sector gets that sector's largest members: no
	// article enumerates the banks, and without this "which companies" is
	// answered only by whichever ones the retrieved articles happened to name.
	universe := func(query string) []research.UniverseNote {
		var out []research.UniverseNote
		for _, sector := range master.SectorsMentioned(query) {
			if symbols := master.SymbolsInSector(sector); len(symbols) > 0 {
				out = append(out, research.UniverseNote{Industry: sector, Symbols: symbols[:min(40, len(symbols))]})
			}
		}
		return out
	}

	articleFetcher := research.NewArticleFetcher()
	articleFetcher.UserAgent = cfg.SECUserAgent
	engine := research.NewEngine(scrapers,
		research.WithLogger(log),
		research.WithSymbolResolver(resolve),
		research.WithUniverseLookup(universe),
		// Historical and statistical questions are answered with arithmetic
		// over the price series rather than from prose about it. Routed
		// through the market data router so research inherits its provider
		// fallback, cache and single-flight rather than opening a second path
		// to the same upstreams.
		research.WithPrices(research.RouterPrices{Router: router}),
		// Price history tied to the news, insider trading and the calendar:
		// how the stock behaved, what moved it, and how it reacts.
		research.WithAnalysis(analysisDeps(store, archive, master)),
		// Reads the pages behind the results rather than working from their
		// headlines. This is the difference between a report and a list of
		// links: without it the model sees a title and a forty-word snippet,
		// and for Google News results the snippet is the title again.
		research.WithArticleFetcher(articleFetcher),
		// Fundamentals, already compared against the peer group. A question
		// about a company is incomplete without whether it is expensive, and
		// no amount of news coverage answers that.
		research.WithValuations(storeValuations{DB: store}))
	log.Info("research engine ready", "scrapers", len(scrapers), "prices", router != nil)
	return engine
}

// analysisDeps connects research's price analysis to the archive.
func analysisDeps(store *postgres.DB, archive *postgres.Archive, master *company.Master) research.AnalysisDeps {
	return research.AnalysisDeps{
		Name: func(symbol string) string {
			if c, ok := master.Lookup(symbol); ok {
				return news.ShortName(c.Name)
			}
			return ""
		},
		// Every event in the window, a page at a time. One page of the newest
		// 500 covered only the last week for a busy name, so every kind of
		// news was measured on the same few sessions.
		Events: func(ctx context.Context, symbol string, since time.Time) ([]research.EventRef, error) {
			const page, most = 500, 6000
			var out []research.EventRef
			for offset := 0; offset < most; offset += page {
				f := postgres.EventFilter{Symbol: symbol, Since: since, Limit: page, Offset: offset, IncludeUnattributedWatchlist: true}
				var (
					list []news.Event
					err  error
				)
				if archive != nil {
					list, err = archive.ListEvents(ctx, f, time.Now())
				} else {
					list, err = store.ListEvents(ctx, f)
				}
				if err != nil {
					return nil, err
				}
				for _, e := range list {
					imp := 0
					if e.Importance != nil {
						imp = *e.Importance
					}
					out = append(out, research.EventRef{ID: e.ID, Headline: e.Headline, Type: e.Type,
						Importance: imp, DiscoveredAt: e.DiscoveredAt})
				}
				if len(list) < page {
					break
				}
			}
			return out, nil
		},
		Earnings: func(ctx context.Context, symbol string) ([]research.EarningsRef, error) {
			list, err := store.SymbolEarnings(ctx, symbol, 40)
			if err != nil {
				return nil, err
			}
			out := make([]research.EarningsRef, 0, len(list))
			for _, e := range list {
				out = append(out, research.EarningsRef{AnnouncedAt: e.AnnouncedAt, SurprisePct: e.SurprisePct})
			}
			return out, nil
		},
		// One reference per Form 4 and direction: a filing reporting twelve
		// sales lines is one decision.
		Insiders: func(ctx context.Context, symbol string, since time.Time) ([]research.InsiderRef, error) {
			trades, err := store.ListInsiderTrades(ctx, postgres.InsiderFilter{Symbol: symbol, Since: since, Market: true, Limit: 1000})
			if err != nil {
				return nil, err
			}
			byFiling := map[string]*research.InsiderRef{}
			var order []string
			for _, t := range trades {
				if t.Value == nil {
					continue
				}
				key := t.Accession + "/" + t.Code
				ref := byFiling[key]
				if ref == nil {
					ref = &research.InsiderRef{FiledAt: t.FiledAt, Buy: t.Code == "P", Planned: true, Who: t.OwnerName + " (" + t.Role() + ")"}
					byFiling[key] = ref
					order = append(order, key)
				}
				ref.Value += *t.Value
				ref.Planned = ref.Planned && t.Plan105b1
			}
			out := make([]research.InsiderRef, 0, len(order))
			for _, k := range order {
				out = append(out, *byFiling[k])
			}
			return out, nil
		},
		SmartMoney: func(ctx context.Context, symbol string) (*research.SmartMoneyRef, error) {
			trades, err := store.ListInsiderTrades(ctx, postgres.InsiderFilter{
				Symbol: symbol, Since: time.Now().AddDate(0, -6, 0), Market: true, Limit: 500,
			})
			if err != nil {
				return nil, err
			}
			ref := &research.SmartMoneyRef{}
			seenBuyer, seenSeller := map[string]bool{}, map[string]bool{}
			for _, t := range trades {
				v := 0.0
				if t.Value != nil {
					v = *t.Value
				}
				if t.Code == "P" {
					ref.InsiderBuyValue += v
					if !seenBuyer[t.OwnerName] && len(ref.InsiderBuyers) < 5 {
						ref.InsiderBuyers = append(ref.InsiderBuyers, t.OwnerName+" ("+t.Role()+")")
					}
					seenBuyer[t.OwnerName] = true
				} else if t.Code == "S" {
					ref.InsiderSellValue += v
					if !seenSeller[t.OwnerName] {
						ref.InsiderSellers++
					}
					seenSeller[t.OwnerName] = true
				}
			}
			if moves, err := store.SymbolFundMoves(ctx, symbol); err == nil {
				for _, m := range moves {
					if m.Kind == "held" {
						continue
					}
					ref.FundMoves = append(ref.FundMoves, fmt.Sprintf("%s (%s): %s in %s, now %.1f%% of the portfolio",
						m.Manager, m.FundName, m.Kind, m.PeriodLabel, m.WeightPct))
				}
			}
			if filings, err := store.ListCongressFilings(ctx, postgres.CongressFilingFilter{Symbol: symbol, Limit: 100}); err == nil {
				ref.CongressFilings = len(filings)
			}
			return ref, nil
		},
		Catalyst: func(ctx context.Context, symbol string) (*research.CatalystRef, error) {
			list, err := store.UpcomingCatalysts(ctx, 120*24*time.Hour, []string{symbol}, 1)
			if err != nil || len(list) == 0 || list[0].NextDate == nil {
				return nil, err
			}
			c := list[0]
			ref := &research.CatalystRef{Kind: c.NextKind, Date: c.NextDate.Format("2006-01-02"), EPSMean: c.EPSAverage}
			if c.NextDays != nil {
				ref.InDays = *c.NextDays
			}
			return ref, nil
		},
	}
}

// webNews adapts the research engine's open-web search to what the AI layer
// cites in an explanation.
type webNews struct{ engine *research.Engine }

func (w webNews) News(ctx context.Context, query string, limit int) ([]ai.WebResult, error) {
	found, _, err := w.engine.Web(ctx, query, "news", limit)
	out := make([]ai.WebResult, 0, len(found))
	for _, f := range found {
		out = append(out, ai.WebResult{Title: f.Title, URL: f.URL, Source: f.Publisher, Snippet: f.Snippet})
	}
	return out, err
}
