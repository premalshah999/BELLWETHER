package research

import (
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

var queryStopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("a an the and or of in on at to for from by with is are was were be been what which who when where how why does do did has have had can could would should this that these those it its their about please explain research tell me latest recent today company companies news my i we our you your") {
		m[w] = true
	}
	return m
}()

// genericTerms say little on their own in a finance search. Nearly every
// personal-finance or markets article promises the "best strategies" or
// talks about "stocks", so a match on these alone is how a Prime Day deals
// list and a footballer's biography ranked above Fidelity's 401(k) guide for
// "best strategies for early career 401k". They count, but a source must also
// match the question's specific words to be relevant.
var genericTerms = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("best top good great new old strategy tip guide way thing need know vs versus right make more most much many share stock market price invest investing investor buy sell doing going happen happening now year quarter week month day time ever") {
		m[w] = true
	}
	return m
}()

// plan401k joins "401(k)", "403(b)" and "457(b)" into one word so they can
// match a question that writes "401k", which tokenising on punctuation would
// otherwise split into "401" and a dropped "k".
var plan401k = regexp.MustCompile(`(?i)\b(\d{3})\s?\(\s?([a-z])\s?\)`)

// stem folds the plural endings that most often separate a question from a
// headline ("strategies" and "strategy", "launches" and "launch"). Applied to
// both sides, so its imperfections cancel out.
func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 4 && (strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes") || strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "xes")):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:len(w)-1]
	}
	return w
}

func words(text string) []string {
	text = plan401k.ReplaceAllString(text, "$1$2")
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		out = append(out, stem(w))
	}
	return out
}

func wordSet(text string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words(text) {
		m[w] = true
	}
	return m
}

// QueryTerms is shared by retrieval and passage selection. No model is needed
// to remove question scaffolding; numbers and ticker symbols remain intact.
func QueryTerms(query string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words(query) {
		if len(w) < 2 || queryStopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

func termHits(text string, terms []string) int {
	words := wordSet(text)
	hits := 0
	for _, t := range terms {
		if words[t] {
			hits++
		}
	}
	return hits
}

// queryProfile weighs a question's terms by how much each says about what
// was asked. A term with a digit (401k, 10-K, 2026) or written as a ticker or
// a proper noun (NVDA, Caterpillar) is the subject and weighs three times an
// ordinary word; a generic word weighs a quarter.
type queryProfile struct {
	terms  []string
	weight map[string]float64
	// key is the total weight of the terms that are not generic. A source is
	// relevant when it matches at least half of it.
	key float64
	// loose is set when every term is generic, so any of them counts.
	loose bool
}

func profileOf(query string) queryProfile {
	p := queryProfile{terms: QueryTerms(query), weight: map[string]float64{}}
	strong := map[string]bool{}
	for i, raw := range strings.FieldsFunc(plan401k.ReplaceAllString(query, "$1$2"), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '$'
	}) {
		raw = strings.TrimPrefix(raw, "$")
		if raw == "" {
			continue
		}
		w := stem(strings.ToLower(raw))
		upper := strings.ToUpper(raw) == raw && strings.IndexFunc(raw, unicode.IsLetter) >= 0
		capital := unicode.IsUpper([]rune(raw)[0])
		switch {
		case strings.IndexFunc(raw, unicode.IsDigit) >= 0 && strings.IndexFunc(raw, unicode.IsLetter) >= 0,
			upper && len(raw) >= 2 && len(raw) <= 5,
			capital && i > 0:
			strong[w] = true
		}
	}
	for _, t := range p.terms {
		switch {
		case strong[t] || isNumber(t) && len(t) == 4:
			p.weight[t] = 3
		case genericTerms[t]:
			p.weight[t] = 0.25
		default:
			p.weight[t] = 1
		}
		if !genericTerms[t] || strong[t] {
			p.key += p.weight[t]
		}
	}
	// A question made only of generic words ("what stocks should I buy")
	// has nothing more specific to require.
	if p.key == 0 {
		p.loose = true
		for _, t := range p.terms {
			p.key += p.weight[t]
		}
	}
	return p
}

func isNumber(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}

// score is a finding's weighted term matches, and whether it matched enough
// of the question's specific terms to count as evidence about it. subject is
// set when the finding names a company the question is about.
func (p queryProfile) score(f *Finding, subject bool) (float64, bool) {
	title, snippet, body := wordSet(f.Title), wordSet(f.Snippet), wordSet(f.Body)
	var s, matched float64
	for _, t := range p.terms {
		w := p.weight[t]
		in := false
		if title[t] {
			s += 3 * w
			in = true
		}
		if snippet[t] {
			s += w
			in = true
		}
		if body[t] {
			s += w
			in = true
		}
		if in && (!genericTerms[t] || w >= 3 || p.loose) {
			matched += w
		}
	}
	// An article resolved to the company the question names is about the
	// question's subject, however its headline words it.
	if subject {
		s += 12
		matched = max(matched, p.key/2)
	}
	return s, s > 0 && matched*2 >= p.key
}

// matches reports whether text is about the question, by the same rule.
func (p queryProfile) matches(text string) bool {
	_, ok := p.score(&Finding{Title: text}, false)
	return ok
}

func rankFindings(query string, findings []Finding) { rankFindingsFor(query, nil, findings) }

// rankFindingsFor orders findings by relevance to the question. A finding that
// matched too little of the question scores below 10, which keeps it out of
// the evidence and off the reading list; if nothing qualifies, the rule
// relaxes to any match rather than leave the answer with no sources.
func rankFindingsFor(query string, subjects []string, findings []Finding) {
	p := profileOf(query)
	isSubject := map[string]bool{}
	for _, s := range subjects {
		isSubject[s] = true
	}
	now := time.Now()
	raw := make([]float64, len(findings))
	ok := make([]bool, len(findings))
	relevant := 0
	for i := range findings {
		f := &findings[i]
		subject := false
		for _, s := range f.Symbols {
			subject = subject || isSubject[s]
		}
		raw[i], ok[i] = p.score(f, subject)
		if ok[i] {
			relevant++
		}
	}
	for i := range findings {
		f := &findings[i]
		age := now.Sub(f.PublishedAt).Hours() / 24
		fresh := 0.0
		if !f.PublishedAt.IsZero() && age >= 0 {
			fresh = 1 / (1 + age/7)
		}
		v := raw[i]*10 + float64(f.Trust)/25 + fresh
		if !ok[i] && !(relevant == 0 && raw[i] > 0) {
			v = math.Min(v/10, 9.9)
		}
		f.Relevance = math.Round(v*100) / 100
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Relevance != findings[j].Relevance {
			return findings[i].Relevance > findings[j].Relevance
		}
		return findings[i].URL < findings[j].URL
	})
}

