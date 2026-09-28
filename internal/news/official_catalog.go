package news

import "time"

// AdditionalOfficialSources are keyless feeds verified from the deployment
// host. Publication cadence does not imply unlimited access or redistribution.
func AdditionalOfficialSources() []Source {
	feeds := []struct{ id, name, url, country, category string }{
		{"ecb-press", "ECB releases and speeches", "https://www.ecb.europa.eu/rss/press.html", "EU", "regulatory"},
		{"ecb-statistics", "ECB statistical releases", "https://www.ecb.europa.eu/rss/statpress.html", "EU", "economy"},
		{"boe-news", "Bank of England news", "https://www.bankofengland.co.uk/rss/news", "GB", "regulatory"},
		{"eia-energy", "EIA Today in Energy", "https://www.eia.gov/rss/todayinenergy.xml", "US", "energy"},
		{"ftc-press", "FTC competition and consumer protection", "https://www.ftc.gov/feeds/press-release.xml", "US", "regulatory"},

		// The US agencies whose announcements reprice a sector before any
		// publisher writes about them, and which nothing else in this
		// catalogue reached. Each was pulled live from this host before being
		// added, with the item count it returned:
		//
		//   DOJ    25 items -- merger challenges and antitrust suits. A suit
		//                     against an announced deal is the single most
		//                     direct way a merger arbitrage breaks.
		//   USTR   10 items -- tariff and trade actions, which land on whole
		//                     import-exposed sectors at once.
		//   BEA    48 items -- GDP, personal income, trade balance.
		//   CFTC   10 items -- commodities enforcement and positioning.
		//   BLS     1 item  -- thin, but it is the CPI and payroll release
		//                     feed, and those two numbers move every index.
		//
		// Two were tried and dropped rather than assumed: SEC's litigation
		// feed 404s at every documented path, and Treasury's press feed
		// returns 200 with zero items.
		{"doj-news", "DOJ press releases", "https://www.justice.gov/news/rss?type=press_release", "US", "regulatory"},
		{"ustr-press", "USTR trade actions", "https://ustr.gov/rss.xml", "US", "regulatory"},
		{"bea-news", "BEA economic releases", "https://apps.bea.gov/rss/rss.xml", "US", "economy"},
		{"cftc-press", "CFTC press releases", "https://www.cftc.gov/RSS/RSSGP/rssgp.xml", "US", "regulatory"},
		{"bls-releases", "BLS news releases", "https://www.bls.gov/feed/bls_latest.rss", "US", "economy"},
	}
	out := make([]Source, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, Source{ID: f.id, Name: f.name, URL: f.url, Country: f.country, Category: f.category,
			Method: MethodRSS, Language: "en", Trust: TrustOfficial, Refresh: 5 * time.Minute, Timeout: 15 * time.Second,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true, UserAgent: catalogUserAgent})
	}
	return out
}
