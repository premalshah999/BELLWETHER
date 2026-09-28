package main

import (
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// scanUniverse is the US scan universe: every symbol the scanner and the
// fundamentals runner cover, and what index_constituents records as current
// membership.
//
// It was NSE + US. The NSE half is gone: this is a US equities product, and a
// second venue in the universe was not free -- it decided what reached the
// default market feed (which admits an event only when one of its companies is
// a constituent), what the screens ran over, and which companies a peer group
// could be drawn from. Carrying 750 Indian names through all of that to serve
// a market the product no longer covers cost a third of every scan and made
// the feed read as an Indian paper.
type scanUniverse struct {
	symbols  []marketdata.Symbol
	listings []postgres.ConstituentListing
}

// buildScanUniverse turns the embedded S&P 1500 listing set into one
// venue-tagged universe -- a liquid, recognizable subset of a much larger
// listed universe, which is what a scan universe is for.
func buildScanUniverse(usListings []company.USListing) scanUniverse {
	var u scanUniverse
	for _, l := range usListings {
		sym := marketdata.Symbol{Ticker: l.Symbol, Exchange: marketdata.ExchangeUS}
		u.symbols = append(u.symbols, sym)
		u.listings = append(u.listings, postgres.ConstituentListing{
			Symbol: sym.String(), Industry: l.Sector, Venue: "US", Taxonomy: "gics",
		})
	}
	return u
}

// Symbols implements the func() []marketdata.Symbol shape both
// scanner.Runner.Universe and fundamentals.Runner.Universe want, as a method
// value rather than a closure over a slice -- so a future refresh can swap
// u.symbols without every caller needing to know that happened.
func (u *scanUniverse) Symbols() []marketdata.Symbol { return u.symbols }

// buildUSCIKIndex builds the CIK -> ticker map SEC filing entity resolution
// needs, from the full SEC ticker reference (not just the scan universe --
// a filer worth attributing a filing to is not necessarily one of the
// 1,504 S&P 1500 names).
func buildUSCIKIndex(usTickers []company.USTicker) map[string]string {
	byCIK := make(map[string]string, len(usTickers))
	for _, t := range usTickers {
		if t.CIK == "" {
			continue
		}
		// A handful of CIKs are shared by dual-class shares (BRK-A/BRK-B).
		// The first ticker encountered wins; us_tickers.csv is generated
		// from SEC's own file in its original order, which is not
		// alphabetical, so this is not a meaningful ranking -- it is simply
		// deterministic, which is what matters for a value that must not
		// change between runs.
		if _, exists := byCIK[t.CIK]; !exists {
			byCIK[t.CIK] = t.Symbol
		}
	}
	return byCIK
}
