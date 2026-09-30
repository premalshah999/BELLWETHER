package events

import (
	"regexp"
	"strings"
	"unicode"
)

// Cheap keyword classification, applied before any model sees an item, so a
// headline is typed when it arrives rather than when the hourly classifier
// reaches it. It gets the common cases right in microseconds and may be wrong
// at the margins: the model refines it afterwards.

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
	{compile("insolvency", "bankruptcy", "liquidation", "winding up",
		"chapter (?:7|11|15)", "receivership", "files for chapter", "debtor[- ]in[- ]possession"), TypeInsolvency, 2},
	// Both orderings occur: "auditor resigns" and "resigns as auditor". The
	// second is more common in practice, because the headline names the audit
	// firm first.
	{compile("auditor resign(s|ed|ation)?", "resign(s|ed)? as (?:the )?(?:statutory )?auditor",
		"qualified opinion", "adverse opinion", "audit qualification", "auditor (?:change|replaced)"), TypeAuditorChange, 2},
	// "fined", "penalty" and "non-compliance" cover the most common wording of
	// an enforcement headline.
	{compile("(?:SEC|FTC|DOJ|CFTC|FINRA|OSHA|EPA|FDA) (?:bars|debars|charges|sues|fines|orders|penalis|penaliz)",
		"show cause notice", "regulatory action", "banned by", "debarred", "fined", "penalty",
		"penalised", "penalized", "non[- ]compliance", "violat(es|ed|ion)",
		"enforcement action", "consent order", "cease and desist", "wells notice",
		"deferred prosecution", "settles with the (?:SEC|FTC|DOJ)"), TypeRegulatoryAction, 2},
	{compile("raid(s|ed)?", "search and seizure", "tax demand", "income tax notice",
		"IRS (?:assess|audit|notice|demand|dispute)", "tax assessment", "back taxes",
		"transfer pricing (?:dispute|adjustment)"), TypeTaxAction, 1},
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
	{compile("stake sale", "sells (?:[\\w-]+ ){0,3}stake", "divest(s|ed|ment)?", "block deal",
		"offer for sale", "secondary offering", "trims (?:its )?(?:[\\w-]+ ){0,2}stake",
		"exits (?:its )?(?:[\\w-]+ ){0,2}(?:stake|position)"), TypeStakeSale, 0},

	// Results and returns.
	// Guidance before earnings, because the table is evaluated in order and
	// the first match wins. "3M cuts its full-year earnings guidance" is a
	// guidance story that happens to contain the word "earnings"; with
	// earnings first it was filed as a results announcement. The bump beside
	// each rule is importance, not precedence -- only position decides which
	// rule wins.
	{compile("guidance", "outlook (?:raised|cut|lowered|reaffirmed)", "forecast(s|ed)?",
		"(?:raises|cuts|lowers|reaffirms|withdraws) (?:its )?(?:full[- ]year|FY|Q[1-4]|annual)",
		"pre[- ]announce"), TypeGuidance, 1},
	{compile("Q[1-4] results", "quarterly results", "net profit", "revenue (?:rose|fell|up|down)", "earnings", "PAT", "EBITDA"), TypeEarnings, 1},
	{compile("dividend", "interim dividend", "final dividend"), TypeDividend, 0},
	{compile("buyback", "buy[- ]back", "share repurchase"), TypeBuyback, 1},
	{compile("bonus issue", "stock split", "share split", "rights issue"), TypeBonus, 0},

	// Commercial activity.
	{compile("bags", "wins (?:an? )?(?:order|contract|deal)", "order win",
		"secures (?:an? )?(?:order|contract|deal)", "awarded (?:an? )?contract",
		"lands (?:[\\w-]+ ){0,3}(?:order|contract|deal)", "L1 bidder",
		"receives (?:an? )?order"), TypeOrderWin, 1},
	{compile("contract", "agreement", "MoU", "partnership", "tie[- ]?up", "joint venture"), TypeContract, 0},
	{compile("order cancel(led|ed)?", "cancel(?:s|led|ed)? (?:an? |its )?(?:[\\w-]+ ){0,3}(?:order|contract)",
		"contract terminated", "terminates (?:an? |its )?(?:[\\w-]+ ){0,2}(?:contract|agreement)",
		"deal called off", "deal collapses", "walks away from"), TypeOrderCancelled, 1},

	// Money.
	{compile("fund rais(e|ing)", "raises (?:Rs|₹|\\$)", "IPO", "preferential allotment",
		"(?:secondary|follow[- ]on|equity) offering", "at[- ]the[- ]market offering",
		"private placement", "PIPE (?:deal|financing)", "convertible note"), TypeFundRaise, 0},
	{compile("pledge(d|s)?", "encumbrance", "promoter (?:selling|buying|stake)"), TypePledge, 1},
	{compile("bond(s)? issue", "debt raise", "refinanc(e|ing)",
		"(?:senior|unsecured|secured|convertible) notes", "notes offering", "prices (?:a |an )?\\$",
		"credit facility", "revolving credit", "term loan", "coupon", "tender offer for"), TypeDebt, 0},

	// Investment.
	{compile("capex", "capital expenditure", "new (?:[\\w-]+ ){0,2}(?:plant|fab|facility|factory|mill)",
		"expansion", "greenfield", "brownfield", "will invest (?:Rs|₹|\\$)",
		"breaks ground", "production capacity"), TypeCapex, 0},
	{compile("launch(es|ed)?", "new product", "unveils"), TypeNewProduct, 0},

	// Above the company.
	{compile("monetary policy", "inflation", "GDP", "CPI", "PPI", "PCE",
		"federal funds rate", "fed funds", "FOMC", "(?:the )?Fed (?:cuts|raises|holds|hikes)",
		"nonfarm payrolls", "jobless claims", "unemployment rate", "retail sales",
		"consumer confidence", "yield curve", "rate (?:cut|hike)", "repo rate"), TypeMacroEvent, 0},
	{compile("crude", "brent", "OPEC", "oil price"), TypeCommodityEvent, 0},
	{compile("tariff", "sanction(s)?", "war", "geopolitic", "trade restriction", "export ban"), TypeGeopoliticalEvent, 0},
	{compile("circular", "regulation", "guidelines",
		"(?:SEC|FTC|Fed|Federal Reserve|CFPB|FCC|FERC|EPA|FDA|USTR) (?:proposes|adopts|finalis|finaliz|issues|notifies)",
		"(?:proposed|final|interim) rule", "rulemaking", "comment period",
		"executive order", "Federal Register"), TypeRegulatoryPolicy, 0},
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
	"SEC charges", "raid", "default(s|ed)?", "downgrade", "emergency",
	"chapter 11", "guidance cut", "recall", "short report",
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

// marketNoise is coverage no US equity reader acts on when it names no
// company: another market's session wrap, or a retail technical-signal
// listicle ("RSI Alert: ... Now Oversold", "... hits 52-week high").
var marketNoise = compile(
	"FTSE", "DAX", "CAC 40", "Nikkei", "Hang Seng", "Euro Stoxx", "Sensex", "Nifty",
	"52-week (?:high|low)", "moving average cross", "RSI alert",
	"oversold", "overbought", "intrinsic value", "price target (?:raised|cut)",
	"analyst (?:upgrades|downgrades) stock",
)

// NotReadableHere reports that a headline is mostly in another script. The
// feed, the prompts and the resolver are English end to end, so such an item
// cannot be resolved, ranked or read. The threshold is high: a quoted foreign
// name does not make an English headline unreadable.
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

// Unactionable reports whether an item that named no company is market
// noise rather than news.
func Unactionable(headline, summary string) bool {
	return marketNoise.MatchString(headline + ". " + firstSentences(summary, 1))
}
