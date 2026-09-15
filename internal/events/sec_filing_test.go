package events

import "testing"

// These titles and descriptions are transcribed from live SEC EDGAR
// current-filings feed responses (2026-09-15), after the generic Atom
// parser's cleanText has unescaped entities, stripped tags, and collapsed
// whitespace -- exactly the shape ParseSECFiling actually receives.
func TestParseSECFiling8K(t *testing.T) {
	title := "8-K/A - House of Doge Inc. (0001903595) (Filer)"
	desc := "Filed: 2026-09-14 AccNo: 0001213900-26-099794 Size: 1 MB " +
		"Item 2.01: Completion of Acquisition or Disposition of Assets " +
		"Item 3.01: Notice of Delisting or Failure to Satisfy a Continued Listing Rule or Standard; Transfer of Listing " +
		"Item 5.02: Departure of Directors or Certain Officers; Election of Directors; Appointment of Certain Officers: Compensatory Arrangements of Certain Officers " +
		"Item 8.01: Other Events"

	f := ParseSECFiling(title, desc)
	if f.FormType != "8-K/A" {
		t.Errorf("FormType = %q, want 8-K/A", f.FormType)
	}
	if f.Company != "House of Doge Inc." {
		t.Errorf("Company = %q, want %q", f.Company, "House of Doge Inc.")
	}
	if f.CIK != "0001903595" {
		t.Errorf("CIK = %q, want 0001903595", f.CIK)
	}
	if f.Role != "Filer" {
		t.Errorf("Role = %q, want Filer", f.Role)
	}
	if f.AccNo != "0001213900-26-099794" {
		t.Errorf("AccNo = %q", f.AccNo)
	}
	wantItems := []string{"2.01", "3.01", "5.02", "8.01"}
	if len(f.ItemCodes) != len(wantItems) {
		t.Fatalf("ItemCodes = %v, want %v", f.ItemCodes, wantItems)
	}
	for i, c := range wantItems {
		if f.ItemCodes[i] != c {
			t.Errorf("ItemCodes[%d] = %q, want %q", i, f.ItemCodes[i], c)
		}
	}
	// 2.01 (acquisition) is substantive and must win over 8.01 despite
	// appearing later, and over 5.02 despite appearing first among the
	// substantive items -- classifySECItems returns the first substantive
	// hit in filer-declared order, and 2.01 is that here.
	if f.Type != TypeAcquisition {
		t.Errorf("Type = %s, want ACQUISITION (item 2.01 outranks 5.02/8.01)", f.Type)
	}
	if f.FiledDate.Format("2006-01-02") != "2026-09-14" {
		t.Errorf("FiledDate = %v, want 2026-09-14", f.FiledDate)
	}
}

func TestParseSECFiling8KEarningsOnly(t *testing.T) {
	title := "8-K - Example Corp (0001234567) (Filer)"
	desc := "Filed: 2026-09-14 AccNo: 0001663577-26-000275 Size: 191 KB Item 2.02: Results of Operations and Financial Condition Item 9.01: Financial Statements and Exhibits"
	f := ParseSECFiling(title, desc)
	if f.Type != TypeEarnings {
		t.Errorf("Type = %s, want EARNINGS", f.Type)
	}
}

func TestParseSECFilingOnlyBoilerplateItems(t *testing.T) {
	// A filing that names only the two catch-all items (7.01, 8.01) has
	// nothing substantive to classify as; it falls back to whichever
	// catch-all appeared first rather than UNCLASSIFIED, since a filer
	// declaring anything at all is more than nothing.
	title := "8-K - Example Corp (0001234567) (Filer)"
	desc := "Filed: 2026-09-14 AccNo: 0001663577-26-000275 Size: 12 KB Item 8.01: Other Events"
	f := ParseSECFiling(title, desc)
	if f.Type != TypeAdministrative {
		t.Errorf("Type = %s, want ADMINISTRATIVE (only item 8.01 declared)", f.Type)
	}
}

func TestParseSECFilingForm4KeepsOnlyIssuerRole(t *testing.T) {
	issuer := ParseSECFiling(
		"4 - Chime Financial, Inc. (0001795586) (Issuer)",
		"Filed: 2026-09-14 AccNo: 0001376066-26-000007 Size: 25 KB")
	reporting := ParseSECFiling(
		"4 - CAROLAN SHAWN T (0001376066) (Reporting)",
		"Filed: 2026-09-14 AccNo: 0001376066-26-000007 Size: 25 KB")

	if issuer.Role != "Issuer" || issuer.CIK != "0001795586" {
		t.Errorf("issuer entry = %+v", issuer)
	}
	if reporting.Role != "Reporting" || reporting.CIK != "0001376066" {
		t.Errorf("reporting entry = %+v", reporting)
	}
	if issuer.Type != TypeInsiderTransaction || reporting.Type != TypeInsiderTransaction {
		t.Errorf("both entries of a Form 4 should classify as INSIDER_TRANSACTION: issuer=%s reporting=%s",
			issuer.Type, reporting.Type)
	}
	// The two entries share one accession number -- the mechanism by which
	// a caller filters to the Issuer-tagged entry alone.
	if issuer.AccNo != reporting.AccNo {
		t.Errorf("accession numbers differ: %q vs %q, want equal", issuer.AccNo, reporting.AccNo)
	}
}

func TestParseSECFiling13F(t *testing.T) {
	f := ParseSECFiling(
		"13F-HR - Davis Wealth Advisors, LLC (0002104502) (Filer)",
		"Filed: 2026-09-14 AccNo: 0001214659-26-011672 Size: 42 KB")
	if f.Type != TypeShareholdingChange {
		t.Errorf("Type = %s, want SHAREHOLDING_CHANGE", f.Type)
	}
	if f.CIK != "0002104502" {
		t.Errorf("CIK = %q, want 0002104502", f.CIK)
	}
}

func TestParseSECFilingUnrecognizedTitleStillYieldsCompany(t *testing.T) {
	f := ParseSECFiling("Something unexpected", "no structured fields here")
	if f.Company != "Something unexpected" {
		t.Errorf("Company = %q, want the whole title kept", f.Company)
	}
	if f.CIK != "" {
		t.Errorf("CIK = %q, want empty (unresolvable, not a bug)", f.CIK)
	}
	if f.Type != TypeUnclassified {
		t.Errorf("Type = %s, want UNCLASSIFIED", f.Type)
	}
}

func TestBuildSECHeadline(t *testing.T) {
	f := SECFiling{Company: "Acme Corp", FormType: "8-K", ItemCodes: []string{"2.02"}}
	got := buildSECHeadline(f)
	want := "Acme Corp: Form 8-K (Item 2.02)"
	if got != want {
		t.Errorf("headline = %q, want %q", got, want)
	}
}
