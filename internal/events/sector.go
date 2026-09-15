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

// InferSectors returns the industries a macro or policy headline bears on.
//
// The result is capped, because an event that appears to touch ten sectors has
// almost certainly matched on a common word rather than genuinely reached
// them all, and attaching it to every listed company would drown the feed.
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
	const maxSectors = 4
	if len(out) > maxSectors {
		out = out[:maxSectors]
	}
	sort.Strings(out)
	return out
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
