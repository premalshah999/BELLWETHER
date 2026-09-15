package events

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Filing is one exchange disclosure, read into structured form.
//
// Everything on it comes from the document itself. NSE states the category of
// each filing, the amount of each dividend and the exact promoter holding, so
// none of that is inferred, guessed, or sent to a model. What the model is for
// is the part NSE does not say: whether this matters, and to whom.
type Filing struct {
	SourceID string
	Company  string // as the exchange names it, which is the legal name
	Symbol   string // extracted from the document path, empty when absent
	Type     Type
	Subject  string // NSE's own category string, kept verbatim
	Summary  string // the readable part of the description

	// Facts are the key-value pairs the feed encodes. They are kept as
	// strings because that is what the exchange published; typed readings are
	// offered by the accessors below, which can fail without destroying the
	// original.
	Facts map[string]string

	// OccurredAt is the date the filing is *about* — the meeting date, the
	// record date, the "as on" date of a shareholding snapshot. It is
	// routinely in the future for calendar filings, and is distinct from when
	// the filing was published or discovered.
	OccurredAt time.Time
}

// Fact returns one extracted value.
func (f Filing) Fact(key string) (string, bool) {
	v, ok := f.Facts[strings.ToUpper(strings.TrimSpace(key))]
	return v, ok
}

// symbolFromPath extracts an NSE symbol from a document path.
//
// NSE prefixes most filing documents with the company's trading symbol, which
// is a far stronger identifier than the company name. It is not universal:
// XBRL submissions are named by numeric identifiers instead, and annual
// reports use their own layout.
var (
	pathSymbol       = regexp.MustCompile(`^([A-Z0-9&*-]{2,20})_\d{12,14}`)
	pathSymbolAR     = regexp.MustCompile(`^AR_\d+_([A-Z0-9&*-]{2,20})_`)
	corpActionExDate = regexp.MustCompile(`(?i)\s*-\s*Ex-Date\s*:\s*(.+)$`)
)

func symbolFromPath(rawURL string) string {
	name := rawURL
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if m := pathSymbolAR.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if m := pathSymbol.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// parsePipeFacts reads NSE's "KEY:VALUE |KEY : VALUE" description format.
//
// Spacing around the colon is inconsistent between feeds and sometimes within
// one, so keys are upper-cased and trimmed rather than matched literally.
func parsePipeFacts(desc string) map[string]string {
	facts := map[string]string{}
	for _, part := range strings.Split(desc, "|") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.Index(part, ":")
		if i <= 0 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(part[:i]))
		val := strings.TrimSpace(part[i+1:])
		if key == "" || val == "" || val == "-" {
			continue
		}
		facts[key] = val
	}
	return facts
}

// nseDateLayouts are the date formats NSE uses across its feeds. They are all
// zoneless and all Indian Standard Time.
var nseDateLayouts = []string{
	"02-Jan-2006", "02-Jan-06", "02-JAN-2006", "02-JAN-06",
	"2006-01-02", "02/01/2006", "January 2, 2006",
}

func parseNSEDate(s string, loc *time.Location) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range nseDateLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// announcementSplit separates NSE's announcement description into its readable
// half and its category. The format is:
//
//	"<Company> has informed the Exchange regarding <what> |SUBJECT: <category>"
var announcementSplit = regexp.MustCompile(`(?is)^(.*?)\s*\|\s*SUBJECT\s*:\s*(.*)$`)

// informedPrefix strips the boilerplate every announcement opens with, which
// repeats the company name already present in the title.
//
// NSE uses three prepositions here and does not favour the obvious one:
// across a day's 1,600 announcements it writes "about" 606 times, "that" 472
// and "regarding" 348. Matching only one of them leaves the boilerplate on two
// thirds of headlines.
var informedPrefix = regexp.MustCompile(`(?i)^.*?\bhas informed the Exchange\s+(?:about|that|regarding|of|on)\s+`)

