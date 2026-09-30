package news

import (
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Watched is one instrument to follow, and why. Each watched company gets its
// own news query: the market-wide catalog finds what is happening, not what
// is happening to a position. The source carries its symbol, so the scheduler
// can poll it harder when that company becomes eventful.
type Watched struct {
	Ticker  string
	Venue   marketdata.Exchange // ExchangeIndex for a benchmark
	Company string
	// Attention marks an instrument the scanner flagged as moving with no
	// explanation in the archive: the question is "what happened today".
	Attention bool
}

func watchedSource(w Watched) Source {
	ticker, company := w.Ticker, w.Company
	query := watchlistQuery(ticker, company, w.Attention)
	refresh := 4 * time.Minute
	if w.Attention {
		// The explanation is arriving now; the watchlist cadence is too slow.
		refresh = 90 * time.Second
	}
	// The id is the canonical symbol, lowercased: watch-aapl, watch-gspc.index.
	id := "watch-" + strings.ToLower(ticker)
	if w.Venue != marketdata.ExchangeUS {
		id += "." + strings.ToLower(string(w.Venue))
	}
	return Source{
		ID:   id,
		Name: displayName(ticker, company, w.Attention),
		URL:  GoogleNewsSearch(query),

		Method:   MethodGoogleNews,
		Category: "watchlist",
		Country:  "US",
		Language: "en",

		Trust: TrustMajorFin,
		// Between the fast and normal lanes. A watched company is worth more
		// attention than the market at large and less than an exchange feed,
		// and heat escalation compresses this further when something happens.
		Refresh: refresh,
		Timeout: 20 * time.Second,

		// Same terms as the rest of the discovery layer: a headline and a
		// link to the publisher, not their content.
		Usage:   UsageDiscoveryOnly,
		Display: DisplayLinkOnly,

		// The symbol the query is about. Carrying it means every item from
		// this source is attributable without re-resolving from the headline,
		// and means the scheduler can promote this source when the company
		// becomes eventful.
		Symbols: []string{ticker},
		Enabled: true,
	}
}

// watchlistQuery composes the search: the company name as an exact phrase
// when known, because most tickers are ordinary words ("ALL", "NOW", "CAT")
// and searching one attributes unrelated stories to the company. The time
// window keeps years-old articles from arriving on every poll: one day for a
// flagged move, three for a standing interest so a weekend's news arrives.
func watchlistQuery(ticker, company string, attention bool) string {
	window := "when:3d"
	if attention {
		window = "when:1d"
	}
	company = strings.TrimSpace(company)
	if company == "" {
		// No name: market words keep the ticker in a financial context.
		return fmt.Sprintf("%q (shares OR stock OR NASDAQ OR NYSE) %s", ticker, window)
	}
	// Publishers write "Apple", not "Apple Inc.": drop the legal suffix.
	for _, suffix := range []string{" Inc.", " Inc", " Corporation", " Corp.", " Corp", " & Co.", " Co.", " Co",
		" Company", " plc", " PLC", " Ltd.", " Ltd", " Limited", ","} {
		company = strings.TrimSuffix(company, suffix)
	}
	return fmt.Sprintf("%q %s", company, window)
}

func displayName(ticker, company string, attention bool) string {
	tag := " (watchlist)"
	if attention {
		// Named differently because the two answer different questions, and an
		// operator reading the source list should be able to tell which
		// instruments the scanner put there.
		tag = " (unexplained move)"
	}
	if company == "" {
		return ticker + tag
	}
	return company + tag
}

// WatchlistSources builds a source per watched instrument.
//
// Duplicate tickers collapse. Where a ticker arrives both as a watchlist
// entry and as a scanner flag, the flag wins — the tighter query is the more
// useful of the two, and the operator following it still gets the news.
func WatchlistSources(items []Watched) []Source {
	seen := map[string]int{}
	out := make([]Source, 0, len(items))
	for _, w := range items {
		w.Ticker = strings.ToUpper(strings.TrimSpace(w.Ticker))
		if w.Ticker == "" {
			continue
		}
		// Keyed by ticker and venue together: a stock and an index can share
		// a ticker.
		key := w.Ticker + "." + string(w.Venue)
		if at, dup := seen[key]; dup {
			if w.Attention && !out[at].Attention() {
				out[at] = watchedSource(w)
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, watchedSource(w))
	}
	return out
}

// Attention reports whether this source exists because the scanner flagged the
// instrument rather than because someone is following it.
func (s Source) Attention() bool {
	return s.Category == "watchlist" && strings.Contains(s.Name, "(unexplained move)")
}

// Watchlist reports whether a source follows a specific watched instrument.
func (s Source) Watchlist() bool { return s.Category == "watchlist" }