// relevantOnly drops the findings that did not match the question, once
// enough did. Listing a hundred links of which half are about something else
// makes the answer look better sourced than it is and buries the ones that
// were read.
func relevantOnly(findings []Finding) []Finding {
	n := 0
	for _, f := range findings {
		if f.Relevance >= 10 {
			n++
		}
	}
	if n < 8 {
		return findings[:min(len(findings), 30)]
	}
	out := findings[:0]
	for _, f := range findings {
		if f.Relevance >= 10 {
			out = append(out, f)
		}
	}
	return out
}

// EvidenceExcerpt keeps the lead and the most query-relevant passages, in
// document order. It does not paraphrase or ask a model to summarise a model.
func EvidenceExcerpt(text, query string, budget int) string {
	words := strings.Fields(text)
	if len(words) <= budget {
		return text
	}
	if budget <= 0 {
		return ""
	}
	const size = 80
	type passage struct{ start, end, score int }
	var chunks []passage
	terms := QueryTerms(query)
	for i := 0; i < len(words); i += size {
		end := min(i+size, len(words))
		score := termHits(strings.Join(words[i:end], " "), terms)
		if i == 0 {
			score += 1000
		}
		chunks = append(chunks, passage{i, end, score})
	}
	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].score > chunks[j].score })
	chunks = chunks[:min(len(chunks), (budget+size-1)/size)]
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].start < chunks[j].start })
	var parts []string
	for _, c := range chunks {
		n := min(c.end-c.start, budget)
		if n <= 0 {
			break
		}
		parts = append(parts, strings.Join(words[c.start:c.start+n], " "))
		budget -= n
	}
	return strings.Join(parts, "\n[…]\n")
}

// EvidenceIndexes excludes discovery-only links and caps publisher dominance.
// Returned indexes refer to the original list so citations survive selection.
func EvidenceIndexes(findings []Finding, limit int) []int {
	var out []int
	hosts := map[string]int{}
	for i, f := range findings {
		if strings.TrimSpace(f.Body) == "" || (f.Relevance > 0 && f.Relevance < 10) {
			continue
		}
		u, err := url.Parse(f.URL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if hosts[host] >= 3 {
			continue
		}
		hosts[host]++
		out = append(out, i)
		if len(out) >= limit {
			break
		}
	}
	return out
}
