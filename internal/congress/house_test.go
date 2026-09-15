package congress

import (
	"context"
	"os"
	"testing"
)

func TestParseHouseIndexRealFile(t *testing.T) {
	body, err := os.ReadFile("testdata/2026FD.ZIP")
	if err != nil {
		t.Fatal(err)
	}
	xmlBody, err := newZipXML(body, "2026FD.xml")
	if err != nil {
		t.Fatal(err)
	}
	filings, err := ParseHouseIndex(xmlBody)
	if err != nil {
		t.Fatal(err)
	}
	if len(filings) < 1000 {
		t.Fatalf("parsed %d filings, want at least 1000 (the real 2026 index has 1,626)", len(filings))
	}

	var ptrs int
	var alford *Filing
	for i := range filings {
		if filings[i].IsPTR() {
			ptrs++
		}
		if filings[i].Last == "Alford" && filings[i].DocID == "20034201" {
			alford = &filings[i]
		}
	}
	if ptrs < 300 {
		t.Errorf("PTR count = %d, want at least 300 (the real index has 388)", ptrs)
	}
	if alford == nil {
		t.Fatal("did not find the known Alford PTR filing (DocID 20034201) in the parsed index")
	}
	if alford.StateDistrict != "MO04" {
		t.Errorf("StateDistrict = %q, want MO04", alford.StateDistrict)
	}
	if alford.FilingDate.Format("2006-01-02") != "2026-03-31" {
		t.Errorf("FilingDate = %v, want 2026-03-31", alford.FilingDate)
	}
	if got := alford.DocURL(); got != "https://disclosures-clerk.house.gov/public_disc/ptr-pdfs/2026/20034201.pdf" {
		t.Errorf("DocURL = %q", got)
	}
}

// TestExtractPTRRealFile is the regression for the whole reason this reads
// at document level rather than row level: the real filing this fixture
// captures wraps some tickers' parentheticals onto a different line than
// their transaction row, and names at least one ETF holding (Invesco QQQ)
// without a parenthesized ticker at all. A row-pairing extraction would
// either drop entries or mis-pair a date with the wrong ticker; this does
// neither, because it does not attempt the pairing.
func TestExtractPTRRealFile(t *testing.T) {
	if _, err := os.Stat("/usr/bin/pdftotext"); err != nil {
		t.Skip("pdftotext not installed in this environment")
	}
	pdf, err := os.ReadFile("testdata/sample_ptr.pdf")
	if err != nil {
		t.Fatal(err)
	}
	tickers, earliest, err := ExtractPTR(context.Background(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"AMZN": true, "AAPL": true, "PYPL": true, "SPYB": true}
	got := map[string]bool{}
	for _, tk := range tickers {
		got[tk] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("tickers = %v, want %s among them", tickers, w)
		}
	}
	// "T" (AT&T) is a single letter that also appears constantly as an
	// ordinary word fragment in this PDF's dollar-amount-per-share
	// narrative text ("T – 37.426 shares sold @..."); confirming it is
	// found is really confirming the parenthesized-ticker pattern is
	// selective enough not to be swamped by that noise.
	if !got["T"] {
		t.Errorf("tickers = %v, want T (AT&T) among them", tickers)
	}
	if earliest.IsZero() {
		t.Fatal("no transaction date found")
	}
	if got := earliest.Format("2006-01-02"); got != "2026-03-16" {
		t.Errorf("earliest transaction date = %s, want 2026-03-16", got)
	}
}
