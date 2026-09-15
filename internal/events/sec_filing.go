package events

import (
	"regexp"
	"strings"
	"time"
)

// SECFiling is one SEC EDGAR disclosure, read into structured form. The US
// analogue of Filing: everything on it comes from what the filer declared,
// which is what lets the entity resolution rely on the CIK rather than a
// text guess.
type SECFiling struct {
	Company  string // as SEC's feed names the filer
	CIK      string // 10-digit, zero-padded
	Role     string // "Filer" | "Issuer" | "Reporting" -- who this entry names
	FormType string // "8-K", "8-K/A", "4", "13F-HR", ...
	AccNo    string // accession number, SEC's own filing identifier
	Type     Type
	// ItemCodes are the 8-K item numbers this filing declared ("8.01",
	// "5.02"), kept verbatim alongside the derived Type: the filer's own
	// declaration is a stronger signal than any classifier, and a filing
	// naming several items (a common case) has more going on than the one
	// Type this maps to captures.
	ItemCodes []string
	FiledDate time.Time
}

// secTitle reads SEC's "<form> - <name> (<cik>) (<role>)" title.
//
// The trailing role tag matters beyond labelling: Form 4 emits *two* entries
// per filing under the same accession number, one for the issuer and one for
// the person who filed it. Keeping only "Issuer" entries is what keeps a
// single filing from becoming two raw items with two different CIKs, one of
// which names a person rather than a company.
var secTitle = regexp.MustCompile(`^(\S+)\s+-\s+(.*?)\s+\((\d{7,10})\)\s+\((Filer|Issuer|Reporting)\)\s*$`)

// secSummaryFields reads "Filed: 2026-09-14 AccNo: 0001663577-26-000275 Size: 191 KB".
var (
	secFiledDate = regexp.MustCompile(`Filed:\s*([\d-]+)`)
	secAccNo     = regexp.MustCompile(`AccNo:\s*([\d-]+)`)
	// secItemCode matches an 8-K item declaration ("Item 8.01: Other
	// Events"), capturing just the code. cleanText has already flattened
	// every <br> to a space, so consecutive items run together in the plain
	// text ("...Other Events Item 9.01: Financial...") with no separator
	// between one item's free-text description and the next item's code --
	// which is why only the code is captured, not an attempt at the text
	// after it.
	secItemCode = regexp.MustCompile(`Item\s+(\d{1,2}\.\d{2}):`)
)

// secItemType maps an 8-K item number to this system's taxonomy. Item
// numbers are literal SEC regulation citations (Item 8.01 of Form 8-K under
// 17 CFR 249.308), not something that changes with a redesign.
//
// This is a stronger signal than the keyword classifier NSE-style filings
// fall back to: the filer is declaring the item under penalty of the
// securities laws, not being described by a third party.
var secItemType = map[string]Type{
	"1.01": TypeContract,           // Entry into a Material Definitive Agreement
	"1.02": TypeAdministrative,     // Termination of a Material Definitive Agreement
	"1.03": TypeInsolvency,         // Bankruptcy or Receivership
	"2.01": TypeAcquisition,        // Completion of Acquisition or Disposition of Assets
	"2.02": TypeEarnings,           // Results of Operations and Financial Condition
	"2.03": TypeDebt,               // Creation of a Direct Financial Obligation
	"2.04": TypeDebt,               // Triggering Events That Accelerate a Direct Financial Obligation
	"2.05": TypeAdministrative,     // Costs Associated with Exit or Disposal Activities
	"2.06": TypeAdministrative,     // Material Impairments
	"3.01": TypeRegulatoryAction,   // Notice of Delisting or Failure to Satisfy Listing Rule
	"3.02": TypeAllotment,          // Unregistered Sales of Equity Securities
	"3.03": TypeShareholdingChange, // Material Modification to Rights of Security Holders
	"4.01": TypeAuditorChange,      // Changes in Registrant's Certifying Accountant
	"4.02": TypeAdministrative,     // Non-Reliance on Previously Issued Financials
	"5.01": TypeStakeSale,          // Changes in Control of Registrant
	"5.02": TypeManagementChange,   // Departure/Election of Directors or Officers
	"5.03": TypeAdministrative,     // Amendments to Articles of Incorporation or Bylaws
	"5.07": TypeAGM,                // Submission of Matters to a Vote of Security Holders
	"6.01": TypeAdministrative,     // ABS Informational and Computational Material
	"7.01": TypePressRelease,       // Regulation FD Disclosure
	"8.01": TypeAdministrative,     // Other Events
	"9.01": TypeAdministrative,     // Financial Statements and Exhibits
}

