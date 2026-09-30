package company

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// exchangeTicker matches the exchange-qualified notation US market copy uses
// constantly -- "(NASDAQ:NVDA)", "NYSE: KO". The ticker is an unambiguous
// identification, and the exchange name must not be read as a company:
// Nasdaq Inc. is itself listed (NDAQ).
var exchangeTicker = regexp.MustCompile(`(?i)\b(?:NASDAQ|NYSE|NYSEAMERICAN|NYSEARCA|AMEX|OTC|CBOE)\s*[:\-]\s*([A-Z0-9.\-]{1,6})\b`)

// exchangeWords are never company mentions as bare words in market copy.
var exchangeWords = map[string]bool{"NASDAQ": true, "NYSE": true, "AMEX": true, "CBOE": true, "OTC": true}

// Resolve finds the companies named in text.
//
// The ladder runs most-certain first -- exchange-qualified ticker, registered
// name, curated alias, lead word, bare uppercase symbol -- and a company found
// by a stronger rule is never downgraded by a weaker one. Name matching is
// longest-first and non-overlapping. A phrase claimed by two issuers matches
// nothing: there is no evidence in the phrase for choosing.
func (m *Master) Resolve(text string) []Match {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	best := map[string]Match{}
	offer := func(sym string, conf float64, method Method, matched string) {
		c, listed := m.bySymbol[sym]
		if !listed {
			return
		}
		if prev, seen := best[sym]; seen && prev.Confidence >= conf {
			return
		}
		best[sym] = Match{Symbol: c.Symbol, Name: c.Name, Confidence: conf, Method: method, Matched: matched}
	}

	// Exchange-qualified tickers, then blanked out so "NVIDIA (NASDAQ:NVDA)"
	// does not also resolve Nasdaq Inc. Blanking the span rather than
	// blocking the word keeps "Nasdaq Inc. reported earnings" working.
	for _, g := range exchangeTicker.FindAllStringSubmatch(text, -1) {
		if sym := strings.ToUpper(g[1]); sym != "" {
			offer(sym, confExplicit, MethodExplicit, sym)
		}
	}
	text = blankSpans(text, exchangeTicker.FindAllStringIndex(text, -1))

	// Registered names, aliases and lead words.
	tokens := strings.Fields(Normalize(text))
	for i := 0; i < len(tokens); i++ {
		for n := min(maxPhraseTokens, len(tokens)-i); n >= 1; n-- {
			phrase := strings.Join(tokens[i:i+n], " ")
			// One word is the weakest evidence, and normalizing has discarded
			// the case that separates a company from a common noun. So: no
			// ordinary word ("Delta") or word-like ticker ("ALL") unless a
			// person curated it as an alias, it must appear capitalized
			// ("Apple unveils", not "an apple"), and a lead word must start
			// a name rather than sit inside another one.
			if n == 1 {
				if nameBlocklist[phrase] || tickerBlocklist[strings.ToUpper(phrase)] ||
					(!m.aliasPhrase[phrase] && commonWords[phrase]) ||
					!appearsCapitalized(text, phrase) ||
					(m.leadPhrase[phrase] && !startsProperNoun(text, phrase)) {
					continue
				}
			}
			claimants := m.byPhrase[phrase]
			if len(claimants) != 1 {
				continue
			}
			conf, method := confLegalName, MethodLegalName
			switch {
			case m.aliasPhrase[phrase]:
				conf, method = confAlias, MethodAlias
			case m.leadPhrase[phrase]:
				conf, method = confSingle, MethodLeadWord
			case n == 1 && m.solePhrase[phrase]:
				conf = confSoleName
			case n == 1:
				conf = confSingle
			}
			offer(claimants[0], conf, method, phrase)
			i += n - 1 // consume the span
			break
		}
	}

	// Bare uppercase symbols, unless the whole text is shouted: in an
	// all-caps press release every word looks like a ticker.
	if !isShouting(text) {
		for _, tok := range tickerTokens(text) {
			if len(tok) >= 3 && !tickerBlocklist[tok] && !exchangeWords[tok] {
				offer(tok, confTicker, MethodTicker, tok)
			}
		}
	}

	out := make([]Match, 0, len(best))
	for _, mt := range best {
		out = append(out, mt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// ResolveAbove returns only matches at or above a confidence floor, chosen
// by the caller to suit the cost of being wrong.
func (m *Master) ResolveAbove(text string, floor float64) []Match {
	all := m.Resolve(text)
	out := all[:0]
	for _, mt := range all {
		if mt.Confidence >= floor {
			out = append(out, mt)
		}
	}
	return out
}

// blankSpans replaces each span with spaces, preserving byte offsets so the
// capitalization checks still line up with the original text.
func blankSpans(text string, spans [][]int) string {
	if len(spans) == 0 {
		return text
	}
	b := []byte(text)
	for _, sp := range spans {
		for i := sp[0]; i < sp[1] && i < len(b); i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

// isShouting reports whether at least two thirds of the letters are capitals.
func isShouting(text string) bool {
	var upper, letters int
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	return letters >= 12 && upper*3 >= letters*2
}

// occurrences calls fn for every whole-word, capitalized occurrence of word
// in raw text, stopping when fn returns true.
func occurrences(raw, word string, fn func(i int) bool) bool {
	for i := 0; word != "" && i+len(word) <= len(raw); i++ {
		end := i + len(word)
		if !strings.EqualFold(raw[i:end], word) || (raw[i] >= 'a' && raw[i] <= 'z') ||
			(i > 0 && isWordByte(raw[i-1])) || (end < len(raw) && isWordByte(raw[end])) {
			continue
		}
		if fn(i) {
			return true
		}
	}
	return false
}

// appearsCapitalized reports whether word occurs capitalized, as a word.
func appearsCapitalized(raw, word string) bool {
	return occurrences(raw, word, func(int) bool { return true })
}

// startsProperNoun reports whether word appears at the head of a capitalized
// phrase: "Costco" in "Costco raised prices", not in "Big Costco Rival".
// Punctuation breaks the chain, so a name after a comma still counts.
func startsProperNoun(raw, word string) bool {
	return occurrences(raw, word, func(i int) bool {
		j := i
		for j > 0 && raw[j-1] == ' ' {
			j--
		}
		if j == 0 || !isLetterByte(raw[j-1]) {
			return true
		}
		start := j
		for start > 0 && isWordByte(raw[start-1]) {
			start--
		}
		return !(raw[start] >= 'A' && raw[start] <= 'Z')
	})
}

func isLetterByte(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func isWordByte(b byte) bool { return b == '_' || (b >= '0' && b <= '9') || isLetterByte(b) }
