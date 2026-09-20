package ai

import (
	"strconv"
	"time"
)

// A bounded evidence packet keeps one synthesis within a predictable budget.
const maxResearchSources = 12

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
