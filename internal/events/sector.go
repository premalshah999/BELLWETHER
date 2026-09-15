package events

import (
	"sort"
	"strings"
)

// Sector exposure is how a macro or policy event reaches companies that it
// never names.
//
// An RBI circular on bank provisioning names no bank. A crude oil move names
// no refiner. Reaching the affected companies means going through the
// industry, which is what this file does — and doing so with an explicit,
// reviewable mapping rather than by asking a model to free-associate.

// sectorKeywords maps words appearing in macro and policy headlines to the NSE
// industry classifications they bear on.
//
// The mapping is intentionally conservative. A false sector link attaches an
// event to dozens of companies at once, so it is far more damaging than a
// false link to a single company, and anything ambiguous is left out.
var sectorKeywords = map[string][]string{
	// Rates and banking. The RBI's policy corridor reaches lenders first and
	// most directly.
	"repo rate":         {"Financial Services"},
	"reverse repo":      {"Financial Services"},
	"monetary policy":   {"Financial Services", "Realty"},
	"crr":               {"Financial Services"},
	"slr":               {"Financial Services"},
	"provisioning":      {"Financial Services"},
	"npa":               {"Financial Services"},
	"bad loan":          {"Financial Services"},
	"lending rate":      {"Financial Services", "Realty"},
	"basel":             {"Financial Services"},
	"priority sector":   {"Financial Services"},
	"microfinance":      {"Financial Services"},
	"nbfc":              {"Financial Services"},
	"co-operative bank": {"Financial Services"},
	"housing finance":   {"Financial Services", "Realty"},
	"insurance":         {"Financial Services"},

	// Energy and commodities. Crude is the clearest example of an input whose
	// direction is opposite for producers and consumers, which is why
	// direction is decided per company and never per event.
	"crude oil":    {"Oil Gas & Consumable Fuels"},
	"brent":        {"Oil Gas & Consumable Fuels"},
	"opec":         {"Oil Gas & Consumable Fuels"},
	"natural gas":  {"Oil Gas & Consumable Fuels", "Utilities"},
	"petrol":       {"Oil Gas & Consumable Fuels"},
	"diesel":       {"Oil Gas & Consumable Fuels"},
	"lpg":          {"Oil Gas & Consumable Fuels"},
	"coal":         {"Oil Gas & Consumable Fuels", "Power"},
	"electricity":  {"Power", "Utilities"},
	"power tariff": {"Power", "Utilities"},
	"renewable":    {"Power"},
	"solar":        {"Power", "Capital Goods"},

	// Metals.
	"steel":     {"Metals & Mining"},
	"iron ore":  {"Metals & Mining"},
	"aluminium": {"Metals & Mining"},
	"copper":    {"Metals & Mining"},
	"zinc":      {"Metals & Mining"},
	"mining":    {"Metals & Mining"},

	// Trade policy. A duty change is the most reliably sector-wide event
	// there is, which is why the tariff terms map broadly.
	"import duty":  {"Metals & Mining", "Chemicals", "Consumer Durables"},
	"export duty":  {"Metals & Mining", "Chemicals"},
	"customs duty": {"Metals & Mining", "Chemicals", "Consumer Durables"},
	"anti-dumping": {"Metals & Mining", "Chemicals"},
	"tariff":       {"Metals & Mining", "Chemicals", "Textiles"},
	"gst":          {"Fast Moving Consumer Goods", "Consumer Durables", "Automobile and Auto Components"},

	// Sector regulators.
	"pharmaceutical":        {"Healthcare"},
	"usfda":                 {"Healthcare"},
	"drug price":            {"Healthcare"},
	"nppa":                  {"Healthcare"},
	"telecom":               {"Telecommunication"},
	"trai":                  {"Telecommunication"},
	"spectrum":              {"Telecommunication"},
	"agr dues":              {"Telecommunication"},
	"real estate":           {"Realty"},
	"rera":                  {"Realty"},
	"automobile":            {"Automobile and Auto Components"},
	"vehicle scrappage":     {"Automobile and Auto Components"},
	"emission norm":         {"Automobile and Auto Components"},
	"semiconductor":         {"Information Technology", "Consumer Durables"},
	"h-1b":                  {"Information Technology"},
	"visa fee":              {"Information Technology"},
	"cement":                {"Construction Materials"},
	"infrastructure":        {"Construction", "Capital Goods"},
	"defence":               {"Capital Goods"},
	"railway":               {"Capital Goods", "Construction"},
	"textile":               {"Textiles"},
	"cotton":                {"Textiles"},
	"fertiliser":            {"Chemicals"},
	"fertilizer":            {"Chemicals"},
	"monsoon":               {"Fast Moving Consumer Goods", "Chemicals"},
	"minimum support price": {"Fast Moving Consumer Goods"},

	// Market plumbing. SEBI rules on flows and settlement reach the industry
	// that intermediates them.
	"mutual fund":       {"Financial Services"},
	"fpi":               {"Financial Services"},
	"foreign portfolio": {"Financial Services"},
	"derivatives":       {"Financial Services"},
	"settlement cycle":  {"Financial Services"},
	"ipo":               {"Financial Services"},
}

