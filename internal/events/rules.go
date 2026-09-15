package events

import (
	"regexp"
	"strings"
	"unicode"
)

// Cheap keyword classification, applied before any model sees an item.
//
// Exchange filings state their own category, so they need none of this. News
// does not: a Reuters headline arrives as prose, and without a first pass it
// would sit as UNCLASSIFIED until the hourly classifier reached it. For a
// system where timing is the point, an event that exists only after the model
// has thought about it is an event that arrived late.
//
// So this runs in microseconds, gets the common cases right, and is explicitly
// allowed to be wrong at the margins — the model refines it afterwards, and
// the confidence attached here says how much to trust it in the meantime.

// rule is one keyword pattern and what it implies.
type rule struct {
	pattern *regexp.Regexp
	typ     Type
	// bump adjusts importance away from the type's baseline when the wording
	// itself carries weight: "unexpectedly resigns" is not "retires".
	bump int
}

// compile builds a word-boundary-anchored, case-insensitive matcher.
//
// Anchoring on word boundaries matters more than it looks: without it "order"
// matches "disorder" and "recorder", and "merger" matches "emerger". Those are
// exactly the false positives that make a keyword pass look untrustworthy.
func compile(alternatives ...string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(alternatives, "|") + `)\b`)
}

// headlineRules are ordered: the first match wins.
//
// Order encodes precedence rather than being incidental. "Company wins order
// after regulator lifts ban" is primarily a regulatory story, so the trouble
// patterns come before the commercial ones.
var headlineRules = []rule{
	// Trouble first. These change an investment case rather than adjusting it.
	{compile("insolvency", "bankruptcy", "liquidation", "winding up", "NCLT"), TypeInsolvency, 2},
	// Both orderings occur: "auditor resigns" and "resigns as auditor". The
	// second is more common in practice, because the headline names the audit
	// firm first.
	{compile("auditor resign(s|ed|ation)?", "resign(s|ed)? as (?:the )?(?:statutory )?auditor",
		"qualified opinion", "adverse opinion", "audit qualification", "auditor (?:change|replaced)"), TypeAuditorChange, 2},
	// "fined", "penalty" and "non-compliance" were all missing, and between
	// them they cover the most common wording of an exchange penalty:
	// "Ircon fined Rs 9.66 lakh each by NSE, BSE for board non-compliance".
	{compile("SEBI (?:bars|debars|penalis|penaliz|orders|fines)", "show cause notice",
		"regulatory action", "banned by", "debarred", "fined", "penalty", "penalised", "penalized",
		"non[- ]compliance", "violat(es|ed|ion)"), TypeRegulatoryAction, 2},
	{compile("raid(s|ed)?", "search and seizure", "tax demand", "GST demand", "income tax notice"), TypeTaxAction, 1},
	{compile("lawsuit", "litigation", "sues", "sued", "court order", "arbitration", "tribunal"), TypeLitigation, 0},
	{compile("downgrade(s|d)?", "upgrade(s|d)?", "credit rating", "rating action", "outlook revised"), TypeCreditRating, 0},

	// People. A chief executive leaving is high-importance and unclear in
	// direction, which is precisely the combination this system is built to
	// represent honestly.
	{compile("CEO", "chief executive", "managing director", "CFO", "chief financial officer", "chairman"), TypeManagementChange, 1},
	{compile("resign(s|ed|ation)?", "steps down", "quits", "exits"), TypeManagementChange, 1},
	{compile("appoint(s|ed|ment)?", "elevated to", "takes charge", "named as"), TypeManagementChange, 0},

	// Structure.
	{compile("acquir(e|es|ed|ing|ition)", "takeover", "buys stake", "acquisition"), TypeAcquisition, 1},
	{compile("merger", "merges", "amalgamation"), TypeMerger, 1},
	{compile("demerger", "demerges", "spin[- ]?off", "scheme of arrangement"), TypeDemerger, 1},
	{compile("stake sale", "sells stake", "divest(s|ed|ment)?", "block deal", "offer for sale"), TypeStakeSale, 0},

	// Results and returns.
	{compile("Q[1-4] results", "quarterly results", "net profit", "revenue (?:rose|fell|up|down)", "earnings", "PAT", "EBITDA"), TypeEarnings, 1},
	{compile("guidance", "outlook (?:raised|cut|lowered)", "forecast(s|ed)?"), TypeGuidance, 1},
	{compile("dividend", "interim dividend", "final dividend"), TypeDividend, 0},
	{compile("buyback", "buy[- ]back", "share repurchase"), TypeBuyback, 1},
	{compile("bonus issue", "stock split", "share split", "rights issue"), TypeBonus, 0},

	// Commercial activity.
	{compile("bags", "wins (?:an? )?(?:order|contract|deal)", "order win",
		"secures (?:an? )?(?:order|contract|deal)", "awarded (?:an? )?contract",
		"lands (?:[\\w-]+ ){0,3}(?:order|contract|deal)", "L1 bidder",
		"receives (?:an? )?order"), TypeOrderWin, 1},
	{compile("contract", "agreement", "MoU", "partnership", "tie[- ]?up", "joint venture"), TypeContract, 0},
	{compile("order cancel(led|ed)?", "contract terminated", "deal called off"), TypeOrderCancelled, 1},

	// Money.
	{compile("fund rais(e|ing)", "QIP", "raises (?:Rs|₹|\\$)", "IPO", "preferential allotment"), TypeFundRaise, 0},
	{compile("pledge(d|s)?", "encumbrance", "promoter (?:selling|buying|stake)"), TypePledge, 1},
	{compile("bond(s)? issue", "NCD", "debenture", "debt raise", "refinanc(e|ing)"), TypeDebt, 0},

	// Investment.
	{compile("capex", "capital expenditure", "new plant", "expansion", "greenfield", "brownfield"), TypeCapex, 0},
	{compile("launch(es|ed)?", "new product", "unveils"), TypeNewProduct, 0},

	// Above the company.
	{compile("repo rate", "monetary policy", "RBI (?:cuts|raises|holds)", "inflation", "GDP", "IIP", "CPI", "WPI"), TypeMacroEvent, 0},
	{compile("crude", "brent", "OPEC", "oil price"), TypeCommodityEvent, 0},
	{compile("tariff", "sanction(s)?", "war", "geopolitic", "trade restriction", "export ban"), TypeGeopoliticalEvent, 0},
	{compile("circular", "regulation", "guidelines", "SEBI (?:proposes|notifies)", "RBI (?:notifies|issues)"), TypeRegulatoryPolicy, 0},
}

