// Package company resolves the companies named in a piece of text to NSE
// symbols.
//
// This is the most correctness-sensitive code in the news pipeline. Attaching
// an article to the wrong stock puts a false headline on a position an
// operator may act on, while failing to attach one merely loses an article
// that another source will usually carry too. Every ambiguous decision here
// therefore resolves toward returning nothing.
package company

import (
	"bufio"
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"
)

//go:embed data/equity_l.csv
var embeddedMaster embed.FS

//go:embed data/common_words.txt
var commonWordsFile string

// commonWords is the lexicon that stops an ordinary English word from
// standing in for a company. Parsed once, at first use.
var commonWords = func() map[string]bool {
	out := make(map[string]bool, 1200)
	for _, line := range strings.Split(commonWordsFile, "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, w := range strings.Fields(line) {
			out[strings.ToLower(w)] = true
		}
	}
	return out
}()

// Company is one listed instrument.
//
// Symbol is the venue's own bare ticker (RELIANCE, AAPL), not the canonical
// venue-qualified form -- CanonicalSymbol builds that, and every caller
// storing a symbol must use it. The two are only identical for US listings,
// which is exactly the trap this type exists to keep a caller out of: a
// resolver that returned a bare ticker to something that appended ".NSE"
// unconditionally would file every US match under an NSE symbol.
type Company struct {
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
	ISIN   string `json:"isin"`
	Series string `json:"series"`
	// Venue is "NSE" or "US". Empty means NSE, since the NSE master predates
	// this field and loads without setting it.
	Venue string `json:"venue,omitempty"`
}

// CanonicalSymbol is the venue-qualified symbol every other package stores
// and joins on: bare for US, suffixed for NSE.
func (c Company) CanonicalSymbol() string {
	if c.Venue == VenueUS {
		return c.Symbol
	}
	return c.Symbol + ".NSE"
}

// Venues a company can be listed on.
const (
	VenueNSE = "NSE"
	VenueUS  = "US"
)

// Method records how a match was reached, so a reviewer can tell a legal-name
// hit from a bare-ticker guess without re-running the resolver.
type Method string

const (
	MethodLegalName Method = "legal_name"
	MethodAlias     Method = "alias"
	MethodTicker    Method = "ticker"
	MethodLeadWord  Method = "lead_word"       // distinctive first word of a name
	MethodExplicit  Method = "explicit_ticker" // "NSE: RELIANCE", "RELIANCE.NS"
)

// Match is one resolved company mention.
type Match struct {
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Method     Method  `json:"method"`
	Matched    string  `json:"matched"` // the span of text that produced the hit
	// Venue carries the same warning Company.Symbol does: Symbol here is
	// bare, and only CanonicalSymbol is safe to store.
	Venue string `json:"venue,omitempty"`
}

// CanonicalSymbol is the venue-qualified form of this match's symbol.
func (m Match) CanonicalSymbol() string {
	if m.Venue == VenueUS {
		return m.Symbol
	}
	return m.Symbol + ".NSE"
}

// Confidence levels. The spread matters more than the absolute values: it
// lets a consumer demand legal-name certainty for an alert while accepting a
// bare ticker for a browsable feed.
const (
	confExplicit  = 0.99 // "NSE: RELIANCE" — unambiguous by construction
	confLegalName = 0.97 // the full registered name appeared
	confAlias     = 0.95 // a curated brand or former name appeared
	confTicker    = 0.90 // a bare uppercase symbol appeared
	// confSoleName is a company whose entire registered name is the one word
	// that appeared -- "Apple", "Microsoft", "Nvidia". That is a legal-name
	// match, not a fragment, and it is the common shape of a US name once
	// the corporate suffix is normalised away, where an Indian name usually
	// keeps two words ("Reliance Industries"). Rated below a multi-word
	// legal name because one word carries less evidence, and above a lead
	// word because nothing was dropped to get here.
	confSoleName = 0.92
	confSingle   = 0.80 // one word of a longer company name appeared
)

// maxPhraseTokens caps how long an n-gram may be when scanning text. The
// longest normalized NSE name is comfortably inside this.
const maxPhraseTokens = 8

// Master is an indexed, read-only view of the listed universe.
type Master struct {
	// bySymbol is keyed by CANONICAL symbol (RELIANCE.NSE, AAPL), not by
	// bare ticker. Two venues genuinely share tickers -- INFY and ABB are a
	// different instrument on NSE than on the NYSE -- so a bare-ticker key
	// would silently let one overwrite the other.
	bySymbol map[string]Company
	// byPhrase maps a normalized name or alias to every canonical symbol
	// claiming it. A phrase with more than one claimant is ambiguous and is
	// resolved only when a venue preference settles it.
	byPhrase map[string][]string
	// aliasPhrase marks phrases contributed by the curated alias table rather
	// than by a registered legal name, so a Match can report which it was.
	aliasPhrase map[string]bool
	// leadPhrase marks a distinctive first word of a company name, registered
	// so that "Suzlon" finds Suzlon Energy. These are leads, not identifications.
	leadPhrase map[string]bool
	// solePhrase marks a phrase that is some company's ENTIRE normalized
	// registered name. One word that is the whole name is far better
	// evidence than one word taken out of a longer one, and only this
	// distinguishes them.
	solePhrase map[string]bool
	// industry maps a symbol to its NSE industry classification.
	industry map[string]string
	ordered  []Company
}

