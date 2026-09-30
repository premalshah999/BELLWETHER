// Package congress reads the House Clerk's Periodic Transaction Reports, the
// STOCK Act filing a member makes within 45 days of trading a security.
//
// The filing index (who filed what, when) is exact: it is the Clerk's own XML.
// Which tickers a filing names is a best-effort reading of the PDF, at
// document level: every parenthesised ticker in it and the earliest
// transaction date. The PDFs are not laid out consistently enough for row-
// level pairing, so none is claimed; each result links to the filing for the
// exact lines.
package congress

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Filing is one entry in the House Clerk's annual disclosure index.
type Filing struct {
	Last, First   string
	StateDistrict string // "MO04"
	FilingType    string // "P" = Periodic Transaction Report, the one this package reads
	FilingDate    time.Time
	Year          int
	DocID         string
}

// StoredFiling is one filing as persisted: the Clerk's own index entry plus
// the PDF extraction result and its resolution against the listed universe.
type StoredFiling struct {
	Filing
	// Chamber is "house" for everything this package can read today. Kept
	// as a field rather than assumed so the schema does not need to change
	// the day a Senate source is added (the Senate's own eFD portal blocks
	// every User-Agent with a 403, so that is not yet this package's job).
	Chamber string
	// Symbols is every extracted ticker that resolved to a real, active US
	// listing -- the set this record can be found by. UnresolvedTickers is
	// everything the regex matched that did not resolve, kept rather than
	// discarded so a reviewer can see what the extractor is missing instead
	// of it disappearing silently.
	Symbols           []string
	UnresolvedTickers []string
	// EarliestTransactionDate is zero when the PDF named no parseable date.
	EarliestTransactionDate time.Time
	// DisclosureDelayDays is FilingDate - EarliestTransactionDate, the
	// STOCK Act's own yardstick (filing is due within 45 days) -- nil when
	// EarliestTransactionDate is zero, rather than 0, because 0 is itself a
	// meaningful same-day disclosure. A negative value means the
	// extraction's best-effort date reading is wrong, which is possible
	// given the source (see the package doc), so it is a signal to distrust
	// the date rather than evidence of an early filing.
	DisclosureDelayDays *int
	DiscoveredAt        time.Time
}

// IsPTR reports whether this filing is a Periodic Transaction Report -- an
// actual buy/sell/exchange disclosure, as opposed to the annual financial
// disclosure (A/C/D/W/X/T/H letter codes the same index carries).
func (f Filing) IsPTR() bool { return f.FilingType == "P" }

// Name is how a filing's member should be shown: "Last, First".
func (f Filing) Name() string { return strings.TrimSpace(f.Last + ", " + f.First) }

// DocURL is where the filing's own PDF lives -- a long-lived, direct
// government URL, always shown alongside any tickers this package read from
// it so a reader can check the source rather than trust the extraction
// blind.
func (f Filing) DocURL() string {
	return fmt.Sprintf("https://disclosures-clerk.house.gov/public_disc/ptr-pdfs/%d/%s.pdf", f.Year, f.DocID)
}

type houseIndexXML struct {
	Members []struct {
		Prefix     string `xml:"Prefix"`
		Last       string `xml:"Last"`
		First      string `xml:"First"`
		Suffix     string `xml:"Suffix"`
		FilingType string `xml:"FilingType"`
		StateDst   string `xml:"StateDst"`
		Year       string `xml:"Year"`
		FilingDate string `xml:"FilingDate"`
		DocID      string `xml:"DocID"`
	} `xml:"Member"`
}

// ParseHouseIndex reads one year's disclosure index (the *FD.xml file inside
// the Clerk's *FD.ZIP), verified live against the 2026 index (2026FD.ZIP,
// 1,626 entries across eight filing-type codes, 388 of them PTRs).
func ParseHouseIndex(body []byte) ([]Filing, error) {
	var doc houseIndexXML
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("congress: parse house index: %w", err)
	}
	out := make([]Filing, 0, len(doc.Members))
	for _, m := range doc.Members {
		year, _ := strconv.Atoi(strings.TrimSpace(m.Year))
		f := Filing{
			Last: strings.TrimSpace(m.Last), First: strings.TrimSpace(m.First),
			StateDistrict: strings.TrimSpace(m.StateDst),
			FilingType:    strings.TrimSpace(m.FilingType),
			Year:          year,
			DocID:         strings.TrimSpace(m.DocID),
		}
		if f.DocID == "" || f.Last == "" {
			continue
		}
		if t, err := time.Parse("1/2/2006", strings.TrimSpace(m.FilingDate)); err == nil {
			f.FilingDate = t
		}
		out = append(out, f)
	}
	return out, nil
}

// FetchHouseIndex downloads and parses one year's disclosure index.
func FetchHouseIndex(ctx context.Context, client *http.Client, userAgent string, year int) ([]Filing, error) {
	url := fmt.Sprintf("https://disclosures-clerk.house.gov/public_disc/financial-pdfs/%dFD.ZIP", year)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("congress: fetch house index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("congress: house index returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	zr, err := newZipXML(body, fmt.Sprintf("%dFD.xml", year))
	if err != nil {
		return nil, err
	}
	return ParseHouseIndex(zr)
}

// tickerInParens matches a stock/ETF ticker written in parentheses right
// after a security's name, e.g. "Apple Inc. - Common Stock (AAPL)". This is
// the one reliably-structured signal these filings offer; see the package
// doc for what it deliberately does not attempt.
var tickerInParens = regexp.MustCompile(`\(([A-Z]{1,5}(?:[.\-][A-Z]{1,2})?)\)`)

// notTickers excludes parenthetical text that matches the ticker pattern by
// shape but is not one -- transaction-status annotations that appear inside
// these same PDFs.
var notTickers = map[string]bool{
	"ST": true, "OT": true, "NEW": true,
}

// dateOrDate matches a pair of MM/DD/YYYY dates as they appear in a
// transaction row (Transaction Date, Notification Date) -- used only to find
// the earliest transaction date a filing names, not to pair a specific date
// with a specific ticker (see the package doc for why not).
var dateOrDate = regexp.MustCompile(`(\d{2}/\d{2}/\d{4})\s+(\d{2}/\d{2}/\d{4})`)

// ExtractPTR reads a report PDF from memory and returns every ticker it names
// and its earliest transaction date. It pipes the bytes to pdftotext -layout:
// two pure-Go PDF libraries reordered or dropped table cells in these forms.
func ExtractPTR(ctx context.Context, pdf []byte) (tickers []string, earliest time.Time, err error) {
	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", "-", "-")
	cmd.Stdin = bytes.NewReader(pdf)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, time.Time{}, fmt.Errorf("congress: pdftotext: %w", err)
	}
	normalized := strings.Join(strings.Fields(out.String()), " ")

	seen := map[string]bool{}
	for _, m := range tickerInParens.FindAllStringSubmatch(normalized, -1) {
		t := m[1]
		if notTickers[t] || seen[t] {
			continue
		}
		seen[t] = true
		tickers = append(tickers, t)
	}
	sort.Strings(tickers)

	for _, m := range dateOrDate.FindAllStringSubmatch(normalized, -1) {
		if d, err := time.Parse("01/02/2006", m[1]); err == nil {
			if earliest.IsZero() || d.Before(earliest) {
				earliest = d
			}
		}
	}
	return tickers, earliest, nil
}