// ParseFiling reads one raw NSE item into a Filing.
//
// The description format differs per feed, so the source identifier selects
// the reader. A feed we do not recognise still yields a Filing with its title
// and type left for the classifier, rather than being dropped.
func ParseFiling(sourceID, title, description, rawURL string, loc *time.Location) Filing {
	f := Filing{
		SourceID: sourceID,
		Company:  strings.TrimSpace(title),
		Symbol:   symbolFromPath(rawURL),
		Facts:    map[string]string{},
		Type:     TypeUnclassified,
	}

	switch sourceID {
	case "nse-announcements":
		if m := announcementSplit.FindStringSubmatch(description); m != nil {
			f.Summary = strings.TrimSpace(informedPrefix.ReplaceAllString(m[1], ""))
			f.Subject = strings.TrimSpace(m[2])
		} else {
			f.Summary = strings.TrimSpace(informedPrefix.ReplaceAllString(description, ""))
		}
		f.Type, _ = ClassifySubject(f.Subject)

	case "nse-corp-actions":
		// The title carries the ex-date: "Salzer Electronics Limited - Ex-Date: 28-Aug-2026".
		if m := corpActionExDate.FindStringSubmatch(f.Company); m != nil {
			f.Company = strings.TrimSpace(corpActionExDate.ReplaceAllString(f.Company, ""))
			f.Facts["EX DATE"] = strings.TrimSpace(m[1])
		}
		for k, v := range parsePipeFacts(description) {
			f.Facts[k] = v
		}
		purpose := f.Facts["PURPOSE"]
		f.Subject = purpose
		f.Summary = purpose
		f.Type = classifyCorporateAction(purpose)
		f.OccurredAt = firstDate(loc, f.Facts["EX DATE"], f.Facts["RECORD DATE"])

	case "nse-results":
		f.Facts = parsePipeFacts(description)
		f.Type = TypeEarnings
		period := f.Facts["RELATING TO"]
		f.Subject = "Financial Results"
		f.Summary = strings.TrimSpace(strings.Join(nonEmpty(
			period, f.Facts["AUDITED/UNAUDITED"], f.Facts["CONSOLIDATED/NON-CONSOLIDATED"]), ", "))

	case "nse-board-meetings":
		// "<purpose> |Meeting Date: 28-Aug-2026"
		parts := strings.SplitN(description, "|", 2)
		f.Summary = strings.TrimSpace(parts[0])
		f.Subject = "Board Meeting"
		f.Type = TypeBoardMeeting
		if len(parts) > 1 {
			for k, v := range parsePipeFacts(parts[1]) {
				f.Facts[k] = v
			}
		}
		f.OccurredAt = firstDate(loc, f.Facts["MEETING DATE"])
		// A board meeting called to consider a dividend or a buyback is worth
		// more than one called for "other business matters", and the purpose
		// line says which it is.
		if t := classifyBoardPurpose(f.Summary); t != TypeUnclassified {
			f.Type = t
		}

	case "nse-shareholding":
		f.Facts = parsePipeFacts(description)
		f.Type = TypeShareholdingChange
		f.Subject = "Shareholding Pattern"
		if pr, ok := f.Facts["PR_AND_PRGRP"]; ok {
			f.Summary = "Promoter holding " + pr + "%"
			if pub, ok := f.Facts["PUBLIC_VAL"]; ok {
				f.Summary += ", public " + pub + "%"
			}
		}
		f.OccurredAt = firstDate(loc, f.Facts["AS ON DATE"])

	case "nse-insider":
		f.Facts = parsePipeFacts(description)
		f.Type = TypeInsiderTransaction
		f.Subject = "Insider Trading"
		f.Summary = f.Facts["TYPE OF SECURITY"]

	case "nse-annual-reports":
		f.Facts = parsePipeFacts(description)
		f.Type = TypeAnnualReport
		f.Subject = "Annual Report"
		f.OccurredAt = firstDate(loc, f.Facts["AS ON DATE"])

	case "nse-circulars":
		// Circulars put their subject in the title and leave the description
		// empty. They concern the exchange's own operations rather than one
		// company, so no symbol is expected.
		f.Subject = f.Company
		f.Summary = f.Company
		f.Company = ""
		f.Type = TypeAdministrative
	}

	if f.Summary == "" {
		f.Summary = strings.TrimSpace(description)
	}
	return f
}

