package news

import (
	"net/url"
	"time"
)

// This file is the curated source catalog.
//
// Every entry here was fetched successfully from the deployment host before
// being added. That matters more than it sounds: a feed that loads in a
// browser may still refuse a datacentre IP, and several well-known Indian
// and global feeds do exactly that. Sources are added on evidence, not on
// the existence of a documented URL.
//
// Deliberately excluded, having been probed and found unusable from here:
//
//	Moneycontrol       200 OK but zero items in every category feed
//	Financial Express  403 to non-browser clients
//	Zee Business       403 to non-browser clients
//	BSE announcements  API returns {} without a browser session
//	RBI press releases serves HTML, not RSS, at the documented path
//	Reuters direct     404; reachable only through discovery
//
// The BSE and RBI gaps are real and worth closing; see the corporate-feed
// interface, which exists so those can be added without touching this list.

// catalogUserAgent identifies this app to a publisher that refuses
// anonymous clients. The engine sends no User-Agent by default, which most
// feeds accept and a few drop outright; this is the polite minimum for the
// ones that do not, and is deliberately honest about what is fetching
// rather than impersonating a browser.
const catalogUserAgent = "TradeSys/1.0 (+https://github.com/tradesys/dashboard)"

func GoogleNewsSearch(query, hl, gl string) string {
	// A caller that names no locale gets the US one. This defaulted to en-IN
	// when the app was India-first, which meant a query written for US markets
	// was answered out of the Indian news index unless every call site
	// remembered to say otherwise -- and several did not.
	if hl == "" {
		hl, gl = "en-US", "US"
	}
	params := url.Values{
		"q":    {query},
		"hl":   {hl},
		"gl":   {gl},
		"ceid": {gl + ":" + hl[:2]},
	}
	return "https://news.google.com/rss/search?" + params.Encode()
}

