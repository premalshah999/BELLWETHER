package news

import (
	"net/url"
	"time"
)

// The curated source catalog. Every entry was fetched successfully from the
// deployment host before being added: a feed that loads in a browser may
// still refuse a datacentre IP (Reuters and Bloomberg do), so sources are
// added on evidence rather than on a documented URL.

// catalogUserAgent identifies this app to the few publishers that refuse an
// anonymous client. Honest about what is fetching, not a browser disguise.
const catalogUserAgent = "TradeSys/1.0 (+https://github.com/tradesys/dashboard)"

// GoogleNewsSearch is the Google News RSS URL for a query, US edition.
func GoogleNewsSearch(query string) string {
	params := url.Values{"q": {query}, "hl": {"en-US"}, "gl": {"US"}, "ceid": {"US:en"}}
	return "https://news.google.com/rss/search?" + params.Encode()
}

// press is a publisher's own RSS feed: a canonical URL and a real timestamp,
// preferred over an aggregator wherever the publisher serves this host.
func press(id, name, feed, category, country string, trust int, refresh, timeout time.Duration) Source {
	return Source{
		ID: id, Name: name, URL: feed,
		Method: MethodRSS, Category: category, Country: country, Language: "en",
		Trust: trust, Refresh: refresh, Timeout: timeout,
		Usage: UsageLegalReview, Display: DisplayLinkOnly, Enabled: true,
	}
}

// discovery is a Google News query: a headline and a link to the publisher,
// with the aggregator's timestamp. It reaches event classes and publishers
// no direct feed covers.
func discovery(id, name, query string) Source {
	return Source{
		ID: id, Name: name, URL: GoogleNewsSearch(query),
		Method: MethodGoogleNews, Category: "discovery", Country: "US", Language: "en",
		Trust: TrustWire, Refresh: 10 * time.Minute, Timeout: 20 * time.Second,
		Usage: UsageDiscoveryOnly, Display: DisplayLinkOnly, Enabled: true,
	}
}

