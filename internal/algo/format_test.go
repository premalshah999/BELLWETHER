package algo

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := map[float64]string{
		0:  "0",
		1:  "1.00",
		35: "35.00",
		// 52.535 is not exactly representable in float64 (it stores as
		// 52.5349…), so use a value whose rounding is unambiguous.
		52.536: "52.54",
		0.5:    "0.5000",
		1316:   "1,316.00",
		2431.5: "2,431.50",
		// Share volumes are whole numbers; two decimal places on them is noise.
		4679682: "4,679,682",
		123456:  "123,456",
		// A large non-integral value keeps its decimals.
		123456.78: "123,456.78",
		// Past ten million, compact notation is more readable than 8 digits.
		27770000: "27.77M",
		-1500:    "-1,500.00",
	}
	for in, want := range tests {
		if got := formatNumber(in); got != want {
			t.Errorf("formatNumber(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatMultiplier(t *testing.T) {
	// A "1.5x average" rule must read as 1.5x, not 1.5000x.
	tests := map[float64]string{
		1.5:         "1.5",
		0.1:         "0.1",
		2:           "2",
		1.25:        "1.25",
		0.001:       "0.001",
		3.333333333: "3.3333",
	}
	for in, want := range tests {
		if got := formatMultiplier(in); got != want {
			t.Errorf("formatMultiplier(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAddThousands(t *testing.T) {
	tests := map[string]string{
		"0":          "0",
		"1":          "1",
		"999":        "999",
		"1000":       "1,000",
		"1000.50":    "1,000.50",
		"1234567.89": "1,234,567.89",
		"-2500.00":   "-2,500.00",
	}
	for in, want := range tests {
		if got := addThousands(in); got != want {
			t.Errorf("addThousands(%q) = %q, want %q", in, got, want)
		}
	}
}