// Load reads a master from an NSE EQUITY_L.csv document.
func Load(r io.Reader) (*Master, error) {
	cr := csv.NewReader(bufio.NewReader(r))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("company: read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToUpper(strings.TrimSpace(h))] = i
	}
	symIdx, okS := col["SYMBOL"]
	nameIdx, okN := col["NAME OF COMPANY"]
	if !okS || !okN {
		return nil, fmt.Errorf("company: missing SYMBOL or NAME OF COMPANY column")
	}
	isinIdx, seriesIdx := col["ISIN NUMBER"], col["SERIES"]

	m := &Master{
		bySymbol:    make(map[string]Company, 2600),
		byPhrase:    make(map[string][]string, 3000),
		aliasPhrase: make(map[string]bool, len(brandAliases)),
		leadPhrase:  make(map[string]bool, 512),
		solePhrase:  make(map[string]bool, 3000),
	}
	get := func(rec []string, i int) string {
		if i >= 0 && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// One malformed row must not discard the listed universe.
			continue
		}
		sym := strings.ToUpper(get(rec, symIdx))
		name := get(rec, nameIdx)
		if sym == "" || name == "" {
			continue
		}
		c := Company{Symbol: sym, Name: name, ISIN: get(rec, isinIdx), Series: get(rec, seriesIdx), Venue: VenueNSE}
		m.bySymbol[c.CanonicalSymbol()] = c
		m.ordered = append(m.ordered, c)
		if p := Normalize(name); p != "" {
			m.byPhrase[p] = appendUnique(m.byPhrase[p], c.CanonicalSymbol())
			m.solePhrase[p] = true
		}
	}
	if len(m.ordered) == 0 {
		return nil, fmt.Errorf("company: master is empty")
	}

	// Curated aliases are added after the legal names so that a hand-written
	// alias can point at a symbol whose own name never appears in the press.
	// An alias naming an unlisted symbol is skipped rather than trusted: the
	// listing is the authority on what exists.
	for alias, sym := range brandAliases {
		// The curated alias table predates venues and names NSE tickers.
		canonical := sym + ".NSE"
		if _, listed := m.bySymbol[canonical]; !listed {
			continue
		}
		if key := Normalize(alias); key != "" {
			if _, fromName := m.byPhrase[key]; !fromName {
				m.aliasPhrase[key] = true
			}
			m.byPhrase[key] = appendUnique(m.byPhrase[key], canonical)
		}
	}
	m.indexLeadWords(m.ordered)
	m.loadIndustries()
	sort.Slice(m.ordered, func(i, j int) bool { return m.ordered[i].Symbol < m.ordered[j].Symbol })
	return m, nil
}

// LoadEmbedded reads the snapshot compiled into the binary.
//
// The app must be able to resolve companies before it has ever reached the
// network, so the listed universe ships with it. Refresh replaces it once
// NSE is reachable.
func LoadEmbedded() (*Master, error) {
	f, err := embeddedMaster.Open("data/equity_l.csv")
	if err != nil {
		return nil, fmt.Errorf("company: open embedded master: %w", err)
	}
	defer f.Close()
	return Load(f)
}

// Len reports how many instruments are listed.
func (m *Master) Len() int { return len(m.ordered) }

// All returns every company, ordered by symbol.
func (m *Master) All() []Company { return append([]Company(nil), m.ordered...) }

// Lookup returns one company by exact symbol.
// Lookup accepts either form -- a canonical symbol (RELIANCE.NSE, AAPL) or
// a bare NSE ticker (RELIANCE), which every caller predating venues passes.
// A bare ticker is tried as NSE first and then as US, so the older callers
// keep their exact previous behaviour and a US-only ticker still resolves.
func (m *Master) Lookup(symbol string) (Company, bool) {
	key := strings.ToUpper(strings.TrimSpace(symbol))
	if strings.Contains(key, ".") {
		c, ok := m.bySymbol[key]
		return c, ok
	}
	// Bare: NSE first, deliberately. Every caller predating venues passes an
	// NSE ticker, and a handful of those tickers (INFY, ABB) are also live
	// US listings -- trying US first would silently hand those callers the
	// ADR instead of the NSE line they asked about.
	if c, ok := m.bySymbol[key+".NSE"]; ok {
		return c, true
	}
	c, ok := m.bySymbol[key]
	return c, ok
}

