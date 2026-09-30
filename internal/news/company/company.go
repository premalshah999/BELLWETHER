// Package company resolves the companies named in text to US tickers.
//
// This is the most correctness-sensitive code in the news pipeline: tagging an
// article with the wrong stock puts a false headline on a position someone may
// act on, while missing one loses an article another source usually carries
// too. Every ambiguous decision therefore resolves toward returning nothing.
package company

import (
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

//go:embed data/us_tickers.csv data/us_listings.csv
var data embed.FS

//go:embed data/common_words.txt
var commonWordsFile string

// commonWords stops an ordinary English word from standing in for a company.
var commonWords = func() map[string]bool {
	out := make(map[string]bool, 1200)
	for _, line := range strings.Split(commonWordsFile, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			for _, w := range strings.Fields(line) {
				out[strings.ToLower(w)] = true
			}
		}
	}
	return out
}()

// USTicker is one SEC-registered ticker, in SEC's own order (roughly by
// market value). The CIK is the join key to every SEC filing.
type USTicker struct {
	Symbol, Name, CIK, Exchange string
}

// USListing is one member of the scan universe, the S&P 1500, with its GICS
// classification.
type USListing struct {
	Symbol, Name, Sector, SubIndustry, CIK, Exchange string
}

// readCSV reads an embedded CSV into rows keyed by upper-cased header. One
// malformed row is skipped rather than discarding the file.
func readCSV(name string) ([]map[string]string, error) {
	f, err := data.Open("data/" + name)
	if err != nil {
		return nil, fmt.Errorf("company: open %s: %w", name, err)
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("company: read %s header: %w", name, err)
	}
	var rows []map[string]string
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			continue
		}
		row := make(map[string]string, len(header))
		for i, h := range header {
			if i < len(rec) {
				row[strings.ToUpper(strings.TrimSpace(h))] = strings.TrimSpace(rec[i])
			}
		}
		if row["SYMBOL"] = strings.ToUpper(row["SYMBOL"]); row["SYMBOL"] != "" {
			rows = append(rows, row)
		}
	}
}

// LoadEmbeddedUS reads the SEC ticker reference and the scan universe
// compiled into the binary.
func LoadEmbeddedUS() ([]USTicker, []USListing, error) {
	tr, err := readCSV("us_tickers.csv")
	if err != nil {
		return nil, nil, err
	}
	lr, err := readCSV("us_listings.csv")
	if err != nil {
		return nil, nil, err
	}
	tickers := make([]USTicker, 0, len(tr))
	for _, r := range tr {
		tickers = append(tickers, USTicker{r["SYMBOL"], r["NAME"], r["CIK"], strings.ToUpper(r["EXCHANGE"])})
	}
	listings := make([]USListing, 0, len(lr))
	for _, r := range lr {
		listings = append(listings, USListing{r["SYMBOL"], r["NAME"], r["SECTOR"], r["SUB_INDUSTRY"], r["CIK"], strings.ToUpper(r["EXCHANGE"])})
	}
	return tickers, listings, nil
}

// usTickerSet is every SEC-registered ticker, loaded once on first use.
var usTickerSet = sync.OnceValue(func() map[string]bool {
	tickers, _, err := LoadEmbeddedUS()
	if err != nil {
		panic(err) // the file is compiled in; failing to read it is a build defect
	}
	set := make(map[string]bool, len(tickers))
	for _, t := range tickers {
		set[t.Symbol] = true
	}
	return set
})

// IsUSTicker reports whether t is a real SEC-registered ticker, so a symbol
// a model or a PDF extractor produced is kept only when it names a listing.
func IsUSTicker(t string) bool { return usTickerSet()[strings.ToUpper(strings.TrimSpace(t))] }

// Company is one listed instrument.
type Company struct {
	Symbol string
	Name   string
	CIK    string
}

// Method records how a match was reached, so a reviewer can tell a legal-name
// hit from a bare-ticker guess without re-running the resolver.
type Method string

const (
	MethodLegalName Method = "legal_name"
	MethodAlias     Method = "alias"
	MethodTicker    Method = "ticker"
	MethodExplicit  Method = "explicit_ticker" // "NASDAQ: NVDA"
)

// Match is one resolved company mention.
type Match struct {
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Method     Method  `json:"method"`
	Matched    string  `json:"matched"` // the span of text that produced the hit
}

