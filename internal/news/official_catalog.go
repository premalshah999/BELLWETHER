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
	}
	out := make([]Source, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, Source{ID: f.id, Name: f.name, URL: f.url, Country: f.country, Category: f.category,
			Method: MethodRSS, Language: "en", Trust: TrustOfficial, Refresh: 5 * time.Minute, Timeout: 15 * time.Second,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true, UserAgent: catalogUserAgent})
	}

	return out
}

// ContactGatedOfficialSources are official feeds that require a declared
// contact address in the User-Agent, in the SEC_USER_AGENT format
// ("AppName/1.0 (you@example.com)"). Empty contact returns nothing: a source
// registered without one would 403 on every poll forever, which reads as a
// broken feed rather than an unconfigured one.
//
// Kept separate from AdditionalOfficialSources rather than gated inside it, so
// the caller that has the contact can append these without re-adding the
// keyless feeds the default catalogue already holds.
func ContactGatedOfficialSources(contact string) []Source {
	if contact == "" {
		return nil
	}
	return []Source{
		// BLS is specific about this. Measured from this host, it serves 200
		// to "TradeSys/1.0 (an-address@example.com)" and 403 to both the
		// catalogue's own URL-style agent and a browser string. A browser
		// string is not the workaround: the point of that agent is to be
		// honest about what is fetching.
		//
		// Worth the wiring for one feed, because this is the CPI and payroll
		// release feed and those two numbers move every index in the universe.
		// FTC had been 403ing for 282 consecutive polls before anyone looked,
		// for exactly this reason: it was registered with the catalogue's
		// URL-style agent. Its URL was never wrong. A source that fails this
		// consistently should have been read as misconfigured rather than
		// down, which is an argument for the health page distinguishing the
		// two -- 282 identical failures is not an outage.
		{
			ID: "ftc-press", Name: "FTC competition and consumer protection",
			URL:     "https://www.ftc.gov/feeds/press-release.xml",
			Country: "US", Category: "regulatory",
			Method: MethodRSS, Language: "en", Trust: TrustOfficial,
			Refresh: 5 * time.Minute, Timeout: 15 * time.Second,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true,
			UserAgent: contact,
		},
		{
			ID: "bls-releases", Name: "BLS news releases",
			URL:     "https://www.bls.gov/feed/bls_latest.rss",
			Country: "US", Category: "economy",
			Method: MethodRSS, Language: "en", Trust: TrustOfficial,
			Refresh: 15 * time.Minute, Timeout: 15 * time.Second,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true,
			UserAgent: contact,
		},
	}
}
