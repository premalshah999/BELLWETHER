package events

import (
	"strings"
	"testing"
)

func TestInferSectorsCoversBothTaxonomies(t *testing.T) {
	got := InferSectors("Crude oil prices surge as OPEC cuts production")
	want := map[string]bool{"Oil Gas & Consumable Fuels": true, "US: Energy": true}
	if len(got) != len(want) {
		t.Fatalf("InferSectors = %v, want exactly %v", got, want)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected sector %q", s)
		}
	}
}

// TestGICSSectorsNeverCollideWithNSE is the direct regression for the bug
// class ComparePeers had before it was scoped by taxonomy: "Information
// Technology" and "Utilities" are spelled identically in NSE's industry
// classification and in GICS, so a plain, unprefixed GICS sector name would
// be indistinguishable from the NSE one the moment both matched the same
// headline.
func TestGICSSectorsNeverCollideWithNSE(t *testing.T) {
	nse := map[string]bool{}
	for _, sectors := range sectorKeywords {
		for _, s := range sectors {
			nse[s] = true
		}
	}
	for kw, sectors := range gicsKeywords {
		for _, s := range sectors {
			if !strings.HasPrefix(s, "US: ") {
				t.Errorf("gicsKeywords[%q] contains %q, which is not prefixed \"US: \"", kw, s)
			}
			if nse[s] {
				t.Errorf("gicsKeywords[%q] contains %q, which collides with an NSE sector name", kw, s)
			}
		}
	}
	for agency, sectors := range agencySectors {
		for _, s := range sectors {
			if !strings.HasPrefix(s, "US: ") {
				t.Errorf("agencySectors[%q] contains %q, which is not prefixed \"US: \"", agency, s)
			}
			if nse[s] {
				t.Errorf("agencySectors[%q] contains %q, which collides with an NSE sector name", agency, s)
			}
		}
	}
}

func TestSectorsForAgency(t *testing.T) {
	sectors, ok := SectorsForAgency("Office of the United States Trade Representative")
	if !ok {
		t.Fatal("expected USTR to be in the agency table")
	}
	found := false
	for _, s := range sectors {
		if s == "US: Industrials" {
			found = true
		}
	}
	if !found {
		t.Errorf("USTR sectors = %v, want US: Industrials among them", sectors)
	}

	if _, ok := SectorsForAgency("Some Obscure Sub-Agency Nobody Has Heard Of"); ok {
		t.Error("expected an unknown agency to report ok=false, not a guess")
	}
}

func TestInferSectorsCapPerTaxonomy(t *testing.T) {
	// A headline that would independently max out both taxonomies' caps
	// must not have one taxonomy crowd out the other.
	text := "steel iron ore cement construction crude oil brent natural gas coal " +
		"federal reserve fomc tariff section 301 semiconductor fda antitrust"
	got := InferSectors(text)
	var nseCount, usCount int
	for _, s := range got {
		if strings.HasPrefix(s, "US: ") {
			usCount++
		} else {
			nseCount++
		}
	}
	if nseCount == 0 || usCount == 0 {
		t.Fatalf("InferSectors = %v, want both taxonomies represented", got)
	}
}
