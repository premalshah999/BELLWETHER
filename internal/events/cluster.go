package events

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
	"unicode"
)

// stopWords are words that carry no identifying weight in a headline. They are
// removed before comparison so that two reports of one story are not judged
// similar merely because both are written in English.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"of": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"with": true, "by": true, "from": true, "as": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "has": true, "have": true,
	"had": true, "will": true, "would": true, "can": true, "could": true,
	"may": true, "might": true, "should": true, "its": true, "it": true,
	"this": true, "that": true, "these": true, "those": true, "after": true,
	"over": true, "into": true, "amid": true, "says": true, "said": true,
	"up": true, "down": true, "new": true, "more": true, "than": true,
	"per": true, "cent": true, "crore": true, "lakh": true, "rs": true,
}

// NormalizeTitle reduces a headline to its content words.
//
// Publishers restyle the same story constantly — "TCS Wins $1 Billion
// Contract", "TCS wins $1bn contract", "Tata Consultancy bags $1 billion
// deal" — and the differences are punctuation, case and filler. What survives
// here is the part that actually distinguishes one story from another.
func NormalizeTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	fields := strings.Fields(b.String())
	out := fields[:0]
	for _, f := range fields {
		if stopWords[f] || len(f) < 2 {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// TitleKey is the sorted, deduplicated token set of a headline.
//
// Sorting makes the key insensitive to word order, so an active and a passive
// rendering of one event produce the same key. It is deliberately a coarse
// signal: an exact key match is strong evidence of the same story, and
// anything short of that falls through to similarity scoring.
func TitleKey(title string) string {
	tokens := strings.Fields(NormalizeTitle(title))
	if len(tokens) == 0 {
		return ""
	}
	seen := map[string]bool{}
	uniq := tokens[:0]
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	sort.Strings(uniq)
	sum := sha256.Sum256([]byte(strings.Join(uniq, " ")))
	return hex.EncodeToString(sum[:12])
}

// TitleSimilarity scores two headlines from 0 to 1 by Jaccard overlap of
// content words: cheap, no model, and well suited to short text where a shared
// company name or contract value carries weight. It is not semantic, so
// clustering also requires a shared entity; the failure mode is a duplicate
// event, not a wrongly merged one.
func TitleSimilarity(a, b string) float64 {
	setA := tokenSet(a)
	setB := tokenSet(b)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	inter := 0
	for t := range setA {
		if setB[t] {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func tokenSet(title string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(NormalizeTitle(title)) {
		out[t] = true
	}
	return out
}

// Clustering thresholds.
const (
	// similarityThreshold is how alike two headlines must be, given that they
	// already share a company, to be judged the same story. It is set high:
	// merging two genuinely different events into one is far worse than
	// showing a duplicate, because the merged event silently loses whichever
	// headline lost the tie.
	similarityThreshold = 0.55

	// clusterWindow is how far back a candidate event may be. A story is
	// picked up by other outlets over hours, not days, and a window that is
	// too generous starts merging this quarter's results with last quarter's.
	clusterWindow = 36 * time.Hour

	// officialWindow is the wider window used when matching a news report to
	// an exchange filing. Coverage of a filing continues well into the next
	// day, and attaching it to the filing is exactly the corroboration the
	// event model exists to capture.
	officialWindow = 48 * time.Hour
)

// Candidate is an existing event a new item might belong to.
type Candidate struct {
	ID           int64
	Type         Type
	Headline     string
	TitleKey     string
	Symbols      []string
	DiscoveredAt time.Time
	Official     bool
}

// MatchDecision is the outcome of trying to place an item into a cluster.
type MatchDecision struct {
	EventID int64
	Score   float64
	Reason  string
	Matched bool
}

// FindCluster decides whether an incoming item belongs to an existing event.
//
// The rule is conjunctive by design: a candidate must share at least one
// resolved company AND either an identical title key or a high similarity
// score. Requiring both is what keeps "Reliance wins order" and "Reliance
// faces probe" apart on a day when both are true, and what keeps two unrelated
// companies' identically-worded results announcements from collapsing.
func FindCluster(title string, symbols []string, typ Type, at time.Time, candidates []Candidate) MatchDecision {
	key := TitleKey(title)
	if len(symbols) == 0 {
		// With no resolved company there is nothing to anchor a similarity
		// judgement to, so only an exact token-set match counts. That is
		// still worth doing: syndicated copy and wire pickups reproduce a
		// headline verbatim across many outlets, and those are the same story
		// whether or not we managed to name a company in them.
		if key == "" {
			return MatchDecision{}
		}
		for _, c := range candidates {
			if c.TitleKey != key {
				continue
			}
			age := at.Sub(c.DiscoveredAt)
			if age < 0 {
				age = -age
			}
			if age <= clusterWindow {
				return MatchDecision{EventID: c.ID, Score: 1, Reason: "identical headline tokens", Matched: true}
			}
		}
		return MatchDecision{}
	}
	symSet := map[string]bool{}
	for _, s := range symbols {
		symSet[s] = true
	}

	best := MatchDecision{}
	for _, c := range candidates {
		window := clusterWindow
		if c.Official || typ.official() {
			window = officialWindow
		}
		age := at.Sub(c.DiscoveredAt)
		if age < 0 {
			age = -age
		}
		if age > window {
			continue
		}
		shared := false
		for _, s := range c.Symbols {
			if symSet[s] {
				shared = true
				break
			}
		}
		// An identical headline is the same story even when only one side has
		// a company resolved: a syndicated copy whose outlet named the ticker
		// and one whose outlet did not. Two sides that each name a different
		// company stay apart.
		if !shared && key != "" && key == c.TitleKey && len(c.Symbols) == 0 {
			return MatchDecision{EventID: c.ID, Score: 1, Reason: "identical headline tokens", Matched: true}
		}
		if !shared {
			continue
		}

		// An identical token set is as strong as this method gets.
		if key != "" && key == c.TitleKey {
			return MatchDecision{EventID: c.ID, Score: 1, Reason: "identical headline tokens", Matched: true}
		}
		score := TitleSimilarity(title, c.Headline)

		// Two filings of the same type about the same company on the same day
		// are one event even when the wording differs — an exchange filing
		// and the company's own press release about it, for instance.
		if typ == c.Type && typ != TypeUnclassified && typ != TypeAdministrative && score >= 0.35 {
			score += 0.15
		}
		if score > best.Score {
			best = MatchDecision{EventID: c.ID, Score: score, Reason: "headline similarity", Matched: score >= similarityThreshold}
		}
	}
	if !best.Matched {
		return MatchDecision{Score: best.Score}
	}
	return best
}

// official reports whether a type only ever originates from an exchange or
// regulator, and therefore deserves the wider corroboration window.
func (t Type) official() bool {
	switch t {
	case TypeEarnings, TypeDividend, TypeBonus, TypeStockSplit, TypeBuyback,
		TypeRightsIssue, TypeRecordDate, TypeBoardMeeting, TypeShareholdingChange,
		TypeInsiderTransaction, TypeAnnualReport, TypeAllotment:
		return true
	}
	return false
}

// Fingerprint is the deterministic identity of an event.
//
// It exists so that reprocessing the same raw item twice — after a restart, or
// after a parser fix — converges on the same event rather than creating a
// second one. Company, type and calendar day are enough: one company does not
// declare two different dividends on one day, and if it files a correction,
// that correction is evidence for the same event rather than a new one.
func Fingerprint(symbols []string, typ Type, titleKey string, day time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	sorted := append([]string(nil), symbols...)
	sort.Strings(sorted)

	h := sha256.New()
	h.Write([]byte(strings.Join(sorted, ",")))
	h.Write([]byte{0})
	h.Write([]byte(typ))
	h.Write([]byte{0})
	h.Write([]byte(day.In(loc).Format("2006-01-02")))
	h.Write([]byte{0})
	// The title key is included so two unrelated events of one type on one day
	// stay distinct — a company can win two separate orders.
	h.Write([]byte(titleKey))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// StripPublisher removes a trailing " - Publisher" that aggregators append to
// a headline.
//
// Google News writes every headline as "Story - Outlet", so the same story
// from two outlets arrived as two different headlines and never clustered.
// The suffix is removed only when it is the item's own publisher, or a bare
// domain: "Notable Two Hundred Day Moving Average Cross - CLF" ends in a
// ticker, and those are different stories about different companies.
func StripPublisher(title, publisher string) string {
	t := strings.TrimSpace(title)
	for _, sep := range []string{" - ", " | ", " — ", " – "} {
		i := strings.LastIndex(t, sep)
		if i <= 0 {
			continue
		}
		tail := strings.TrimSpace(t[i+len(sep):])
		if tail == "" || len(tail) > 60 {
			continue
		}
		pub := strings.TrimSpace(publisher)
		isPublisher := pub != "" && (strings.EqualFold(tail, pub) ||
			strings.EqualFold(strings.TrimPrefix(tail, "www."), strings.TrimPrefix(pub, "www.")))
		isDomain := !strings.ContainsAny(tail, " ") && strings.Contains(tail, ".") &&
			strings.IndexFunc(tail, unicode.IsLetter) >= 0
		if isPublisher || isDomain {
			return strings.TrimSpace(t[:i])
		}
	}
	return t
}