// DefaultSources returns the built-in catalog. Cadence follows the value of
// the source: a regulator's release is polled hard, rulemaking is not.
func DefaultSources() []Source {
	const sec20, min5, min10 = 20 * time.Second, 5 * time.Minute, 10 * time.Minute
	var out []Source

	// Regulators and policy bodies: an announcement here reprices a sector
	// before any publisher writes about it.
	for _, f := range []struct{ id, name, url string }{
		{"fed-press", "Federal Reserve Press Releases", "https://www.federalreserve.gov/feeds/press_all.xml"},
		{"whitehouse-actions", "White House Presidential Actions", "https://www.whitehouse.gov/presidential-actions/feed/"},
		{"sec-press", "SEC Press Releases", "https://www.sec.gov/news/pressreleases.rss"},
	} {
		out = append(out, Source{
			ID: f.id, Name: f.name, URL: f.url,
			Method: MethodRSS, Category: "regulatory", Country: "US", Language: "en",
			Trust: TrustOfficial, Refresh: 90 * time.Second, Timeout: sec20,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true,
		})
	}

	// The Federal Register: every rule, proposed rule and notice from every
	// agency. Rulemaking is not urgent, so it polls slowly; its issuing
	// agency is structured metadata (see events.SectorsForAgency).
	out = append(out, Source{
		ID:   "federal-register",
		Name: "Federal Register",
		URL: "https://www.federalregister.gov/api/v1/documents.json?per_page=40&order=newest" +
			"&conditions%5Btype%5D%5B%5D=RULE&conditions%5Btype%5D%5B%5D=PRORULE&conditions%5Btype%5D%5B%5D=NOTICE",
		Method: MethodFederalRegister, Category: "regulatory", Country: "US", Language: "en",
		Trust: TrustOfficial, Refresh: 20 * time.Minute, Timeout: sec20,
		Usage: UsageOfficial, Display: DisplayFull, Enabled: true,
	})

	// Financial media that serve this host directly. The FT is GB: it is a
	// global paper. Seeking Alpha's feed is its breaking-news wire, not its
	// long-form analysis.
	const sec15 = 15 * time.Second
	out = append(out,
		press("cnbc-world-markets", "CNBC World Markets", "https://search.cnbc.com/rs/search/combinedcms/view.xml?partnerId=wrss01&id=15839069", "markets", "US", TrustMajorFin, min5, sec15),
		press("cnbc-finance", "CNBC Finance", "https://search.cnbc.com/rs/search/combinedcms/view.xml?partnerId=wrss01&id=10000664", "finance", "US", TrustMajorFin, min10, sec15),
		press("cnbc-earnings", "CNBC Earnings", "https://search.cnbc.com/rs/search/combinedcms/view.xml?partnerId=wrss01&id=15839135", "results", "US", TrustMajorFin, min10, sec15),
		press("marketwatch-top", "MarketWatch Top Stories", "https://feeds.content.dowjones.io/public/rss/mw_topstories", "markets", "US", TrustMajorFin, min5, sec15),
		press("ft-companies", "Financial Times Companies", "https://www.ft.com/companies?format=rss", "companies", "GB", TrustMajorFin, min10, sec15),
		press("yahoo-finance", "Yahoo Finance", "https://finance.yahoo.com/news/rssindex", "markets", "US", TrustMajorFin, min5, sec15),
		press("nasdaq-markets", "Nasdaq Markets", "https://www.nasdaq.com/feed/rssoutbound?category=Markets", "markets", "US", TrustMajorFin, min5, sec15),
		press("investing-com", "Investing.com News", "https://www.investing.com/rss/news.rss", "markets", "US", TrustMajorFin, min10, sec15),
		press("seeking-alpha", "Seeking Alpha Market Currents", "https://seekingalpha.com/market_currents.xml", "markets", "US", TrustMajorFin, min5, sec15),
		press("wsj-markets", "Wall Street Journal Markets", "https://feeds.content.dowjones.io/public/rss/RSSMarketsMain", "markets", "US", TrustMajorFin, min5, sec20),
		press("fox-business", "Fox Business", "https://moxie.foxbusiness.com/google-publisher/latest.xml", "markets", "US", TrustGeneric, min10, sec20),
		press("business-insider", "Business Insider", "https://www.businessinsider.com/rss", "markets", "US", TrustGeneric, 15*time.Minute, sec20),
	)

	// Press-release wires: the company speaking for itself, hours to days
	// before the 8-K. TrustCompanyIR, not official: it is the issuer's chosen
	// framing. GlobeNewswire drops a request with no User-Agent.
	globe := press("globenewswire-public", "GlobeNewswire Public Companies",
		"https://www.globenewswire.com/RssFeed/orgclass/1/feedTitle/GlobeNewswire%20-%20News%20about%20Public%20Companies",
		"companies", "US", TrustCompanyIR, min5, sec20)
	globe.UserAgent = catalogUserAgent
	out = append(out, globe,
		press("businesswire-tech", "Business Wire Technology", "https://feed.businesswire.com/rss/home/?rss=G1QFDERJXkJeGVtRWA==", "companies", "US", TrustCompanyIR, min10, sec20))

	// Event classes, each query measured against Google News before it was
	// added; and the wires whose own feeds refuse a datacentre IP.
	out = append(out,
		discovery("disc-us-block-trades", "Block Trades & Secondary Offerings (via discovery)",
			`US company "block trade" OR "secondary offering" OR "share sale" shares when:3d`),
		discovery("disc-us-rating-actions", "Credit Rating Actions (via discovery)",
			`Moody's OR S&P OR Fitch rating downgrade OR upgrade company when:3d`),
		discovery("disc-us-buybacks", "Buybacks (via discovery)",
			`US company "share buyback" OR "stock repurchase" announces when:3d`),
		// Both directions: a raise moves a price as much as a cut.
		discovery("disc-us-guidance", "Guidance Changes (via discovery)",
			`US company cuts OR lowers OR raises guidance forecast when:2d`),
		discovery("disc-us-leadership", "Leadership Changes (via discovery)",
			`US company CEO OR CFO resigns OR steps down OR appointed when:2d`),
		// The announcement, which precedes the 8-K by hours to days.
		discovery("disc-us-ma", "Mergers & Acquisitions (via discovery)",
			`US company to acquire OR merger OR "definitive agreement" when:2d`),
		discovery("disc-us-antitrust", "Antitrust & DOJ Actions (via discovery)",
			`"Justice Department" antitrust lawsuit OR sues OR "blocks" company when:7d`),
		discovery("reuters-business", "Reuters Business (via discovery)", `site:reuters.com business markets when:1d`),
		discovery("bloomberg-markets", "Bloomberg Markets (via discovery)", `site:bloomberg.com markets when:1d`),
		discovery("prnewswire", "PR Newswire (via discovery)", `site:prnewswire.com when:1d`),
	)

	// GDELT: not breaking news but "what are the direct feeds missing", so it
	// polls slowly at aggregator trust. The country filter is the uppercase
	// FIPS code (sourcecountry:US); "us" and "unitedstates" both parse and
	// match nothing. Check GDELT's LOOKUP-COUNTRIES.TXT before changing it.
	out = append(out, Source{
		ID: "gdelt-us-business", Name: "GDELT US Business",
		URL:    "https://api.gdeltproject.org/api/v2/doc/doc?query=(stocks%20OR%20shares%20OR%20earnings)%20sourcecountry:US&mode=ArtList&format=json&maxrecords=75&timespan=60min",
		Method: MethodGDELT, Category: "discovery", Country: "US", Language: "en",
		Trust: TrustAggregator, Refresh: 30 * time.Minute, Timeout: 90 * time.Second,
		Usage: UsagePublicReviewed, Display: DisplayLinkOnly, Enabled: true,
	})

	// The keyless official feeds. Those needing a declared contact are added
	// by the caller that has it: see buildRegistry in cmd/tradesys.
	return append(out, AdditionalOfficialSources()...)
}