// gicsKeywords is sectorKeywords' US counterpart: the same discipline
// (conservative, reviewable, no model), mapped onto GICS sectors instead of
// NSE's industry classification.
//
// Every value here is prefixed "US: ". Two GICS sector names -- "Information
// Technology" and "Utilities" -- are spelled identically to NSE industry
// names, so an unprefixed value would silently collide the moment both
// matched the same headline (this is the same class of bug ComparePeers had
// before it was scoped by taxonomy: two different vocabularies that
// sometimes spell a category the same way). The prefix also means a sector
// filter showing both venues side by side reads as two distinct options
// rather than one that mixes two different companies' worth of exposure.
var gicsKeywords = map[string][]string{
	// The Fed, rates, banking.
	"federal reserve":     {"US: Financials"},
	"fomc":                {"US: Financials"},
	"interest rate":       {"US: Financials", "US: Real Estate"},
	"fed funds rate":      {"US: Financials"},
	"quantitative easing": {"US: Financials"},
	"bank capital":        {"US: Financials"},
	"stress test":         {"US: Financials"},
	"basel":               {"US: Financials"},

	// Trade and tariffs -- the single largest category of Federal Register
	// action that reprices whole sectors at once.
	"tariff":         {"US: Industrials", "US: Materials", "US: Consumer Discretionary"},
	"section 301":    {"US: Industrials", "US: Materials"},
	"section 232":    {"US: Materials", "US: Industrials"},
	"export control": {"US: Information Technology", "US: Industrials"},
	"entity list":    {"US: Information Technology", "US: Industrials"},
	"sanctions":      {"US: Energy", "US: Financials"},

	// Energy and commodities.
	"crude oil":                   {"US: Energy"},
	"opec":                        {"US: Energy"},
	"natural gas":                 {"US: Energy", "US: Utilities"},
	"strategic petroleum reserve": {"US: Energy"},
	"renewable":                   {"US: Utilities", "US: Industrials"},
	"solar":                       {"US: Utilities", "US: Industrials"},
	"electricity":                 {"US: Utilities"},
	"grid":                        {"US: Utilities"},

	// Healthcare and drug policy.
	"fda":            {"US: Health Care"},
	"drug pricing":   {"US: Health Care"},
	"medicare":       {"US: Health Care"},
	"clinical trial": {"US: Health Care"},

	// Tech and telecom regulation.
	"antitrust":               {"US: Information Technology", "US: Communication Services"},
	"fcc":                     {"US: Communication Services"},
	"spectrum":                {"US: Communication Services"},
	"semiconductor":           {"US: Information Technology"},
	"artificial intelligence": {"US: Information Technology"},
	"data privacy":            {"US: Information Technology", "US: Communication Services"},

	// Aerospace and defense.
	"faa":                {"US: Industrials"},
	"defense department": {"US: Industrials"},
	"pentagon":           {"US: Industrials"},

	// Housing and real estate.
	"mortgage rate":  {"US: Real Estate", "US: Financials"},
	"housing starts": {"US: Real Estate", "US: Industrials"},

	// Broad market plumbing.
	"sec rule":        {"US: Financials"},
	"sec enforcement": {"US: Financials"},
	"ipo":             {"US: Financials"},
	"buyback":         {"US: Financials"},
}

