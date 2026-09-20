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
	}
	out := make([]Source, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, Source{ID: f.id, Name: f.name, URL: f.url, Country: f.country, Category: f.category,
			Method: MethodRSS, Language: "en", Trust: TrustOfficial, Refresh: 5 * time.Minute, Timeout: 15 * time.Second,
			Usage: UsageOfficial, Display: DisplayFull, Enabled: true, UserAgent: catalogUserAgent})
	}
	return out
}