// DefaultRegistry builds a registry from the curated catalog.
func DefaultRegistry() (*Registry, error) { return NewRegistry(DefaultSources()...) }

// secFeedTimeout is long because browse-edgar's response time varies wildly
// under load (1s to over 60s from this host, with TLS done in 30ms): at
// twenty seconds most polls failed and the SEC feeds sat permanently
// degraded. Still inside the 8-K feed's two-minute refresh.
const secFeedTimeout = 60 * time.Second

// SECSources returns the EDGAR filings tape, which is as official as an
// exchange disclosure: the filer states these facts under the securities
// laws. userAgent is the contact SEC's fair-access policy requires; with an
// empty one every request is refused, so callers must not register these.
func SECSources(userAgent string) []Source {
	var out []Source
	for _, f := range []struct {
		id, name, form string
		refresh        time.Duration
	}{
		{"sec-8k", "SEC 8-K Current Filings", "8-K", 2 * time.Minute},          // material events: polled hardest
		{"sec-form4", "SEC Form 4 Insider Transactions", "4", 5 * time.Minute}, // insider trades, within minutes
		{"sec-13f", "SEC 13F Institutional Holdings", "13F", 15 * time.Minute}, // quarterly by rule
	} {
		out = append(out, Source{
			ID: f.id, Name: f.name,
			URL:    "https://www.sec.gov/cgi-bin/browse-edgar?action=getcurrent&type=" + f.form + "&company=&dateb=&owner=include&count=40&output=atom",
			Method: MethodSECFiling, Category: "filings",
			Country: "US", Language: "en", UserAgent: userAgent,
			Trust: TrustOfficial, Refresh: f.refresh, Timeout: secFeedTimeout,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true,
		})
	}
	return out
}
