package main

import (
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// scanUniverse is every symbol the scanner and the fundamentals runner cover,
// and what index_constituents records as current membership: the S&P 1500,
// a liquid, recognizable subset of the listed universe.
type scanUniverse struct {
	symbols  []marketdata.Symbol
	listings []postgres.ConstituentListing
}

func buildScanUniverse(master *company.Master) scanUniverse {
	var u scanUniverse
	for _, sym := range master.Universe() {
		sector, _ := master.Sector(sym)
		u.symbols = append(u.symbols, marketdata.Symbol{Ticker: sym})
		u.listings = append(u.listings, postgres.ConstituentListing{Symbol: sym, Industry: sector})
	}
	return u
}

// Symbols is the func() []marketdata.Symbol shape the scanner and the
// fundamentals runner take.
func (u *scanUniverse) Symbols() []marketdata.Symbol { return u.symbols }
