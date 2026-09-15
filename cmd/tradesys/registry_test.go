package main

import (
	"testing"

	"github.com/tradesys/dashboard/internal/news"
)

// TestBuildRegistrySECGating locks in the actual guard against sending SEC
// an undeclared-contact User-Agent: an empty SEC_USER_AGENT omits the SEC
// sources from the catalog entirely, rather than registering them and
// letting every poll fail with a 403.
func TestBuildRegistrySECGating(t *testing.T) {
	without, err := buildRegistry("")
	if err != nil {
		t.Fatalf("buildRegistry(\"\"): %v", err)
	}
	for _, id := range []string{"sec-8k", "sec-form4", "sec-13f"} {
		if _, ok := without.Get(id); ok {
			t.Errorf("buildRegistry(\"\") registered %s; SEC sources must be omitted without a contact UA", id)
		}
	}

	with, err := buildRegistry("TradeSys/1.0 (test@example.com)")
	if err != nil {
		t.Fatalf("buildRegistry(ua): %v", err)
	}
	for _, id := range []string{"sec-8k", "sec-form4", "sec-13f"} {
		src, ok := with.Get(id)
		if !ok {
			t.Errorf("buildRegistry(ua) did not register %s", id)
			continue
		}
		if src.Method != news.MethodSECFiling {
			t.Errorf("%s: Method = %s, want %s", id, src.Method, news.MethodSECFiling)
		}
	}
	// The rest of the catalog must still be present -- this is additive,
	// not a replacement of the static list.
	if _, ok := with.Get("nse-announcements"); !ok {
		t.Error("buildRegistry dropped the static catalog while adding SEC sources")
	}
}
