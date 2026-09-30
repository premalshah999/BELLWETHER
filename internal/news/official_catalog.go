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

		// US agencies whose announcements reprice a sector before any
		// publisher writes about them: trade actions, GDP and income data,
		// commodities enforcement. DOJ's press feed is deliberately absent: it
		// carries every US Attorney's office, skipped the relevance gate as an
		// official source, and filled the policy page with criminal cases.
		// Antitrust arrives through disc-us-antitrust instead.
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
// contact in the User-Agent ("AppName/1.0 (you@example.com)"). An empty
// contact returns nothing: registered without one they would be refused on
// every poll.
func ContactGatedOfficialSources(contact string) []Source {
	if contact == "" {
		return nil
	}
	return []Source{
		// BLS and FTC serve a contact-style agent ("TradeSys/1.0
		// (you@example.com)") and refuse both a URL-style one and a browser
		// string. FTC failed 282 consecutive polls for exactly this reason:
		// its URL was never wrong.
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