// FastClassification is the deterministic first reading of an item.
type FastClassification struct {
	Type       Type
	Importance int
	// Confidence is how much the keyword pass trusts itself. It is
	// deliberately modest: this exists to make an event available
	// immediately, not to be the final word.
	Confidence float64
	// Matched is the phrase that triggered it, so a wrong classification can
	// be traced to the rule that caused it rather than guessed at.
	Matched string
}

// urgentPattern marks wording that means an item should skip the queue.
//
// Not a classification — an item can be urgent and of unclear type. What it
// controls is ordering: these reach the model first, and their companies are
// marked eventful immediately rather than after the next processing pass.
var urgentPattern = compile(
	"breaking", "just in", "halt(ed|s)? trading", "trading halt(ed|s)?", "circuit breaker",
	"resign(s|ed)", "steps down", "insolvency", "bankruptcy", "fraud",
	"SEBI bars", "raid", "default(s|ed)?", "downgrade", "emergency",
)

// ClassifyHeadline gives an item a type and an importance without a model.
//
// The second return reports whether any rule matched at all. An unmatched item
// is left UNCLASSIFIED rather than being forced into the nearest category:
// "UNCLASSIFIED, awaiting the model" is honest, and a wrong confident label is
// worse than no label.
func ClassifyHeadline(headline, summary string) (FastClassification, bool) {
	text := headline
	if summary != "" && summary != headline {
		// Only the first part of the summary is considered. Later sentences
		// are context, and matching on them produces classifications about
		// something the headline was not about.
		text += ". " + firstSentences(summary, 2)
	}

	for _, r := range headlineRules {
		loc := r.pattern.FindStringIndex(text)
		if loc == nil {
			continue
		}
		importance := r.typ.BaselineImportance() + r.bump
		if importance > 10 {
			importance = 10
		}
		if importance < 0 {
			importance = 0
		}
		// A match in the headline is stronger evidence than one in the
		// summary, because a headline is about the thing and a summary
		// mentions it.
		confidence := 0.45
		if loc[0] < len(headline) {
			confidence = 0.6
		}
		return FastClassification{
			Type: r.typ, Importance: importance, Confidence: confidence,
			Matched: text[loc[0]:loc[1]],
		}, true
	}
	return FastClassification{Type: TypeUnclassified, Importance: TypeUnclassified.BaselineImportance()}, false
}

