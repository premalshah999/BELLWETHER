package news

import (
	"net/url"
	"strings"
)

// foreignMarketHosts are publishers whose finance coverage is written for
// another country's investors: tax rules, savings products, prices and
// listings that do not apply to a US reader. A search for "401k strategy" or
// a company's name still returns them, and their mutual-fund and rupee advice
// then reads as an answer.
var foreignMarketHosts = []string{
	"livemint.com", "economictimes.indiatimes.com", "timesofindia.indiatimes.com",
	"indiatimes.com", "economictimes.com", "timesofindia.com", "moneycontrol.com", "business-standard.com",
	"financialexpress.com", "thehindubusinessline.com", "thehindu.com",
	"ndtv.com", "ndtvprofit.com", "hindustantimes.com", "zeebiz.com",
	"news18.com", "cnbctv18.com", "businesstoday.in", "indiatoday.in",
	"outlookbusiness.com", "rediff.com", "firstpost.com", "deccanherald.com",
	"goodreturns.in", "tradebrains.in", "trendlyne.com", "tickertape.in",
	"angelone.in", "zerodha.com", "groww.in", "5paisa.com", "upstox.com",
	"dsij.in", "equitymaster.com", "investing.com/india", "in.investing.com",
	"in.tradingview.com", "inc42.com", "entrackr.com", "yourstory.com",
}

// foreignMarketPublishers are the same outlets by the name a news aggregator
// gives them, for links that hide the publisher's address behind a redirect.
var foreignMarketPublishers = map[string]bool{
	"mint": true, "livemint": true, "the economic times": true, "economic times": true,
	"the times of india": true, "times of india": true, "moneycontrol": true,
	"moneycontrol.com": true, "business standard": true, "the financial express": true,
	"financial express": true, "the hindu businessline": true, "businessline": true,
	"the hindu": true, "ndtv profit": true, "ndtv": true, "hindustan times": true,
	"zee business": true, "news18": true, "cnbc tv18": true, "cnbctv18": true,
	"cnbc-tv18": true, "business today": true, "india today": true,
	"outlook business": true, "rediff": true, "firstpost": true, "deccan herald": true,
	"goodreturns": true, "trade brains": true, "groww": true, "zerodha": true,
	"5paisa": true, "upstox": true, "angel one": true, "dsij": true, "equitymaster": true,
	"inc42": true, "entrackr": true, "yourstory": true, "the indian express": true,
	"indian express": true, "financial express - india": true,
}

// OutsideUSMarket reports whether an article comes from a publisher writing
// for another country's investors, judged by its address and, failing that,
// by the publisher name an aggregator attached.
func OutsideUSMarket(rawURL, publisher string) bool {
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && u.Hostname() != "" {
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		// Country-code domains of the markets this app used to cover. A
		// .in or .co.in address is never a source on US securities.
		if strings.HasSuffix(host, ".in") {
			return true
		}
		path := host + strings.ToLower(u.EscapedPath())
		for _, h := range foreignMarketHosts {
			if host == h || strings.HasSuffix(host, "."+h) || strings.HasPrefix(path, h+"/") {
				return true
			}
		}
	}
	name := strings.ToLower(strings.TrimSpace(publisher))
	return name != "" && foreignMarketPublishers[name]
}
