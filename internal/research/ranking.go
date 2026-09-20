package research

import (
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

var queryStopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("a an the and or of in on at to for from by with is are was were be been what which who when where how why does do did has have had can could would should this that these those it its their about please explain research tell me latest recent today company companies") {
		m[w] = true
	}
	return m
}()

// QueryTerms is shared by retrieval and passage selection. No model is needed
// to remove question scaffolding; numbers and ticker symbols remain intact.
func QueryTerms(query string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) < 2 || queryStopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

func termHits(text string, terms []string) int {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		words[w] = true
	}
	hits := 0
	for _, t := range terms {
		if words[t] {
			hits++
		}
	}
	return hits
}

func rankFindings(query string, findings []Finding) {
	terms := QueryTerms(query)
	now := time.Now()
	for i := range findings {
		f := &findings[i]
		hits := termHits(f.Title, terms)*3 + termHits(f.Snippet, terms) + termHits(f.Body, terms)
		age := now.Sub(f.PublishedAt).Hours() / 24
		fresh := 0.0
		if !f.PublishedAt.IsZero() && age >= 0 {
			fresh = 1 / (1 + age/7)
		}
		f.Relevance = math.Round((float64(hits)*10+float64(f.Trust)/25+fresh)*100) / 100
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Relevance != findings[j].Relevance {
			return findings[i].Relevance > findings[j].Relevance
		}
		return findings[i].URL < findings[j].URL
	})
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
