package events

import (
	"sort"
	"strings"
)

// Sector exposure is how a macro or policy event reaches companies it never
// names: a bank-capital rule names no bank, a crude move names no refiner. It
// goes through an explicit, reviewable keyword map rather than a model, and a
// conservative one, because a false sector link attaches an event to dozens
// of companies at once.
//
// Values are GICS sectors prefixed "US: ", the spelling stored on events and
// read by the UI and SectorsFor.
var sectorKeywords = map[string][]string{
	// The Fed, rates, banking.
	"federal reserve":     {"US: Financials"},
	"fomc":                {"US: Financials"},
	"interest rate":       {"US: Financials", "US: Real Estate"},
	"fed funds rate":      {"US: Financials"},
	"quantitative easing": {"US: Financials"},
	"bank capital":        {"US: Financials"},
	"stress test":         {"US: Financials"},
	"basel":               {"US: Financials"},
	"insurance":           {"US: Financials"},
	"derivatives":         {"US: Financials"},

	// Trade and tariffs: the largest category of action that reprices whole
	// sectors at once.
	"tariff":         {"US: Industrials", "US: Materials", "US: Consumer Discretionary"},
	"import duty":    {"US: Materials", "US: Industrials"},
	"anti-dumping":   {"US: Materials", "US: Industrials"},
	"section 301":    {"US: Industrials", "US: Materials"},
	"section 232":    {"US: Materials", "US: Industrials"},
	"export control": {"US: Information Technology", "US: Industrials"},
	"entity list":    {"US: Information Technology", "US: Industrials"},
	"sanctions":      {"US: Energy", "US: Financials"},

	// Energy and commodities. Direction is decided per company, never per
	// event: crude up is good for a producer and bad for an airline.
	"crude oil":                   {"US: Energy"},
	"brent":                       {"US: Energy"},
	"opec":                        {"US: Energy"},
	"gasoline":                    {"US: Energy"},
	"natural gas":                 {"US: Energy", "US: Utilities"},
	"strategic petroleum reserve": {"US: Energy"},
	"coal":                        {"US: Energy", "US: Utilities"},
	"renewable":                   {"US: Utilities", "US: Industrials"},
	"solar":                       {"US: Utilities", "US: Industrials"},
	"electricity":                 {"US: Utilities"},
	"grid":                        {"US: Utilities"},
	"steel":                       {"US: Materials"},
	"iron ore":                    {"US: Materials"},
	"aluminum":                    {"US: Materials"},
	"aluminium":                   {"US: Materials"},
	"copper":                      {"US: Materials"},
	"mining":                      {"US: Materials"},
	"fertilizer":                  {"US: Materials"},

	// Health care and drug policy.
	"fda":            {"US: Health Care"},
	"drug pricing":   {"US: Health Care"},
	"pharmaceutical": {"US: Health Care"},
	"medicare":       {"US: Health Care"},
	"clinical trial": {"US: Health Care"},

	// Tech and telecom regulation.
	"antitrust":               {"US: Information Technology", "US: Communication Services"},
	"fcc":                     {"US: Communication Services"},
	"spectrum":                {"US: Communication Services"},
	"telecom":                 {"US: Communication Services"},
	"semiconductor":           {"US: Information Technology"},
	"artificial intelligence": {"US: Information Technology"},
	"data privacy":            {"US: Information Technology", "US: Communication Services"},
	"h-1b":                    {"US: Information Technology"},

	// Industrials, autos, housing.
	"faa":                {"US: Industrials"},
	"defense department": {"US: Industrials"},
	"pentagon":           {"US: Industrials"},
	"infrastructure":     {"US: Industrials", "US: Materials"},
	"railroad":           {"US: Industrials"},
	"automaker":          {"US: Consumer Discretionary"},
	"emission standard":  {"US: Consumer Discretionary", "US: Industrials"},
	"mortgage rate":      {"US: Real Estate", "US: Financials"},
	"housing starts":     {"US: Real Estate", "US: Industrials"},
	"real estate":        {"US: Real Estate"},

	// Market plumbing.
	"sec rule":        {"US: Financials"},
	"sec enforcement": {"US: Financials"},
	"ipo":             {"US: Financials"},
	"buyback":         {"US: Financials"},
}

// sortedKeywords orders the keywords longest-first, so a specific phrase is
// tested before a general one it contains.
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

// InferSectors returns the sectors a macro or policy headline bears on,
// capped at four: an event that appears to touch many sectors has almost
// certainly matched a common word rather than reached them all.
func InferSectors(text string) []string {
	lower := strings.ToLower(text)
	seen := map[string]bool{}
	var out []string
	for _, kw := range sortedKeywords {
		if !strings.Contains(lower, kw) {
			continue
		}
		for _, sector := range sectorKeywords[kw] {
			if !seen[sector] {
				seen[sector] = true
				out = append(out, sector)
			}
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
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
