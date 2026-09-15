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
