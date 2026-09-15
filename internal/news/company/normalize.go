package company

import (
	"regexp"
	"strings"
	"unicode"
)

// possessive matches a trailing "'s" or "’s" on a word.
var possessive = regexp.MustCompile(`['’]s\b`)

// corporateSuffixes are the legal-form words that carry no identifying
// information. 2,543 of the 2,557 NSE listings end in "Limited", so leaving
// them in would mean every comparison spends its effort on a word shared by
// the entire market.
var corporateSuffixes = []string{
	"private limited", "pvt limited", "pvt ltd", "private ltd",
	"limited", "ltd", "llp", "plc", "incorporated", "inc",
	"corporation", "corp", "company", "co",
}

// Normalize reduces a company name to a comparable key.
//
// The transformations are deliberately conservative. Anything that merges two
// genuinely different companies is far more damaging than anything that fails
// to merge two spellings of one company: a missed match costs an article, a
// wrong match attributes news to a stock the reader may act on.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	// "&" and "and" are used interchangeably by publishers and by NSE itself
	// ("Indian Metals & Ferro Alloys" vs "... and Ferro Alloys").
	s = strings.ReplaceAll(s, "&", " and ")

	// Possessives are stripped whole. Deleting only the apostrophe would weld
	// the trailing "s" onto the previous word, turning "L&T's" into "l and ts"
	// and losing a match that "l and t" would have made. Because the master's
	// own names run through this same function, dropping the "s" cannot cause
	// a mismatch: "Dr. Reddy's Laboratories" normalises identically on both
	// sides of the comparison.
	s = possessive.ReplaceAllString(s, "")

	// Everything that is not a letter, digit or space becomes a space, with
	// one exception: a remaining apostrophe is deleted rather than spaced, so
	// that an in-word mark does not split the word in two.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '\u2019':
			// dropped
		default:
			b.WriteByte(' ')
		}
	}
	s = strings.Join(strings.Fields(b.String()), " ")

	// A leading article is noise: "The Indian Hotels Company" and "Indian
	// Hotels Company" are one company.
	s = strings.TrimPrefix(s, "the ")

	// Strip legal-form suffixes repeatedly, because they stack:
	// "... Private Limited" leaves "... Private" after one pass.
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

// tokenize splits already-normalized text into words.
func tokenize(normalized string) []string {
	if normalized == "" {
		return nil
	}
	return strings.Fields(normalized)
}

// tickerTokens extracts candidate ticker symbols from raw, un-normalized text.
//
// Case is load-bearing here and must not be discarded. Fourteen NSE tickers
// are also ordinary English words — CLEAN, FOCUS, IDEA, RAIN, STAR, TOTAL and
// others — and the only thing separating "GAIL raised guidance" from "the
// winds fail" is that a ticker is written in capitals. Lower-casing first, as
// name matching does, would make those fourteen symbols fire on prose.
func tickerTokens(raw string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range raw {
		// "&" is kept because two real tickers contain it: M&M and L&T.
		if unicode.IsUpper(r) || unicode.IsDigit(r) || r == '&' {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}
