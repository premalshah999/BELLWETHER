package company

import (
	"bufio"
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

//go:embed data/us_tickers.csv
var embeddedUSTickers embed.FS

//go:embed data/us_listings.csv
var embeddedUSListings embed.FS

// USTicker is one SEC-registered ticker — the full referenceable universe,
// analogous to what equity_l.csv is for NSE. Sourced from SEC's own
// company_tickers_exchange.json, which carries the CIK the filings tape
// needs. Most of these never appear in a scan or on a watchlist; what they
// are for is recognizing a symbol at all — resolving "AMZN" or "HSBC" to a
// real listing rather than guessing, the same job equity_l.csv does for a
// bare NSE ticker.
type USTicker struct {
	Symbol   string
	Name     string
	CIK      string
	Exchange string // Nasdaq | NYSE | CBOE | OTC
}

// USListing is one company in the US scan universe: the S&P 500, chosen for
// the same reason industry.csv chose roughly 750 NSE names rather than the
// full 2,557-row master — a standard, liquid, recognizable subset rather
// than an arbitrary cutoff. Carries GICS sector and sub-industry, the US
// analogue of industry.csv's NSE industry classification.
type USListing struct {
	Symbol      string
	Name        string
	Sector      string
	SubIndustry string
	CIK         string
	Exchange    string
}

// LoadUSTickers reads the full SEC ticker reference (symbol, name, cik,
// exchange).
func LoadUSTickers(r io.Reader) ([]USTicker, error) {
	cr := csv.NewReader(bufio.NewReader(r))
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("company: read us tickers header: %w", err)
	}
	col := indexHeader(header)
	symIdx, nameIdx, cikIdx, exIdx := col["SYMBOL"], col["NAME"], col["CIK"], col["EXCHANGE"]

	var out []USTicker
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // one malformed row must not discard the universe
		}
		sym := strings.ToUpper(strings.TrimSpace(field(rec, symIdx)))
		if sym == "" {
			continue
		}
		out = append(out, USTicker{
			Symbol:   sym,
			Name:     field(rec, nameIdx),
			CIK:      field(rec, cikIdx),
			Exchange: strings.ToUpper(field(rec, exIdx)),
		})
	}
	return out, nil
}

// LoadUSListings reads the US scan universe (symbol, name, GICS sector and
// sub-industry, cik, exchange).
func LoadUSListings(r io.Reader) ([]USListing, error) {
	cr := csv.NewReader(bufio.NewReader(r))
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("company: read us listings header: %w", err)
	}
	col := indexHeader(header)
	symIdx, nameIdx := col["SYMBOL"], col["NAME"]
	sectorIdx, subIdx := col["SECTOR"], col["SUB_INDUSTRY"]
	cikIdx, exIdx := col["CIK"], col["EXCHANGE"]

	var out []USListing
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		sym := strings.ToUpper(strings.TrimSpace(field(rec, symIdx)))
		if sym == "" {
			continue
		}
		out = append(out, USListing{
			Symbol:      sym,
			Name:        field(rec, nameIdx),
			Sector:      field(rec, sectorIdx),
			SubIndustry: field(rec, subIdx),
			CIK:         field(rec, cikIdx),
			Exchange:    strings.ToUpper(field(rec, exIdx)),
		})
	}
	return out, nil
}

// LoadEmbeddedUS reads the US ticker reference and scan universe compiled
// into the binary, the US counterparts of LoadEmbedded and its industry.csv.
func LoadEmbeddedUS() ([]USTicker, []USListing, error) {
	tf, err := embeddedUSTickers.Open("data/us_tickers.csv")
	if err != nil {
		return nil, nil, fmt.Errorf("company: open embedded us tickers: %w", err)
	}
	defer tf.Close()
	tickers, err := LoadUSTickers(tf)
	if err != nil {
		return nil, nil, err
	}

	lf, err := embeddedUSListings.Open("data/us_listings.csv")
	if err != nil {
		return nil, nil, fmt.Errorf("company: open embedded us listings: %w", err)
	}
	defer lf.Close()
	listings, err := LoadUSListings(lf)
	if err != nil {
		return nil, nil, err
	}
	return tickers, listings, nil
}

func indexHeader(header []string) map[string]int {
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.ToUpper(strings.TrimSpace(h))] = i
	}
	return col
}

func field(rec []string, i int) string {
	if i >= 0 && i < len(rec) {
		return strings.TrimSpace(rec[i])
	}
	return ""
}
