package events

import (
	"slices"
	"strings"
	"testing"
)

func TestInferSectors(t *testing.T) {
	if got := InferSectors("Crude oil prices surge as OPEC cuts production"); !slices.Equal(got, []string{"US: Energy"}) {
		t.Errorf("InferSectors = %v, want [US: Energy]", got)
	}
	// Many keywords still yield at most four sectors.
	if got := InferSectors("steel crude oil federal reserve tariff semiconductor fda antitrust telecom"); len(got) != 4 {
		t.Errorf("InferSectors = %v, want the cap of four", got)
	}
}

// Every sector the event pipeline writes carries the "US: " prefix the UI and
// SectorsFor read.
func TestSectorsAreGICSPrefixed(t *testing.T) {
	for _, table := range []map[string][]string{sectorKeywords, agencySectors} {
		for key, sectors := range table {
			for _, s := range sectors {
				if !strings.HasPrefix(s, "US: ") {
					t.Errorf("%q maps to %q, which is not prefixed \"US: \"", key, s)
				}
			}
		}
	}
}

func TestSectorsForAgency(t *testing.T) {
	if sectors, ok := SectorsForAgency("Office of the United States Trade Representative"); !ok || !slices.Contains(sectors, "US: Industrials") {
		t.Errorf("USTR sectors = %v, %v; want US: Industrials among them", sectors, ok)
	}
	if _, ok := SectorsForAgency("Some Obscure Sub-Agency Nobody Has Heard Of"); ok {
		t.Error("an unknown agency must report ok=false, not a guess")
	}
}
