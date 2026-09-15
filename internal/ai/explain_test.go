package ai

import "testing"

func TestStripUnresolvedCitations(t *testing.T) {
	// A model that cites [1] when handed no sources is asserting evidence
	// that was never supplied. That marker must not reach the reader.
	tests := []struct {
		name        string
		text        string
		available   int
		wantText    string
		wantDropped int
	}{
		{
			name:      "valid citations are kept",
			text:      "The move tracks the index [1] and volume was light [2].",
			available: 2,
			wantText:  "The move tracks the index [1] and volume was light [2].",
		},
		{
			name:        "citation with no sources at all",
			text:        "The move tracks the broader index [1].",
			available:   0,
			wantText:    "The move tracks the broader index.",
			wantDropped: 1,
		},
		{
			name:        "citation beyond the supplied range",
			text:        "Margins fell [1] and guidance was cut [7].",
			available:   2,
			wantText:    "Margins fell [1] and guidance was cut.",
			wantDropped: 1,
		},
		{
			name:        "several invalid markers",
			text:        "One [4], two [5], three [6].",
			available:   1,
			wantText:    "One, two, three.",
			wantDropped: 3,
		},
		{
			name:      "no markers at all",
			text:      "No clear catalyst in the available sources.",
			available: 3,
			wantText:  "No clear catalyst in the available sources.",
		},
		{
			// Zero is not a valid source number; sources start at 1.
			name:        "zero index is invalid",
			text:        "Something happened [0].",
			available:   3,
			wantText:    "Something happened.",
			wantDropped: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := stripUnresolvedCitations(tc.text, tc.available)
			if got != tc.wantText {
				t.Errorf("text = %q, want %q", got, tc.wantText)
			}
			if dropped != tc.wantDropped {
				t.Errorf("dropped = %d, want %d", dropped, tc.wantDropped)
			}
		})
	}
}

func TestNormaliseConfidence(t *testing.T) {
	tests := map[string]string{
		"high":     "high",
		"High":     "high",
		"HIGH":     "high",
		"medium":   "medium",
		"moderate": "medium",
		"low":      "low",
		// An unrecognised answer is treated as low: reading it as "high"
		// would overstate what the model actually said.
		"":         "low",
		"probably": "low",
		"certain":  "low",
	}
	for in, want := range tests {
		if got := normaliseConfidence(in); got != want {
			t.Errorf("normaliseConfidence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAppendNote(t *testing.T) {
	if got := appendNote("", "b"); got != "b" {
		t.Errorf("appendNote(\"\", \"b\") = %q", got)
	}
	if got := appendNote("a", "b"); got != "a b" {
		t.Errorf("appendNote(\"a\", \"b\") = %q", got)
	}
}