// classifyCorporateAction reads NSE's PURPOSE field, which states the action
// in a compact fixed vocabulary: "DIVIDEND - RS 2.50 PER SHARE", "BONUS 1:1".
func classifyCorporateAction(purpose string) Type {
	p := strings.ToLower(purpose)
	switch {
	case strings.Contains(p, "dividend"):
		return TypeDividend
	case strings.Contains(p, "bonus"):
		return TypeBonus
	case strings.Contains(p, "split") || strings.Contains(p, "sub-division") || strings.Contains(p, "subdivision"):
		return TypeStockSplit
	case strings.Contains(p, "buy back") || strings.Contains(p, "buyback"):
		return TypeBuyback
	case strings.Contains(p, "rights"):
		return TypeRightsIssue
	case strings.Contains(p, "amalgamation") || strings.Contains(p, "merger"):
		return TypeMerger
	case strings.Contains(p, "demerger") || strings.Contains(p, "arrangement"):
		return TypeDemerger
	case strings.Contains(p, "annual general meeting"):
		return TypeAGM
	case strings.Contains(p, "extra ordinary") || strings.Contains(p, "extraordinary"):
		return TypeEGM
	case strings.Contains(p, "scheme"):
		return TypeDemerger
	case p == "":
		return TypeUnclassified
	default:
		return TypeRecordDate
	}
}

// classifyBoardPurpose upgrades a board-meeting intimation when its stated
// purpose names something material.
func classifyBoardPurpose(purpose string) Type {
	p := strings.ToLower(purpose)
	switch {
	case strings.Contains(p, "dividend"):
		return TypeDividend
	case strings.Contains(p, "buy back") || strings.Contains(p, "buyback"):
		return TypeBuyback
	case strings.Contains(p, "bonus"):
		return TypeBonus
	case strings.Contains(p, "split") || strings.Contains(p, "sub-division"):
		return TypeStockSplit
	case strings.Contains(p, "rights issue"):
		return TypeRightsIssue
	case strings.Contains(p, "fund rais") || strings.Contains(p, "raising of fund"):
		return TypeFundRaise
	case strings.Contains(p, "financial result") || strings.Contains(p, "unaudited result") ||
		strings.Contains(p, "audited result"):
		return TypeEarnings
	case strings.Contains(p, "amalgamation") || strings.Contains(p, "merger"):
		return TypeMerger
	case strings.Contains(p, "demerger"):
		return TypeDemerger
	default:
		return TypeUnclassified
	}
}

// dividendAmount extracts the rupees-per-share from a corporate action purpose.
var dividendAmount = regexp.MustCompile(`(?i)(?:rs\.?|inr|₹)\s*([0-9]+(?:\.[0-9]+)?)`)

// DividendPerShare reads the declared dividend, if this filing states one.
//
// The second return distinguishes "no dividend stated" from "a dividend of
// zero", which are different claims and must not collapse into one.
func (f Filing) DividendPerShare() (float64, bool) {
	if f.Type != TypeDividend {
		return 0, false
	}
	purpose := f.Facts["PURPOSE"]
	if purpose == "" {
		purpose = f.Summary
	}
	m := dividendAmount.FindStringSubmatch(purpose)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// PromoterHolding reads the promoter and promoter-group percentage from a
// shareholding-pattern filing.
func (f Filing) PromoterHolding() (float64, bool) {
	v, ok := f.Facts["PR_AND_PRGRP"]
	if !ok {
		return 0, false
	}
	pct, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "%"), 64)
	if err != nil || pct < 0 || pct > 100 {
		return 0, false
	}
	return pct, true
}

// PublicHolding reads the public shareholding percentage.
func (f Filing) PublicHolding() (float64, bool) {
	v, ok := f.Facts["PUBLIC_VAL"]
	if !ok {
		return 0, false
	}
	pct, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "%"), 64)
	if err != nil || pct < 0 || pct > 100 {
		return 0, false
	}
	return pct, true
}

func firstDate(loc *time.Location, candidates ...string) time.Time {
	for _, c := range candidates {
		if t := parseNSEDate(c, loc); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}
