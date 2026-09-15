package main

import (
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// scanUniverse is the combined NSE + US scan universe: every symbol the
// scanner and the fundamentals runner cover, and what index_constituents
// records as current membership.
type scanUniverse struct {
	symbols  []marketdata.Symbol
	listings []postgres.ConstituentListing
}

// buildScanUniverse combines the NSE scan subset (industry.csv, via the
// embedded master) with the US scan subset (the S&P 500, via us_listings.csv)
// into one venue-tagged universe. Both are the same kind of thing -- a
// liquid, recognizable subset of a much larger listed universe -- built the
// same way for the same reason.
func buildScanUniverse(nse *company.Master, usListings []company.USListing) scanUniverse {
	nseIndustry := nse.IndexConstituents() // symbol -> industry, NSE scan subset

	var u scanUniverse
	for symbol, industry := range nseIndustry {
		sym := marketdata.Symbol{Ticker: symbol, Exchange: marketdata.ExchangeNSE}
		u.symbols = append(u.symbols, sym)
		u.listings = append(u.listings, postgres.ConstituentListing{
			Symbol: sym.String(), Industry: industry, Venue: "NSE", Taxonomy: "nse-industry",
		})
	}
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
// a filer worth attributing a filing to is not necessarily one of the 504
// S&P 500 names).
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
