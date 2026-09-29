package server

import "testing"

// A discovery item is named by the outlet that wrote it, not by the query that
// found it; a bare domain is left as a domain and a name keeps its casing.
func TestCleanPublisher(t *testing.T) {
	for in, want := range map[string]string{
		"www.wsj.com":    "wsj.com",
		"Bloomberg.com":  "Bloomberg",
		"Reuters":        "Reuters",
		"sec.gov":        "sec.gov",
		"  PR Newswire ": "PR Newswire",
		"":               "",
	} {
		if got := cleanPublisher(in); got != want {
			t.Errorf("cleanPublisher(%q) = %q, want %q", in, got, want)
		}
	}
}