// Confidence levels. The spread matters more than the values: an alert can
// demand legal-name certainty while a browsable feed accepts a bare ticker.
const (
	confExplicit  = 0.99 // "NASDAQ: NVDA"
	confLegalName = 0.97 // the full registered name appeared
	confAlias     = 0.95 // a curated brand name appeared
	confSoleName  = 0.92 // a one-word registered name ("Apple") appeared whole
	confTicker    = 0.90 // a bare uppercase symbol appeared
)

// maxPhraseTokens caps how long an n-gram may be when scanning text.
const maxPhraseTokens = 8

// Master is an indexed, read-only view of the listed universe.
type Master struct {
	bySymbol map[string]Company
	// byPhrase maps a normalized name or alias to the symbols claiming it;
	// more than one claimant is ambiguous and matches nothing.
	byPhrase    map[string][]string
	aliasPhrase map[string]bool // contributed by the curated alias table
	sector      map[string]string
	rank        map[string]int    // position in SEC's list, roughly market value
	byCIK       map[string]string // the primary (first-listed) share class
}

// Load builds the resolver over every SEC registrant. Sectors come from the
// scan universe.
func Load() (*Master, error) {
	tickers, listings, err := LoadEmbeddedUS()
	if err != nil {
		return nil, err
	}
	m := &Master{
		bySymbol:    make(map[string]Company, len(tickers)),
		byPhrase:    make(map[string][]string, len(tickers)),
		aliasPhrase: make(map[string]bool, len(brandAliases)),
		sector:      make(map[string]string, len(listings)),
		rank:        make(map[string]int, len(tickers)),
		byCIK:       make(map[string]string, len(tickers)),
	}
	for _, l := range listings {
		m.sector[l.Symbol] = l.Sector
	}
	for i, t := range tickers {
		c := Company{Symbol: t.Symbol, Name: t.Name, CIK: t.CIK}
		if _, dup := m.bySymbol[c.Symbol]; dup || c.Name == "" {
			continue
		}
		m.bySymbol[c.Symbol], m.rank[c.Symbol] = c, i
		if _, seen := m.byCIK[c.CIK]; !seen && c.CIK != "" {
			m.byCIK[c.CIK] = c.Symbol
		}
		if p := Normalize(c.Name); p != "" && !m.sameIssuer(m.byPhrase[p], c) {
			m.byPhrase[p] = append(m.byPhrase[p], c.Symbol)
		}
	}
	// Aliases go in after legal names, so an alias can point at a company
	// whose own name never appears in the press.
	for alias, sym := range brandAliases {
		key := Normalize(alias)
		if _, listed := m.bySymbol[sym]; !listed || key == "" {
			continue
		}
		if _, fromName := m.byPhrase[key]; !fromName {
			m.aliasPhrase[key] = true
		}
		m.byPhrase[key] = appendUnique(m.byPhrase[key], sym)
	}
	if len(m.bySymbol) == 0 {
		return nil, fmt.Errorf("company: the listed universe is empty")
	}
	return m, nil
}

