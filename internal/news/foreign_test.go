package news

import "testing"

func TestOutsideUSMarket(t *testing.T) {
	for _, c := range []struct {
		url, publisher string
		want           bool
	}{
		{"https://www.livemint.com/money/personal-finance/x.html", "", true},
		{"https://economictimes.indiatimes.com/markets/x", "", true},
		{"https://www.businesstoday.in/markets/x", "", true},
		{"https://m.economictimes.com/markets/stocks/news/x.cms", "m.economictimes.com", true},
		{"https://in.investing.com/news/x", "", true},
		{"https://www.investing.com/india/news/x", "", true},
		{"https://news.google.com/rss/articles/abc", "Mint", true},
		{"https://news.google.com/rss/articles/abc", "The Economic Times", true},
		{"https://www.investing.com/news/stock-market-news/x", "Investing.com", false},
		{"https://www.cnbc.com/2026/09/30/x.html", "CNBC", false},
		{"https://news.google.com/rss/articles/abc", "Reuters", false},
		{"https://www.fidelity.com/learning-center/x", "", false},
	} {
		if got := OutsideUSMarket(c.url, c.publisher); got != c.want {
			t.Errorf("OutsideUSMarket(%q, %q) = %v, want %v", c.url, c.publisher, got, c.want)
		}
	}
}