// Urgent reports whether an item should be prioritised ahead of the queue.
func Urgent(headline, summary string) bool {
	if urgentPattern.MatchString(headline) {
		return true
	}
	return urgentPattern.MatchString(firstSentences(summary, 1))
}

// firstSentences returns at most n sentences from a string.
func firstSentences(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	count := 0
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '.' && (s[i+1] == ' ' || s[i+1] == '\n') {
			count++
			if count >= n {
				return s[:i+1]
			}
		}
	}
	return s
}

// Two tiers of irrelevance, because they behave differently.
//
// hardMarketNoise is coverage of a foreign market's own session or a retail
// technical signal. Nothing rescues it: a US index wrap that happens to
// mention inflation is still a US index wrap, and treating the word as an
// escape hatch let "Dow Jones | Nasdaq | S&P 500 | US Stock Market Today"
// through on exactly that basis.
var hardMarketNoise = compile(
	"Dow Jones", "S&P 500", "Nasdaq (?:composite|100)?", "Wall Street",
	"US stock(?:s| market)", "U\\.S\\. stock(?:s| market)",
	"FTSE", "DAX", "Nikkei", "Hang Seng", "Euro Stoxx",
	"52-week (?:high|low)", "moving average cross", "RSI alert",
	"oversold", "overbought", "intrinsic value", "price target (?:raised|cut)",
	"analyst (?:upgrades|downgrades) stock",
)

// softMarketNoise is coverage of a foreign company. It is dropped only when
// nothing in the item connects it to India or to an input Indian companies
// depend on — "Apple launches Mac mini" goes, "Apple expands India
// manufacturing" stays.
var softMarketNoise = compile(
	"Nvidia", "Tesla", "Netflix", "Meta Platforms", "Alphabet", "Apple",
	"Berkshire", "Goldman Sachs", "JPMorgan", "Walmart", "Boeing", "Honda",
	"Palantir", "Intuit", "Salesforce", "Oracle Corp", "Cisco", "Alibaba",
	"Samsung", "Toyota", "Volkswagen", "Ford Motor", "General Motors",
)

// commodityOrPolicy marks the foreign subjects that do reach Indian equities.
//
// The list is inputs and rules rather than places: crude, gas, metals, duties,
// rates and the rupee. A foreign story touching one of these reaches Indian
// companies through their cost base or their market access, which is exactly
// the connection worth keeping.
var commodityOrPolicy = compile(
	"crude", "brent", "OPEC", "oil price", "natural gas", "LNG",
	"gold", "silver", "copper", "steel", "coal", "palm oil", "sunflower oil", "fertiliser",
	"tariff", "sanction(s)?", "trade (?:war|deal|deficit)", "import (?:duty|price)", "export ban",
	"interest rate", "rate (?:cut|hike)", "recession",
	"rupee", "dollar index", "USD/INR", "FPI", "FII", "foreign (?:portfolio|investor)",
	"India", "Indian",
)

// NotReadableHere reports that a headline is not in the script this feed is
// written in.
//
// This is a statement about the product, not about the language. The feed, the
// classifier's prompts and the entity resolver are all English and Latin-script
// end to end, so an item in another script cannot be resolved to a company,
// cannot be sensibly ranked against its neighbours, and cannot be read by the
// people using it — while still competing for the top of the list on whatever
// importance the classifier assigns its subject matter. A Hindi notice about a
// bus compliance certificate outranking a merger is what that looks like.
//
// The threshold is deliberately high. Indian English headlines routinely carry
// a rupee sign, a name in Devanagari, or a quoted phrase, and none of those
// make an otherwise English headline unreadable.
func NotReadableHere(headline string) bool {
	var latin, other int
	for _, r := range headline {
		switch {
		case unicode.IsSpace(r), unicode.IsDigit(r), unicode.IsPunct(r), unicode.IsSymbol(r):
			// Carries no script information either way.
		case r < unicode.MaxASCII && unicode.IsLetter(r):
			latin++
		case unicode.IsLetter(r):
			other++
		}
	}
	if latin+other == 0 {
		return false
	}
	return float64(other)/float64(latin+other) > 0.5
}

// Unactionable reports whether an item is market coverage with no reachable
// bearing on Indian equities.
//
// Only consulted for events that resolved to no Indian company; anything
// naming a listed company is kept however it is worded.
func Unactionable(headline, summary string) bool {
	text := headline + ". " + firstSentences(summary, 1)
	if hardMarketNoise.MatchString(text) {
		return true
	}
	if !softMarketNoise.MatchString(text) {
		return false
	}
	return !commodityOrPolicy.MatchString(text)
}