// sameIssuer reports whether c is another share class of a company already
// claiming the phrase (GOOG after GOOGL, BRK-A after BRK-B). SEC lists the
// primary class first, so the first claimant keeps the name.
func (m *Master) sameIssuer(claimants []string, c Company) bool {
	for _, s := range claimants {
		if c.CIK != "" && m.bySymbol[s].CIK == c.CIK {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

// Len reports how many instruments are listed.
func (m *Master) Len() int { return len(m.bySymbol) }

// Lookup returns one company by symbol.
func (m *Master) Lookup(symbol string) (Company, bool) {
	c, ok := m.bySymbol[strings.ToUpper(strings.TrimSpace(symbol))]
	return c, ok
}

// ByCIK returns the ticker of an SEC registrant, so a filing that declares
// its filer's CIK resolves with no text matching at all.
func (m *Master) ByCIK(cik string) (string, bool) {
	s, ok := m.byCIK[strings.TrimSpace(cik)]
	return s, ok
}

// Sector returns a scan-universe member's GICS sector.
func (m *Master) Sector(symbol string) (string, bool) {
	s, ok := m.sector[strings.ToUpper(strings.TrimSpace(symbol))]
	return s, ok
}

// Universe lists the scan universe (the S&P 1500), sorted.
func (m *Master) Universe() []string {
	out := make([]string, 0, len(m.sector))
	for sym := range m.sector {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

// SymbolsInSector lists a sector's scan-universe members, largest first.
func (m *Master) SymbolsInSector(sector string) []string {
	var out []string
	for sym, s := range m.sector {
		if strings.EqualFold(s, sector) {
			out = append(out, sym)
		}
	}
	sort.Slice(out, func(i, j int) bool { return m.rank[out[i]] < m.rank[out[j]] })
	return out
}

// sectorWords map the words people use to GICS sectors. Small and explicit
// rather than fuzzy, because a wrong sector enumerates the wrong companies.
var sectorWords = map[string]string{
	"bank": "Financials", "banks": "Financials", "banking": "Financials", "lender": "Financials",
	"lenders": "Financials", "insurer": "Financials", "insurers": "Financials", "insurance": "Financials",
	"brokerage": "Financials", "asset manager": "Financials", "asset managers": "Financials",
	"software": "Information Technology", "semiconductor": "Information Technology",
	"semiconductors": "Information Technology", "chipmaker": "Information Technology",
	"chipmakers": "Information Technology", "chip stocks": "Information Technology",
	"tech stocks": "Information Technology", "cloud": "Information Technology",
	"pharma": "Health Care", "pharmaceutical": "Health Care", "pharmaceuticals": "Health Care",
	"biotech": "Health Care", "drugmaker": "Health Care", "drugmakers": "Health Care",
	"healthcare": "Health Care", "health care": "Health Care", "hospital": "Health Care",
	"hospitals": "Health Care", "medical device": "Health Care", "medical devices": "Health Care",
	"oil": "Energy", "gas": "Energy", "energy": "Energy", "refiner": "Energy", "refiners": "Energy",
	"drillers": "Energy", "shale": "Energy",
	"utility": "Utilities", "utilities": "Utilities", "electricity": "Utilities",
	"retailer": "Consumer Discretionary", "retailers": "Consumer Discretionary",
	"automaker": "Consumer Discretionary", "automakers": "Consumer Discretionary",
	"restaurant": "Consumer Discretionary", "restaurants": "Consumer Discretionary",
	"homebuilder": "Consumer Discretionary", "homebuilders": "Consumer Discretionary",
	"apparel": "Consumer Discretionary", "hotels": "Consumer Discretionary",
	"cruise": "Consumer Discretionary", "cruise lines": "Consumer Discretionary",
	"food": "Consumer Staples", "beverage": "Consumer Staples", "beverages": "Consumer Staples",
	"grocer": "Consumer Staples", "grocers": "Consumer Staples", "tobacco": "Consumer Staples",
	"consumer staples": "Consumer Staples",
	"telecom":          "Communication Services", "telecoms": "Communication Services",
	"media": "Communication Services", "streaming": "Communication Services",
	"advertising": "Communication Services", "wireless carriers": "Communication Services",
	"reit": "Real Estate", "reits": "Real Estate", "real estate": "Real Estate",
	"steel": "Materials", "metals": "Materials", "mining": "Materials", "miners": "Materials",
	"chemical": "Materials", "chemicals": "Materials", "copper": "Materials",
	"gold miners": "Materials", "fertilizer": "Materials",
	"defense": "Industrials", "defence": "Industrials", "aerospace": "Industrials",
	"airline": "Industrials", "airlines": "Industrials", "railroad": "Industrials",
	"railroads": "Industrials", "trucking": "Industrials", "machinery": "Industrials",
	"industrials": "Industrials", "construction": "Industrials",
}

// SectorsMentioned finds at most two GICS sectors a question names, through
// the explicit word table only: matching loosely would enumerate hundreds of
// companies against a question that named none.
func (m *Master) SectorsMentioned(text string) []string {
	lower := strings.ToLower(text)
	seen := map[string]bool{}
	var out []string
	for word, sector := range sectorWords {
		if !seen[sector] && containsWord(lower, word) {
			seen[sector] = true
			out = append(out, sector)
		}
	}
	sort.Strings(out)
	if len(out) > 2 {
		out = out[:2]
	}
	return out
}

func containsWord(haystack, word string) bool {
	for i := strings.Index(haystack, word); i >= 0; {
		end := i + len(word)
		if (i == 0 || !isWordByte(haystack[i-1])) && (end == len(haystack) || !isWordByte(haystack[end])) {
			return true
		}
		next := strings.Index(haystack[i+1:], word)
		if next < 0 {
			return false
		}
		i += 1 + next
	}
	return false
}