// DefaultSources returns the built-in catalog.
//
// Cadence follows the value of the source rather than treating every feed
// alike. An exchange filing is the event itself and is polled hard; a weekly
// shareholding disclosure is not, and polling it every minute would be rude
// to the publisher and useless to the operator.
func DefaultSources() []Source {
	var out []Source

	// The US regulators and policy bodies that play the same role for the US
	// as RBI plays for India: an announcement here reprices a sector, or the
	// whole market, before any publisher writes about it. All three verified
	// live and reachable from this host with a plain RSS/Atom fetch, no key.
	usOfficial := []struct{ id, name, url, category string }{
		{"fed-press", "Federal Reserve Press Releases", "https://www.federalreserve.gov/feeds/press_all.xml", "regulatory"},
		{"whitehouse-actions", "White House Presidential Actions", "https://www.whitehouse.gov/presidential-actions/feed/", "regulatory"},
		{"sec-press", "SEC Press Releases", "https://www.sec.gov/news/pressreleases.rss", "regulatory"},
	}
	for _, f := range usOfficial {
		out = append(out, Source{
			ID: f.id, Name: f.name, URL: f.url,
			Method: MethodRSS, Category: f.category, Country: "US", Language: "en",
			Trust: TrustOfficial, Refresh: 90 * time.Second, Timeout: 20 * time.Second,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true,
		})
	}

	// The Federal Register: every final and proposed rule, and every notice,
	// from every agency at once -- rulemaking is not urgent the way a press
	// release is, so this polls far less often than the sources above, but
	// it is the one source here whose issuing agency is structured metadata
	// (see events.SectorsForAgency) rather than something to be guessed at
	// from the title. Verified live: 10,000+ matching documents on an
	// unfiltered "newest" pull, including live examples from FAA and the
	// Personnel Management Office.
	out = append(out, Source{
		ID:   "federal-register",
		Name: "Federal Register",
		URL: "https://www.federalregister.gov/api/v1/documents.json?per_page=40&order=newest" +
			"&conditions%5Btype%5D%5B%5D=RULE&conditions%5Btype%5D%5B%5D=PRORULE&conditions%5Btype%5D%5B%5D=NOTICE",
		Method: MethodFederalRegister, Category: "regulatory", Country: "US", Language: "en",
		Trust: TrustOfficial, Refresh: 20 * time.Minute, Timeout: 20 * time.Second,
		Usage: UsageOfficial, Display: DisplayFull, Enabled: true,
	})

	// ---- Layer D: global financial media ----------------------------------
	//
	// MarketWatch and the FT both serve us directly from this host, so they
	// are fetched at the source rather than discovered through an aggregator.
	// That is worth preferring wherever it works: a direct feed gives a
	// canonical URL and a real timestamp instead of a redirect and an
	// aggregator's idea of when the story appeared.
	// Country matters here for two reasons that both used to be silently
	// wrong while this block carried none. It sets the market-phase cadence
	// (lane.go throttles by whether a source follows the Indian session),
	// and it is the venue preference the entity resolver uses to settle a
	// name claimed on both venues -- a US desk writing "Infosys" means the
	// ADR, and without a country it means neither and resolves to nothing.
	// The FT is deliberately GB rather than US: it is a global paper, and
	// claiming otherwise to buy a venue hint would be a lie that shows up
	// as a wrong symbol on a headline.
	global := []struct {
		id, name, url, category, country string
		trust                            int
		refresh                          time.Duration
	}{
		{"cnbc-world-markets", "CNBC World Markets", "https://search.cnbc.com/rs/search/combinedcms/view.xml?partnerId=wrss01&id=15839069", "markets", "US", TrustMajorFin, 5 * time.Minute},
		{"cnbc-finance", "CNBC Finance", "https://search.cnbc.com/rs/search/combinedcms/view.xml?partnerId=wrss01&id=10000664", "finance", "US", TrustMajorFin, 10 * time.Minute},
		{"cnbc-earnings", "CNBC Earnings", "https://search.cnbc.com/rs/search/combinedcms/view.xml?partnerId=wrss01&id=15839135", "results", "US", TrustMajorFin, 10 * time.Minute},
		{"marketwatch-top", "MarketWatch Top Stories", "https://feeds.content.dowjones.io/public/rss/mw_topstories", "markets", "US", TrustMajorFin, 5 * time.Minute},
		{"ft-companies", "Financial Times Companies", "https://www.ft.com/companies?format=rss", "companies", "GB", TrustMajorFin, 10 * time.Minute},

		// Restored, having previously been measured out of the catalog (see
		// the removed comment this replaced, still readable in git history).
		// The earlier verdict was correct for what this app was then: over
		// one archive these three produced 682 events, a fifth of
		// everything collected, resolving to an Indian listed company three
		// times in total -- "RSI Alert: Polestar Now Oversold" is real
		// material, just not for an Indian operator's feed. That same
		// material is now exactly on target: this is the primary US retail
		// and market-news layer, the closest thing this catalog has to what
		// NSE announcements and BSE notices are for India.
		{"yahoo-finance", "Yahoo Finance", "https://finance.yahoo.com/news/rssindex", "markets", "US", TrustMajorFin, 5 * time.Minute},
		{"nasdaq-markets", "Nasdaq Markets", "https://www.nasdaq.com/feed/rssoutbound?category=Markets", "markets", "US", TrustMajorFin, 5 * time.Minute},
		{"investing-com", "Investing.com News", "https://www.investing.com/rss/news.rss", "markets", "US", TrustMajorFin, 10 * time.Minute},
		// Seeking Alpha's "Market Currents" is a breaking-news wire (filings,
		// dividend declarations, guidance, M&A) rather than the long-form
		// analysis the site is best known for -- verified live: dividend
		// declarations, a credit-facility raise, an EPA rule change, all
		// inside the same 7-item pull.
		{"seeking-alpha", "Seeking Alpha Market Currents", "https://seekingalpha.com/market_currents.xml", "markets", "US", TrustMajorFin, 5 * time.Minute},
	}

	for _, f := range global {
		out = append(out, Source{
			ID: f.id, Name: f.name, URL: f.url,
			Method: MethodRSS, Category: f.category, Country: f.country, Language: "en",
			Trust: f.trust, Refresh: f.refresh, Timeout: 15 * time.Second,
			Usage: UsageLegalReview, Display: DisplayLinkOnly, Enabled: true,
		})
	}

	// The same event-class approach, for the US. Each query was measured
	// live against Google News before being added, same discipline as the
	// India queries above; the counts in the comments are what a single pull
	// returned.
	usDiscovery := []struct{ id, name, query string }{
		// 53 items: "Oklo Stock Drops After Announcing $1 Billion Share
		// Sale", "Kioxia said to consider raising $10 billion in U.S.
		// listing". A bare "block trade" OR "secondary offering" query
		// returned one item; broadening to "share sale" is what found the
		// rest of this event class.
		{"disc-us-block-trades", "Block Trades & Secondary Offerings (via discovery)",
			`US company "block trade" OR "secondary offering" OR "share sale" shares when:3d`},
		// 36 items: "Moody's upgrades Embraer rating to Baa2 on strong
		// metrics". A rating action is a solvency judgement and reprices
		// equity as well as debt, same reasoning as the CRISIL/ICRA query.
		{"disc-us-rating-actions", "Credit Rating Actions (via discovery)",
			`Moody's OR S&P OR Fitch rating downgrade OR upgrade company when:3d`},
		// 38 items: "Coca-Cola Europacific steps up buybacks in US and UK
		// markets".
		{"disc-us-buybacks", "Buybacks (via discovery)",
			`US company "share buyback" OR "stock repurchase" announces when:3d`},
		// 15 items: "Weather delays and a compressor issue cut 2026 output
		// at Obsidian Energy". Deliberately catches both directions --
		// "raises" and "cuts" -- since a beat is as much a price mover as a
		// miss.
		{"disc-us-guidance", "Guidance Changes (via discovery)",
			`US company cuts OR lowers OR raises guidance forecast when:2d`},
		// 56 items: "Classic Vacations CEO Melissa Krueger steps down".
		{"disc-us-leadership", "Leadership Changes (via discovery)",
			`US company CEO OR CFO resigns OR steps down OR appointed when:2d`},
		// 48 items: "Crane Company Announces Agreement to Acquire U.S. Water
		// Pump Business". M&A is the single highest-value event class this
		// system tracks and the SEC 8-K tape (Item 2.01/1.01) already covers
		// it once a deal is filed; this catches the announcement, which
		// usually precedes the filing by hours to days.
		{"disc-us-ma", "Mergers & Acquisitions (via discovery)",
			`US company to acquire OR merger OR "definitive agreement" when:2d`},
	}
	for _, f := range usDiscovery {
		out = append(out, Source{
			ID: f.id, Name: f.name, URL: GoogleNewsSearch(f.query, "en-US", "US"),
			Method: MethodGoogleNews, Category: "discovery", Country: "US", Language: "en",
			Trust: TrustWire, Refresh: 10 * time.Minute, Timeout: 20 * time.Second,
			Usage: UsageDiscoveryOnly, Display: DisplayLinkOnly, Enabled: true,
		})
	}

	// ---- Layer D2: the US press-release wires ------------------------------
	//
	// The closest thing the US has to what NSE corporate announcements are
	// for India: the company speaking for itself, verbatim, before any
	// journalist has written about it. An 8-K covers the same ground but
	// only once it is filed, which for most announcements is hours to days
	// later -- these carry the announcement itself.
	//
	// Trust is TrustCompanyIR rather than TrustMajorFin for exactly that
	// reason: this is the issuer's own words, not a desk's account of them.
	// It is not TrustOfficial, which stays reserved for an exchange or a
	// regulator -- a press release is the company's chosen framing, and a
	// company may choose to frame badly.
	//
	// Every URL below was pulled live from this host before being added;
	// the item counts in each comment are what a single fetch returned.
	wires := []struct {
		id, name, url, category string
		refresh                 time.Duration
		userAgent               string
	}{
		// 20 items, all issuer announcements: "Home BancShares, Inc.
		// Announces Recognition in Forbes...". This is the public-companies
		// feed specifically, not GlobeNewswire's full firehose, which
		// carries a great deal of private-company and non-market material.
		// Needs a declared User-Agent. The engine sends none by default, and
		// GlobeNewswire drops a UA-less request at the connection rather
		// than answering it -- which Go's HTTP/2 client reports as
		// "stream error: INTERNAL_ERROR; received from peer", a message
		// that says nothing about the cause. Measured: empty UA fails,
		// any non-empty UA returns 200.
		{"globenewswire-public", "GlobeNewswire Public Companies",
			"https://www.globenewswire.com/RssFeed/orgclass/1/feedTitle/GlobeNewswire%20-%20News%20about%20Public%20Companies",
			"companies", 5 * time.Minute, catalogUserAgent},
		// 25 items. Business Wire's public feeds are subject-scoped rather
		// than a single firehose, and this is the technology and networks
		// one -- narrower than ideal, but technology is a large enough share
		// of US market capitalisation that it earns a slot, and no broader
		// Business Wire feed answered from this host.
		{"businesswire-tech", "Business Wire Technology",
			"https://feed.businesswire.com/rss/home/?rss=G1QFDERJXkJeGVtRWA==",
			"companies", 10 * time.Minute, ""},
	}
	for _, f := range wires {
		out = append(out, Source{
			ID: f.id, Name: f.name, URL: f.url,
			Method: MethodRSS, Category: f.category, Country: "US", Language: "en",
			Trust: TrustCompanyIR, Refresh: f.refresh, Timeout: 20 * time.Second,
			Usage: UsageLegalReview, Display: DisplayLinkOnly, Enabled: true,
			UserAgent: f.userAgent,
		})
	}

	// ---- Layer D3: US market desks ----------------------------------------
	//
	// The US equivalent of Layer C's Indian papers, and the layer this
	// catalogue was most obviously missing: before these it carried
	// seventeen Indian mastheads and four US ones, for an app whose default
	// venue is the US.
	usDesks := []struct {
		id, name, url, category string
		trust                   int
		refresh                 time.Duration
	}{
		// 61 items, the deepest single pull of anything probed for this
		// change. Dow Jones serves it directly, so it carries a real
		// publication time rather than an aggregator's sighting.
		{"wsj-markets", "Wall Street Journal Markets",
			"https://feeds.content.dowjones.io/public/rss/RSSMarketsMain", "markets", TrustMajorFin, 5 * time.Minute},
		// 25 items.
		{"fox-business", "Fox Business",
			"https://moxie.foxbusiness.com/google-publisher/latest.xml", "markets", TrustGeneric, 10 * time.Minute},
		// 20 items. Redirects once (301) before serving; the engine follows.
		{"business-insider", "Business Insider",
			"https://www.businessinsider.com/rss", "markets", TrustGeneric, 15 * time.Minute},
	}
	for _, f := range usDesks {
		out = append(out, Source{
			ID: f.id, Name: f.name, URL: f.url,
			Method: MethodRSS, Category: f.category, Country: "US", Language: "en",
			Trust: f.trust, Refresh: f.refresh, Timeout: 20 * time.Second,
			Usage: UsageLegalReview, Display: DisplayLinkOnly, Enabled: true,
		})
	}

	// The two wires whose own feeds refuse a datacentre IP. Reuters and
	// Bloomberg both block direct RSS from hosts like this one, so they are
	// reached the way the discovery layer reaches anything else -- a Google
	// News site: query -- and carry discovery-only terms and an aggregator's
	// timestamp as a result. Worth having anyway: between them they break a
	// large share of what later shows up everywhere else. 100 items each on
	// a live pull.
	blockedWires := []struct{ id, name, query string }{
		{"reuters-business", "Reuters Business (via discovery)", `site:reuters.com business markets when:1d`},
		{"bloomberg-markets", "Bloomberg Markets (via discovery)", `site:bloomberg.com markets when:1d`},
		{"prnewswire", "PR Newswire (via discovery)", `site:prnewswire.com when:1d`},
	}
	for _, f := range blockedWires {
		out = append(out, Source{
			ID: f.id, Name: f.name, URL: GoogleNewsSearch(f.query, "en-US", "US"),
			Method: MethodGoogleNews, Category: "discovery", Country: "US", Language: "en",
			Trust: TrustWire, Refresh: 10 * time.Minute, Timeout: 20 * time.Second,
			Usage: UsageDiscoveryOnly, Display: DisplayLinkOnly, Enabled: true,
		})
	}

	// ---- Layer E: GDELT coverage expansion --------------------------------
	//
	// Not a breaking-news source and not treated as one. Its job is to answer
	// "what are our direct feeds missing?", so it polls slowly and its items
	// carry aggregator-level trust until something better corroborates them.
	// Same query, US-scoped. Two earlier attempts here were both wrong, and
	// production health data is what proved it: "sourcecountry:us"
	// (lowercase code) parses without error but matches nothing (confirmed
	// live), and the next guess, "sourcecountry:unitedstates" (lowercase
	// name with the space stripped -- GDELT's own documented convention for
	// a multi-word country, e.g. "unitedarabemirates"), looked plausible
	// but source_health told a different story after a day in production:
	// 13 genuine HTTP 200 successes, every one of them zero items -- not a
	// rate limit, an empty match, and implausible on its face besides (US
	// financial-news volume should dwarf India's, not read as zero against
	// gdelt-india-business's real flow of items).
	//
	// GDELT's own FIPS country lookup
	// (https://data.gdeltproject.org/api/v2/guides/LOOKUP-COUNTRIES.TXT)
	// lists "US" for United States, and GDELT's own worked example for this
	// exact operator uses it uppercase: sourcecountry:US. That the
	// lowercase code failed but the name-token path also failed suggests
	// the code form is matched case-sensitively against that uppercase
	// table -- unlike a country name, which GDELT's india source proves is
	// matched case-insensitively. If health checks ever show this source
	// persistently empty again, verify against the lookup file above rather
	// than re-guessing a spelling.
	out = append(out, Source{
		ID: "gdelt-us-business", Name: "GDELT US Business",
		URL:    "https://api.gdeltproject.org/api/v2/doc/doc?query=(stocks%20OR%20shares%20OR%20earnings)%20sourcecountry:US&mode=ArtList&format=json&maxrecords=75&timespan=60min",
		Method: MethodGDELT, Category: "discovery", Country: "US", Language: "en",
		Trust: TrustAggregator, Refresh: 30 * time.Minute, Timeout: 90 * time.Second,
		Usage: UsagePublicReviewed, Display: DisplayLinkOnly, Enabled: true,
	})

	// The keyless official feeds. The ones needing a declared contact are
	// added by the caller that has it -- see buildRegistry in cmd/tradesys.
	return append(out, AdditionalOfficialSources()...)
}