// LookupVenue resolves a bare ticker on one named venue. It exists because
// Lookup cannot: a bare "INFY" is both the canonical US symbol and the NSE
// ticker, so Lookup has to pick one (NSE, for back-compat) and a caller that
// actually knows which venue it means needs a way to say so.
func (m *Master) LookupVenue(symbol, venue string) (Company, bool) {
	key := strings.ToUpper(strings.TrimSpace(symbol))
	if venue == VenueNSE {
		key += ".NSE"
	}
	c, ok := m.bySymbol[key]
	if !ok || (venue != "" && c.Venue != venue) {
		return Company{}, false
	}
	return c, true
}

// indexLeadWords registers the distinctive first word of each company name.
//
// The press rarely prints a full registered name. "Suzlon Energy Limited"
// appears as "Suzlon", and without this the story is lost. What makes it safe
// is that distinctiveness is measured against the listed universe rather than
// asserted: a first word shared by several companies — "Tata", claimed by
// thirteen — indexes nothing at all, so the ambiguous cases eliminate
// themselves without anyone maintaining a list of them.
//
// Words shorter than five characters are skipped as too collision-prone, and
// the resulting matches carry lead-level confidence, below what an alert is
// permitted to fire on.
// indexLeadWords registers the distinctive first word of a company name as
// a lead. Only the companies passed in are eligible: lead words are the
// loosest evidence the resolver accepts, so they are drawn from the curated
// listed universes (NSE's master, the US scan universe) rather than from
// every SEC registrant, where ten thousand names would turn a great many
// ordinary words into company leads.
func (m *Master) indexLeadWords(eligible []Company) {
	owners := make(map[string][]string, len(eligible))
	for _, c := range eligible {
		tokens := tokenize(Normalize(c.Name))
		if len(tokens) < 2 {
			continue // a one-word name is already indexed in full
		}
		owners[tokens[0]] = appendUnique(owners[tokens[0]], c.CanonicalSymbol())
	}
	for word, syms := range owners {
		if len(syms) != 1 || len(word) < 5 {
			continue
		}
		if commonWords[word] || nameBlocklist[word] || tickerBlocklist[strings.ToUpper(word)] {
			continue
		}
		if _, taken := m.byPhrase[word]; taken {
			continue // an exact name or curated alias already owns this word
		}
		m.byPhrase[word] = syms
		m.leadPhrase[word] = true
	}
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

//go:embed data/industry.csv
var embeddedIndustry embed.FS

// Industry returns the NSE industry classification for a symbol.
//
// The listed master carries no sector column, so this comes from NSE's own
// broad-market index, which labels roughly 750 companies across 22
// industries. That covers the investable universe rather than the whole
// listing, which is the right trade: a company outside it is one nobody is
// making a sector call about.
func (m *Master) Industry(symbol string) (string, bool) {
	ind, ok := m.industry[strings.ToUpper(strings.TrimSpace(symbol))]
	return ind, ok
}

// Industries lists every known industry, sorted.
func (m *Master) Industries() []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range m.industry {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// SymbolsInIndustry returns the listed symbols in one industry.
func (m *Master) SymbolsInIndustry(industry string) []string {
	want := strings.ToLower(strings.TrimSpace(industry))
	var out []string
	for sym, ind := range m.industry {
		if strings.ToLower(ind) == want {
			out = append(out, sym)
		}
	}
	sort.Strings(out)
	return out
}

// loadIndustries reads the embedded industry classification.
//
// A failure here is not fatal. Sector inference is an enrichment, and an app
// that refuses to resolve companies because it could not read a sector file
// would be trading a large capability for a small one.
func (m *Master) loadIndustries() {
	m.industry = map[string]string{}
	f, err := embeddedIndustry.Open("data/industry.csv")
	if err != nil {
		return
	}
	defer f.Close()

	cr := csv.NewReader(bufio.NewReader(f))
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToUpper(strings.TrimSpace(h))] = i
	}
	symIdx, okS := col["SYMBOL"]
	indIdx, okI := col["INDUSTRY"]
	if !okS || !okI {
		return
	}
	for {
		rec, err := cr.Read()
		if err != nil {
			return
		}
		if symIdx >= len(rec) || indIdx >= len(rec) {
			continue
		}
		sym := strings.ToUpper(strings.TrimSpace(rec[symIdx]))
		ind := strings.TrimSpace(rec[indIdx])
		if sym != "" && ind != "" {
			m.industry[sym] = ind
		}
	}
}