// ParseSECFiling reads one item from an SEC EDGAR current-filings feed.
// title and description are the raw feed fields, already HTML-unescaped and
// tag-stripped by the generic feed parser (see cleanText in internal/news) --
// this only imposes SEC's own structure on top of that plain text, the same
// division of labour as ParseFiling for NSE.
func ParseSECFiling(title, description string) SECFiling {
	f := SECFiling{Type: TypeUnclassified}

	if m := secTitle.FindStringSubmatch(title); m != nil {
		f.FormType, f.Company, f.CIK, f.Role = m[1], strings.TrimSpace(m[2]), m[3], m[4]
	} else {
		// Unrecognized title shape: keep the title as the company name
		// rather than dropping the filing. Downstream entity resolution
		// simply finds nothing without a CIK, which is the same outcome as
		// any other unresolvable mention.
		f.Company = strings.TrimSpace(title)
	}
	if m := secFiledDate.FindStringSubmatch(description); m != nil {
		if t, err := time.ParseInLocation("2006-01-02", m[1], time.UTC); err == nil {
			f.FiledDate = t
		}
	}
	if m := secAccNo.FindStringSubmatch(description); m != nil {
		f.AccNo = m[1]
	}
	for _, m := range secItemCode.FindAllStringSubmatch(description, -1) {
		f.ItemCodes = append(f.ItemCodes, m[1])
	}

	switch {
	case strings.HasPrefix(f.FormType, "8-K"):
		f.Type = classifySECItems(f.ItemCodes)
	case f.FormType == "4":
		f.Type = TypeInsiderTransaction
	case strings.HasPrefix(f.FormType, "13F"):
		f.Type = TypeShareholdingChange
	}
	return f
}

// classifySECItems picks one Type for a filing that may declare several
// items. The first-listed item is the filer's own lead -- SEC's form
// instructions list items in the order the filer chooses to disclose them,
// and item 9.01 (financial statements and exhibits, present on almost every
// 8-K as a boilerplate attachment note) is deliberately never allowed to win
// over something substantive appearing after it.
func classifySECItems(codes []string) Type {
	best := TypeUnclassified
	for _, code := range codes {
		t, ok := secItemType[code]
		if !ok {
			continue
		}
		if best == TypeUnclassified {
			best = t
		}
		if code != "9.01" && code != "8.01" {
			// A specific, substantive item outranks the two catch-alls
			// regardless of position.
			return t
		}
	}
	return best
}

// buildSECHeadline composes a readable headline the way buildFilingHeadline
// does for NSE.
func buildSECHeadline(f SECFiling) string {
	form := f.FormType
	switch {
	case strings.HasPrefix(form, "8-K"):
		if len(f.ItemCodes) > 0 {
			return f.Company + ": Form " + form + " (Item " + strings.Join(f.ItemCodes, ", ") + ")"
		}
		return f.Company + ": Form " + form
	case form == "4":
		return f.Company + ": insider transaction (Form 4)"
	case strings.HasPrefix(form, "13F"):
		return f.Company + ": institutional holdings report (" + form + ")"
	default:
		return f.Company + ": Form " + form
	}
}
