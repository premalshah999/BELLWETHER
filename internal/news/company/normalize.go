package company

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// possessive matches a trailing "'s" or "’s" on a word.
	possessive = regexp.MustCompile(`['’]s\b`)
	// secQualifier is the state or status tag SEC appends to a registrant's
	// name ("BANK OF AMERICA CORP /DE/", "COSTCO WHOLESALE CORP /NEW"). Left
	// in, the name can never match how the press writes it.
	secQualifier = regexp.MustCompile(`\s*/[A-Za-z]{2,4}/?\s*$`)
)

// corporateSuffixes are the legal-form words that carry no identifying
// information. "and co" comes before "co", so "JPMorgan Chase & Co" does not
// normalize to a name ending in "and".
var corporateSuffixes = []string{
	"and company", "and co", "limited", "ltd", "llc", "llp", "plc", "incorporated",
	"inc", "corporation", "corp", "company", "co",
}

// Normalize reduces a company name to a comparable key. The transformations
// are conservative: merging two different companies is far worse than failing
// to merge two spellings of one.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(secQualifier.ReplaceAllString(s, "")))
	s = strings.ReplaceAll(s, "&", " and ")
	// Possessives go whole: deleting only the apostrophe would weld the "s"
	// onto the word. Both sides normalize the same way, so nothing mismatches.
	s = possessive.ReplaceAllString(s, "")

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '’':
			// an in-word apostrophe must not split the word
		default:
			b.WriteByte(' ')
		}
	}
	s = strings.TrimPrefix(strings.Join(strings.Fields(b.String()), " "), "the ")

	// Suffixes stack ("... Company, Inc."), so strip until none is left.
	for changed := true; changed; {
		changed = false
		for _, suffix := range corporateSuffixes {
			if trimmed, ok := strings.CutSuffix(s, " "+suffix); ok {
				s, changed = strings.TrimSpace(trimmed), true
				break
			}
		}
	}
	return s
}

// tickerTokens extracts candidate tickers from raw text. Case is the whole
// signal here: a ticker is written in capitals, a word is not.
func tickerTokens(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool { return !unicode.IsUpper(r) && !unicode.IsDigit(r) })
}