var sortedGICSKeywords = func() []string {
	out := make([]string, 0, len(gicsKeywords))
	for k := range gicsKeywords {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}()

// sortedKeywords is the keyword list ordered longest-first, so that a
// specific phrase is tested before a general one it contains. Without this,
// "coal" inside "coal india" would match before a more specific rule could.
var sortedKeywords = func() []string {
	out := make([]string, 0, len(sectorKeywords))
	for k := range sectorKeywords {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}()

// InferSectors returns the industries a macro or policy headline bears on,
// across both venues' taxonomies at once -- a headline about crude oil
// touches NSE's "Oil Gas & Consumable Fuels" companies and reads exactly as
// well against "US: Energy" ones, and there is no reason to pick one
// taxonomy over the other from the headline alone.
//
// The result is capped, because an event that appears to touch many sectors
// has almost certainly matched on a common word rather than genuinely
// reached them all, and attaching it to every listed company would drown
// the feed. The cap applies per taxonomy, not to the combined list, so a
// broad US policy story is not crowded out of its own sectors by an
// unrelated NSE keyword match earlier in the (alphabetically sorted, so
// arbitrary) keyword list.
func InferSectors(text string) []string {
	lower := strings.ToLower(text)
	const maxSectors = 4

	match := func(sorted []string, table map[string][]string) []string {
		seen := map[string]bool{}
		var out []string
		for _, kw := range sorted {
			if !strings.Contains(lower, kw) {
				continue
			}
			for _, sector := range table[kw] {
				if !seen[sector] {
					seen[sector] = true
					out = append(out, sector)
				}
			}
		}
		if len(out) > maxSectors {
			out = out[:maxSectors]
		}
		return out
	}

	out := append(match(sortedKeywords, sectorKeywords), match(sortedGICSKeywords, gicsKeywords)...)
	sort.Strings(out)
	return out
}

// SectorsForAgency returns the GICS sectors a Federal Register issuing
// agency's own actions reach, using the agency as structured metadata
// rather than inferring it from free text -- USTR notices are trade
// actions regardless of how any one notice happens to be worded, which the
// keyword map above cannot know without seeing the word "tariff" in the
// title. Falls back to nil (not found), at which point a caller should try
// InferSectors on the document's own title instead.
//
// Verified against a live Federal Register query (306 matching documents,
// 2026-09): "Notice of Actions in Section 301 Investigations" (USTR),
// "Uyghur Forced Labor Prevention Act Entity List" (DHS), "Interference-
// Tolerant Radio Altimeter Systems" (FAA).
var agencySectors = map[string][]string{
	"Office of the United States Trade Representative": {"US: Industrials", "US: Materials", "US: Consumer Discretionary"},
	"International Trade Administration":               {"US: Industrials", "US: Materials"},
	"Federal Reserve System":                           {"US: Financials"},
	"Securities and Exchange Commission":               {"US: Financials"},
	"Commodity Futures Trading Commission":             {"US: Financials"},
	"Federal Deposit Insurance Corporation":            {"US: Financials"},
	"Comptroller of the Currency":                      {"US: Financials"},
	"Federal Communications Commission":                {"US: Communication Services"},
	"Federal Aviation Administration":                  {"US: Industrials"},
	"Federal Energy Regulatory Commission":             {"US: Utilities", "US: Energy"},
	"Energy Department":                                {"US: Energy", "US: Utilities"},
	"Environmental Protection Agency":                  {"US: Materials", "US: Industrials", "US: Utilities"},
	"Food and Drug Administration":                     {"US: Health Care"},
	"Centers for Medicare & Medicaid Services":         {"US: Health Care"},
	"Homeland Security Department":                     {"US: Industrials", "US: Information Technology"},
	"Treasury Department":                              {"US: Financials"},
	"Federal Trade Commission":                         {"US: Information Technology", "US: Communication Services"},
	"National Highway Traffic Safety Administration":   {"US: Consumer Discretionary", "US: Industrials"},
	"Federal Housing Finance Agency":                   {"US: Real Estate", "US: Financials"},
	"Agriculture Department":                           {"US: Consumer Staples", "US: Materials"},
	"Interior Department":                              {"US: Energy", "US: Materials"},
	"Labor Department":                                 {"US: Industrials"},
}

// SectorsForAgency looks up the sectors a Federal Register agency's actions
// reach. ok is false when the agency is not in the table, distinguishing
// "checked and found nothing" from "this agency has no sector reach" --
// nothing in the table is assumed to be exhaustive.
func SectorsForAgency(agency string) (sectors []string, ok bool) {
	s, found := agencySectors[strings.TrimSpace(agency)]
	return s, found
}

// SectorScope reports whether a type is one whose reach is sectoral rather
// than company-specific. Only these are expanded to industries: a company's
// own order win is not a sector event however it is worded.
func (t Type) SectorScope() bool {
	switch t {
	case TypeMacroEvent, TypeRegulatoryPolicy, TypeCommodityEvent,
		TypeGeopoliticalEvent, TypeSectorEvent:
		return true
	}
	return false
}

// SectorScopeTypes lists the types SectorScope admits.
//
// Exported so a query can express "macro items are exempt from company
// filters" without restating the taxonomy in SQL, where it would drift out of
// step the first time a type is added here.
func SectorScopeTypes() []string {
	return []string{
		string(TypeMacroEvent), string(TypeRegulatoryPolicy),
		string(TypeCommodityEvent), string(TypeGeopoliticalEvent),
		string(TypeSectorEvent),
	}
}
