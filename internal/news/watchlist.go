package news

import (
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// WatchlistSource builds a discovery source for one instrument.
//
// The curated catalog is broad by design — "India stocks", "India banking" —
// which is right for finding what is happening in the market and wrong for
// following a position. Measured against a real archive, 15 of 4,308 collected
// items mentioned Reliance, and most of those were mutual-fund NAV
// declarations that merely share the name.
//
// A watchlist is a statement about which companies matter to this operator, so
// each one gets its own query. These sources also carry their symbol, which is
// what lets the scheduler poll them harder when something is happening to that
// company — the heat mechanism had nothing to act on before this existed.
// Watched is one instrument to follow, and why.
type Watched struct {
	Ticker string
	// Venue decides locale: which Google News edition is searched and which
	// market words pad a bare-ticker query. The zero value is
	// marketdata.ExchangeUS, so a Watched built without setting this is
	// treated as American -- callers that mean an NSE/BSE instrument must
	// say so explicitly.
	Venue   marketdata.Exchange
	Company string
	// Attention marks an instrument the scanner flagged as moving with no
	// explanation in the archive. The question then is not "keep me posted"
	// but "what happened today", which is a different search and a different
	// cadence.
	Attention bool
}

func WatchlistSource(sym marketdata.Symbol, company string) Source {
	return watchedSource(Watched{Ticker: sym.Ticker, Venue: sym.Exchange, Company: company})
}

func watchedSource(w Watched) Source {
	ticker, company := w.Ticker, w.Company
	indian := w.Venue == marketdata.ExchangeNSE || w.Venue == marketdata.ExchangeBSE
	query := watchlistQuery(ticker, company, w.Attention, indian)
	refresh := 4 * time.Minute
	if w.Attention {
		// The move already happened; the explanation is arriving now or in
		// the next hour. Polling this at the leisurely watchlist cadence is
		// how the answer shows up after it stopped being useful.
		refresh = 90 * time.Second
	}
	hl, gl, country := "en-US", "US", "US"
	if indian {
		hl, gl, country = "en-IN", "IN", "IN"
	}
	// The id carries the venue for every symbol that needs disambiguating
	// from it (NSE/BSE), and stays bare for US, matching
	// marketdata.Symbol{...}.String() lowercased -- the same rule the
	// venue-qualification migration applied to every id already on record,
	// so a source created today and one migrated from before it look alike.
	id := "watch-" + strings.ToLower(ticker)
	if w.Venue != marketdata.ExchangeUS {
		id += "." + strings.ToLower(string(w.Venue))
	}
	return Source{
		ID:   id,
		Name: displayName(ticker, company, w.Attention),
		URL:  GoogleNewsSearch(query, hl, gl),

		Method:   MethodGoogleNews,
		Category: "watchlist",
		Country:  country,
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

// watchlistQuery composes the search.
//
// The registered name alone, as an exact phrase, when one is known. Adding the
// bare ticker as an alternative seemed harmless and was not: searching
// "Reliance Industries" OR "RELIANCE" returned "From Rehabilitation to
// Self-Reliance" and "Rohingya repatriation, self-reliance", and every one of
// those was then attributed to the company because the query had named it.
//
// A ticker is only a good search term when it is a distinctive string, and
// most are not — "IDEA", "TOTAL", "RELIANCE" are ordinary English words before
// they are instruments. The exact company phrase has no such problem.
//
// The window is the other half of the query and was missing entirely. Measured
// against Google News, a bare "Reliance Industries" returned 81 items of which
// 32 were more than a week old and the oldest was 1,541 days — four-year-old
// articles arriving on every poll, about the companies the operator cares most
// about. The same query with a window returned 61 items, none older than two
// days.
func watchlistQuery(ticker, company string, attention, indian bool) string {
	// An instrument the scanner just flagged is a question about today, so the
	// window closes to a single day. A followed instrument is a standing
	// interest and three days lets a weekend's news arrive.
	window := "when:3d"
	if attention {
		window = "when:1d"
	}

	company = strings.TrimSpace(company)
	if company == "" {
		// With no name to work from, the ticker is paired with market words
		// so it lands in a financial context rather than a linguistic one.
		marketWords := "shares OR stock OR NASDAQ OR NYSE"
		if indian {
			marketWords = "shares OR stock OR NSE"
		}
		return fmt.Sprintf("%q (%s) %s", ticker, marketWords, window)
	}
	// The legal suffix is dropped: publishers write "Reliance Industries",
	// not "Reliance Industries Limited", and the phrase must match how they
	// write it.
	for _, suffix := range []string{" Limited", " Ltd.", " Ltd", " Corporation", " Corp"} {
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
// Duplicate tickers collapse: the same company held on two exchanges is one
// company as far as news is concerned, and polling twice would double the
// traffic to learn the same thing. Where a ticker arrives both as a watchlist
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
		// Keyed by ticker and venue together: the same string can be two
		// different instruments (INFY.NSE vs the NYSE-listed INFY), and
		// collapsing them into one source would silently follow the wrong
		// one whichever happened to sort first.
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