// DefaultRegistry builds a registry from the curated catalog.
func DefaultRegistry() (*Registry, error) { return NewRegistry(DefaultSources()...) }

// secFeed builds one SEC EDGAR "current filings" Atom feed URL, filtered to
// one form type.
func secFeed(formType string) string {
	return "https://www.sec.gov/cgi-bin/browse-edgar?action=getcurrent&type=" +
		formType + "&company=&dateb=&owner=include&count=40&output=atom"
}

// SECSources returns the SEC EDGAR filings tape: 8-K (material events), Form
// 4 (insider transactions) and 13F (institutional holdings), all free,
// keyless and verified live from this host. userAgent is the contact string
// SEC's fair-access policy requires (see SECUserAgent in internal/config);
// callers must not register these sources with an empty one, since every
// request would 403.
//
// Ranked alongside the NSE exchange feeds (TrustOfficial): the filer
// declares these facts under penalty of the securities laws, the same
// standing an exchange disclosure has.
func SECSources(userAgent string) []Source {
	filings := []struct {
		id, name, formType string
		refresh            time.Duration
	}{
		// The event itself, and the one worth polling hardest: 8-K covers
		// material agreements, results, control changes, leadership
		// departures -- the same class of thing nse-announcements exists to
		// catch, at the same urgency.
		{"sec-8k", "SEC 8-K Current Filings", "8-K", 2 * time.Minute},
		// Insider transactions. A departure or a large sale is worth knowing
		// about within minutes, not hours; the STOCK Act disclosure delay for
		// members of Congress (Phase 4) is a separate, much slower channel.
		{"sec-form4", "SEC Form 4 Insider Transactions", "4", 5 * time.Minute},
		// Institutional 13F holdings change quarterly by rule, so nothing is
		// lost by checking this only a few times an hour.
		{"sec-13f", "SEC 13F Institutional Holdings", "13F", 15 * time.Minute},
	}
	out := make([]Source, 0, len(filings))
	for _, f := range filings {
		out = append(out, Source{
			ID: f.id, Name: f.name, URL: secFeed(f.formType),
			Method: MethodSECFiling, Category: "filings",
			Country: "US", Language: "en", UserAgent: userAgent,
			Trust: TrustOfficial, Refresh: f.refresh, Timeout: 20 * time.Second,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true,
		})
	}
	return out
}