// industryAliases map the words people use to the labels NSE uses.
//
// Nobody searches for "Construction Materials"; they search for cement. The
// mapping is small and explicit rather than fuzzy, because a wrong industry
// match enumerates the wrong hundred companies.
var industryAliases = map[string]string{
	"cement":         "Construction Materials",
	"concrete":       "Construction Materials",
	"bank":           "Financial Services",
	"banks":          "Financial Services",
	"banking":        "Financial Services",
	"lender":         "Financial Services",
	"lenders":        "Financial Services",
	"nbfc":           "Financial Services",
	"insurance":      "Financial Services",
	"insurer":        "Financial Services",
	"it":             "Information Technology",
	"software":       "Information Technology",
	"tech":           "Information Technology",
	"pharma":         "Healthcare",
	"pharmaceutical": "Healthcare",
	"drug":           "Healthcare",
	"hospital":       "Healthcare",
	"auto":           "Automobile and Auto Components",
	"automobile":     "Automobile and Auto Components",
	"automotive":     "Automobile and Auto Components",
	"car":            "Automobile and Auto Components",
	"steel":          "Metals & Mining",
	"metal":          "Metals & Mining",
	"metals":         "Metals & Mining",
	"mining":         "Metals & Mining",
	"aluminium":      "Metals & Mining",
	"oil":            "Oil Gas & Consumable Fuels",
	"gas":            "Oil Gas & Consumable Fuels",
	"refiner":        "Oil Gas & Consumable Fuels",
	"refining":       "Oil Gas & Consumable Fuels",
	"energy":         "Oil Gas & Consumable Fuels",
	"power":          "Power",
	"electricity":    "Power",
	"utility":        "Utilities",
	"utilities":      "Utilities",
	"fmcg":           "Fast Moving Consumer Goods",
	"consumer":       "Fast Moving Consumer Goods",
	"realty":         "Realty",
	"property":       "Realty",
	"telecom":        "Telecommunication",
	"telco":          "Telecommunication",
	"chemical":       "Chemicals",
	"chemicals":      "Chemicals",
	"fertiliser":     "Chemicals",
	"textile":        "Textiles",
	"textiles":       "Textiles",
	"infrastructure": "Construction",
	"construction":   "Construction",
	"defence":        "Capital Goods",
	"engineering":    "Capital Goods",
	"media":          "Media Entertainment & Publication",
}

// IndustriesMentioned finds the NSE industries a free-text question refers to.
//
// It is deliberately conservative: at most two industries, and only through
// the explicit alias table. Matching loosely would enumerate hundreds of
// companies against a question that named none.
func (m *Master) IndustriesMentioned(text string) []string {
	lower := strings.ToLower(text)
	seen := map[string]bool{}
	var out []string
	for word, industry := range industryAliases {
		if seen[industry] {
			continue
		}
		// Word-boundary matching, so "it" does not fire on "with".
		if !containsWord(lower, word) {
			continue
		}
		seen[industry] = true
		out = append(out, industry)
		if len(out) >= 2 {
			break
		}
	}
	sort.Strings(out)
	return out
}

func containsWord(haystack, word string) bool {
	for i := 0; i+len(word) <= len(haystack); i++ {
		if haystack[i:i+len(word)] != word {
			continue
		}
		beforeOK := i == 0 || !isAlnum(haystack[i-1])
		end := i + len(word)
		afterOK := end == len(haystack) || !isAlnum(haystack[end])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// ScanUniverse returns the symbols worth scanning statistically.
//
// This is the industry-classified list rather than the full equity master,
// and the difference matters for correctness rather than cost. The master
// carries every listed security, including names that trade a few hundred
// shares a day; for those, a "3x average volume" reading is two retail orders
// and a z-score is arithmetic on noise. Restricting the universe to index
// constituents keeps the statistics meaningful.
//
// It also keeps the load modest and predictable, which is the honest position
// to be in given that exchange data carries usage terms.
func (m *Master) ScanUniverse() []string {
	out := make([]string, 0, len(m.industry))
	for symbol := range m.industry {
		// NSE's own constituent file carries placeholder rows (DUMMYINXGN,
		// DUMMYTRVN) which are not tradeable and resolve to nothing. Left in,
		// they show up as failed lookups on every scan forever.
		if strings.HasPrefix(symbol, "DUMMY") {
			continue
		}
		out = append(out, symbol)
	}
	sort.Strings(out)
	return out
}

// IndexConstituents maps each index member to its industry.
//
// The same list ScanUniverse walks, in the shape the store wants. Both come
// from the constituent file rather than the full equity master, so what the
// scanner considers worth measuring and what the feed considers worth showing
// cannot drift apart.
func (m *Master) IndexConstituents() map[string]string {
	out := make(map[string]string, len(m.industry))
	for symbol, industry := range m.industry {
		if strings.HasPrefix(symbol, "DUMMY") {
			continue
		}
		out[symbol] = industry
	}
	return out
}
