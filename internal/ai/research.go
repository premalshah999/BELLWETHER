package ai

import (
	"strconv"
	"time"
)

// maxResearchSources caps how many documents reach the synthesis prompt.
//
// Raised from 24 once the engine started reading article bodies rather than
// headlines. The old ceiling was set when a "source" was a title and a
// forty-five word snippet, where the twenty-fifth document genuinely did
// repeat the first twenty-four. With full text, sources stop being
// interchangeable: two articles on the same order win carry different figures,
// different counterparties and different dates, and the report is only as
// specific as the material under it.
//
// Findings are ranked by trust before truncation, so what gets dropped is the
// least authoritative material rather than an arbitrary tail.
const maxResearchSources = 48

// relativeAgeShort renders a source's age compactly for the prompt.
//
// The model needs to know roughly how old a document is so it can say "as of
// last week" rather than stating a stale figure as current. Precision beyond
// the hour is not useful for that and costs tokens on every source.
func relativeAgeShort(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 0, d < time.Hour:
		return "just now"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}
