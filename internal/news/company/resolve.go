package company

import (
	"regexp"
	"sort"
	"strings"
)

// explicitTicker matches notations that name an instrument unambiguously,
// because a venue or suffix accompanies the symbol: "NSE: RELIANCE",
// "BSE:500325", "RELIANCE.NS", "TCS.BO".
var explicitTicker = regexp.MustCompile(`(?i)\b(?:NSE|BSE)\s*[:\-]\s*([A-Z0-9&]{2,20})\b|\b([A-Z0-9&]{2,20})\.(?:NS|BO)\b`)

// Resolve finds the companies named in text.
//
// The ladder runs most-certain first — explicit ticker notation, then the
// registered legal name, then a curated alias, then a bare uppercase symbol —
// and a company already resolved by a stronger rule is never downgraded by a
// weaker one. Matching is longest-first and non-overlapping, so "Tata
// Consultancy Services" resolves once as TCS rather than also offering up
// whatever "Tata Consultancy" might match.
//
// Ambiguity is not resolved by guessing. A phrase claimed by more than one
// listing yields nothing unless the claimants are share classes of a single
// issuer, in which case the ordinary class is chosen.
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
		best[sym] = Match{Symbol: sym, Name: c.Name, Confidence: conf, Method: method, Matched: matched}
	}

	// --- explicit venue-qualified notation ---------------------------------
	for _, g := range explicitTicker.FindAllStringSubmatch(text, -1) {
		sym := strings.ToUpper(g[1])
		if sym == "" {
			sym = strings.ToUpper(g[2])
		}
		offer(sym, confExplicit, MethodExplicit, sym)
	}

	// --- registered names and curated aliases ------------------------------
	tokens := tokenize(Normalize(text))
	for i := 0; i < len(tokens); i++ {
		width := maxPhraseTokens
		if remaining := len(tokens) - i; width > remaining {
			width = remaining
		}
		for n := width; n >= 1; n-- {
			phrase := strings.Join(tokens[i:i+n], " ")
			// A one-word phrase is the weakest evidence the resolver
			// accepts, and normalisation has already destroyed the case
			// information that separates a company from a common noun. Three
			// guards apply, none of which a multi-word name needs:
			//
			//   - an ordinary English word that happens to be a listed name
			//     ("Delta", "Swan") is refused outright;
			//   - so is one that happens to be a listed ticker ("LT", "IDEA",
			//     "TOTAL"), which the alias table would otherwise smuggle
			//     past the bare-ticker blocklist;
			//   - and what remains must appear capitalised in the original
			//     text, because a company is a proper noun. This is what
			//     separates "SAIL raised output" from "winds fail to sail".
			if n == 1 {
				if nameBlocklist[phrase] || tickerBlocklist[strings.ToUpper(phrase)] {
					continue
				}
				if !appearsCapitalized(text, phrase) {
					continue
				}
				// A lead word stands in for a name only when it starts one.
				// "Impex" opens Impex Ferro Tech, but in "Striders Impex
				// Limited" it is the middle of a different company's name,
				// and in "House of Abhinandan Lodha" it is the tail of a
				// person's. Requiring that no capitalised word immediately
				// precede it separates the two cases.
				if m.leadPhrase[phrase] && !startsProperNoun(text, phrase) {
					continue
				}
			}
			sym, ok := m.disambiguate(m.byPhrase[phrase])
			if !ok {
				continue
			}
			conf, method := confLegalName, MethodLegalName
			switch {
			case m.aliasPhrase[phrase]:
				conf, method = confAlias, MethodAlias
			case m.leadPhrase[phrase]:
				conf, method = confSingle, MethodLeadWord
			case n == 1:
				// One word is weak evidence even when it is a registered
				// name, so it is reported as a lead rather than a fact.
				conf = confSingle
			}
			offer(sym, conf, method, phrase)
			i += n - 1 // consume the span; no overlapping match
			break
		}
	}

	// --- bare uppercase symbols --------------------------------------------
	for _, tok := range tickerTokens(text) {
		if len(tok) < 3 || tickerBlocklist[tok] {
			continue
		}
		if _, listed := m.bySymbol[tok]; listed {
			offer(tok, confTicker, MethodTicker, tok)
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

// ResolveAbove returns only matches at or above a confidence floor.
//
// Callers pick the floor to suit the cost of being wrong: an alert that
// pushes to a phone should demand near-certainty, while a browsable company
// timeline can afford a weaker lead.
func (m *Master) ResolveAbove(text string, min float64) []Match {
	all := m.Resolve(text)
	out := all[:0]
	for _, mt := range all {
		if mt.Confidence >= min {
			out = append(out, mt)
		}
	}
	return out
}

// appearsCapitalized reports whether word occurs in raw text with a leading
// capital, as a whole word. Matching is done on the raw string precisely
// because Normalize has thrown this information away.
func appearsCapitalized(raw, word string) bool {
	if word == "" {
		return false
	}
	for i := 0; i+len(word) <= len(raw); i++ {
		if !strings.EqualFold(raw[i:i+len(word)], word) {
			continue
		}
		if raw[i] >= 'a' && raw[i] <= 'z' {
			continue // lower-case occurrence: prose, not a name
		}
		beforeOK := i == 0 || !isWordByte(raw[i-1])
		end := i + len(word)
		afterOK := end == len(raw) || !isWordByte(raw[end])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

// startsProperNoun reports whether word appears in raw text at the head of a
// capitalised phrase — that is, with no capitalised word directly before it.
// Punctuation breaks the chain, so a name after a comma or a quote still
// counts as starting one.
func startsProperNoun(raw, word string) bool {
	for i := 0; i+len(word) <= len(raw); i++ {
		if !strings.EqualFold(raw[i:i+len(word)], word) {
			continue
		}
		if raw[i] >= 'a' && raw[i] <= 'z' {
			continue
		}
		end := i + len(word)
		if i > 0 && isWordByte(raw[i-1]) {
			continue // mid-word, not a match at all
		}
		if end < len(raw) && isWordByte(raw[end]) {
			continue
		}
		// Walk back over the separating run. Only spaces continue a name;
		// anything else ends the previous phrase.
		j := i
		for j > 0 && raw[j-1] == ' ' {
			j--
		}
		if j == 0 {
			return true // starts the text
		}
		prevEnd := j
		if !isLetterByte(raw[prevEnd-1]) {
			return true // preceded by punctuation, so a new phrase begins
		}
		prevStart := prevEnd
		for prevStart > 0 && isWordByte(raw[prevStart-1]) {
			prevStart--
		}
		if c := raw[prevStart]; c >= 'A' && c <= 'Z' {
			continue // a capitalised word runs into this one: same name
		}
		return true
	}
	return false
}

func isLetterByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// disambiguate picks a single symbol from the claimants of one phrase.
//
// The only ambiguity worth resolving automatically is the differential-voting
// -rights listing: JISLJALEQS and JISLDVREQS are one company, and a story
// about Jain Irrigation is about the ordinary class. Genuine collisions
// between different issuers return nothing, because there is no evidence in
// the phrase itself for choosing between them.
func (m *Master) disambiguate(symbols []string) (string, bool) {
	switch len(symbols) {
	case 0:
		return "", false
	case 1:
		return symbols[0], true
	}
	var ordinary []string
	for _, s := range symbols {
		if !strings.Contains(s, "DVR") {
			ordinary = append(ordinary, s)
		}
	}
	if len(ordinary) == 1 {
		return ordinary[0], true
	}
	return "", false
}
