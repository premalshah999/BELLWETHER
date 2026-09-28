package events

// The generic half of what was filing.go, the NSE corporate-filing parser.
//
// That parser is gone. No source in the catalogue uses MethodNSEAnnounce any
// more, so ParseFiling, the NSE document-path regex, the Indian date layouts,
// the "|SUBJECT:" splitter, the corporate-action and board-purpose classifiers
// and the rupee dividend extractor were all unreachable code describing a
// document format this product no longer reads -- 369 lines of it.
//
// This one function survived because a live US path needs it: the Federal
// Register interpreter reads the same pipe-delimited "KEY: value" shape that
// internal/news/engine.go writes when it normalises a feed's own fields, and
// this is what decodes it. It is named for the format rather than for the
// exchange, because the format is all it knows about.

import "strings"

// parsePipeFacts reads a "KEY: value | KEY : value" description into a map.
//
// Keys are upper-cased and trimmed so a caller can look one up without
// guessing at the publisher's spacing. A segment with no colon is skipped
// rather than stored under an empty key.
func parsePipeFacts(desc string) map[string]string {
	facts := map[string]string{}
	for _, part := range strings.Split(desc, "|") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, found := strings.Cut(part, ":")
		if !found {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		facts[key] = value
	}
	return facts
}
