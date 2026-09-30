package smartmoney

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SEC is a rate-limited EDGAR client. SEC allows ten requests a second and
// requires a User-Agent naming a real contact; this stays well under the
// limit so ingestion elsewhere in the app keeps its share.
type SEC struct {
	HTTP      *http.Client
	UserAgent string

	mu   sync.Mutex
	last time.Time
}

const secGap = 200 * time.Millisecond

func (c *SEC) wait(ctx context.Context) error {
	c.mu.Lock()
	next := c.last.Add(secGap)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	c.last = next
	c.mu.Unlock()
	select {
	case <-time.After(time.Until(next)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ErrNotFound is a 404: a holiday's missing daily index, for instance.
var ErrNotFound = fmt.Errorf("smartmoney: not found")

// ErrForbidden is a 403. EDGAR returns it, not 404, for a daily index that
// has not been published yet.
var ErrForbidden = fmt.Errorf("smartmoney: forbidden")

// Get fetches a URL, retrying rate limits with a backoff.
func (c *SEC) Get(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusForbidden:
			return nil, ErrForbidden
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("smartmoney: %s returned %d", url, resp.StatusCode)
			select {
			case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("smartmoney: %s returned %d", url, resp.StatusCode)
		case err != nil:
			return nil, err
		}
		return body, nil
	}
	return nil, lastErr
}

// IndexEntry is one line of an EDGAR daily form index.
type IndexEntry struct {
	Form string
	CIK  string
	Date time.Time
	Path string // edgar/data/<cik>/<accession>.txt
}

// Accession is the filing's accession number, from its path.
func (e IndexEntry) Accession() string {
	base := e.Path[strings.LastIndex(e.Path, "/")+1:]
	return strings.TrimSuffix(base, ".txt")
}

// ParseDailyIndex reads form.YYYYMMDD.idx. Columns are fixed-width but the
// company name can contain spaces, so each line is read from the right.
func ParseDailyIndex(r io.Reader) []IndexEntry {
	var out []IndexEntry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	started := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "-----") {
			started = true
			continue
		}
		if !started {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		path := f[len(f)-1]
		date, err := time.Parse("20060102", f[len(f)-2])
		if err != nil {
			continue
		}
		cik := f[len(f)-3]
		out = append(out, IndexEntry{Form: f[0], CIK: strings.TrimLeft(cik, "0"), Date: date, Path: path})
	}
	return out
}

// DailyIndexURL is the EDGAR form index for a day.
func DailyIndexURL(day time.Time) string {
	q := (int(day.Month())-1)/3 + 1
	return fmt.Sprintf("https://www.sec.gov/Archives/edgar/daily-index/%d/QTR%d/form.%s.idx",
		day.Year(), q, day.Format("20060102"))
}

// ---- Form 4 -----------------------------------------------------------

type xmlValue struct {
	Value string `xml:"value"`
}

type form4Doc struct {
	Issuer struct {
		CIK    string `xml:"issuerCik"`
		Name   string `xml:"issuerName"`
		Symbol string `xml:"issuerTradingSymbol"`
	} `xml:"issuer"`
	Owners []struct {
		ID struct {
			CIK  string `xml:"rptOwnerCik"`
			Name string `xml:"rptOwnerName"`
		} `xml:"reportingOwnerId"`
		Rel struct {
			Director string `xml:"isDirector"`
			Officer  string `xml:"isOfficer"`
			TenPct   string `xml:"isTenPercentOwner"`
			Title    string `xml:"officerTitle"`
		} `xml:"reportingOwnerRelationship"`
	} `xml:"reportingOwner"`
	Aff10b51 string `xml:"aff10b5One"`
	Rows     []struct {
		Security xmlValue `xml:"securityTitle"`
		Date     xmlValue `xml:"transactionDate"`
		Coding   struct {
			Code string `xml:"transactionCode"`
		} `xml:"transactionCoding"`
		Amounts struct {
			Shares xmlValue `xml:"transactionShares"`
			Price  xmlValue `xml:"transactionPricePerShare"`
			AD     xmlValue `xml:"transactionAcquiredDisposedCode"`
		} `xml:"transactionAmounts"`
		Post struct {
			Owned xmlValue `xml:"sharesOwnedFollowingTransaction"`
		} `xml:"postTransactionAmounts"`
		Nature struct {
			DI xmlValue `xml:"directOrIndirectOwnership"`
		} `xml:"ownershipNature"`
	} `xml:"nonDerivativeTable>nonDerivativeTransaction"`
}

var ownershipRe = regexp.MustCompile(`(?s)<ownershipDocument>.*?</ownershipDocument>`)

func truthy(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "1" || s == "true"
}

func parseNum(s string) (float64, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// ParseForm4 reads the non-derivative transactions out of a Form 4's full
// submission text. The owner recorded is the first reporting owner; joint
// filings list the same trade under each, which would double count it.
func ParseForm4(doc []byte, accession string, filed time.Time) (form4Doc, []Trade, error) {
	var d form4Doc
	m := ownershipRe.Find(doc)
	if m == nil {
		return d, nil, fmt.Errorf("smartmoney: %s has no ownership document", accession)
	}
	if err := xml.NewDecoder(bytes.NewReader(m)).Decode(&d); err != nil {
		return d, nil, fmt.Errorf("smartmoney: parse %s: %w", accession, err)
	}
	if len(d.Owners) == 0 {
		return d, nil, nil
	}
	o := d.Owners[0]
	plan := truthy(d.Aff10b51)
	var out []Trade
	for i, r := range d.Rows {
		date, err := time.Parse("2006-01-02", strings.TrimSpace(r.Date.Value)[:min(10, len(strings.TrimSpace(r.Date.Value)))])
		if err != nil {
			continue
		}
		shares, ok := parseNum(r.Amounts.Shares.Value)
		if !ok || shares == 0 {
			continue
		}
		t := Trade{
			Accession:    accession,
			Line:         i,
			IssuerCIK:    strings.TrimLeft(d.Issuer.CIK, "0"),
			IssuerName:   strings.TrimSpace(d.Issuer.Name),
			OwnerCIK:     strings.TrimLeft(o.ID.CIK, "0"),
			OwnerName:    strings.TrimSpace(o.ID.Name),
			IsDirector:   truthy(o.Rel.Director),
			IsOfficer:    truthy(o.Rel.Officer),
			IsTenPct:     truthy(o.Rel.TenPct),
			OfficerTitle: strings.TrimSpace(o.Rel.Title),
			Security:     strings.TrimSpace(r.Security.Value),
			TxDate:       date,
			Code:         strings.ToUpper(strings.TrimSpace(r.Coding.Code)),
			Acquired:     strings.EqualFold(strings.TrimSpace(r.Amounts.AD.Value), "A"),
			Shares:       shares,
			Direct:       !strings.EqualFold(strings.TrimSpace(r.Nature.DI.Value), "I"),
			Plan105b1:    plan,
			FiledAt:      filed,
		}
		if p, ok := parseNum(r.Amounts.Price.Value); ok && p > 0 {
			v := p * shares
			t.Price, t.Value = &p, &v
		}
		if a, ok := parseNum(r.Post.Owned.Value); ok {
			t.OwnedAfter = &a
		}
		out = append(out, t)
	}
	return d, out, nil
}

// ---- 13F ----------------------------------------------------------------

// Submissions is the part of data.sec.gov/submissions this package reads.
type Submissions struct {
	Name    string `json:"name"`
	Filings struct {
		Recent struct {
			Form        []string `json:"form"`
			Accession   []string `json:"accessionNumber"`
			ReportDate  []string `json:"reportDate"`
			FilingDate  []string `json:"filingDate"`
			PrimaryDocs []string `json:"primaryDocument"`
		} `json:"recent"`
	} `json:"filings"`
}

// Latest13F returns up to n original (not amended) 13F-HR filings, newest
// first.
func (s Submissions) Latest13F(n int) []FundFiling {
	r := s.Filings.Recent
	var out []FundFiling
	for i := range r.Form {
		if r.Form[i] != "13F-HR" || i >= len(r.Accession) {
			continue
		}
		period, _ := time.Parse("2006-01-02", r.ReportDate[i])
		filed, _ := time.Parse("2006-01-02", r.FilingDate[i])
		out = append(out, FundFiling{Accession: r.Accession[i], Period: period, Filed: filed})
		if len(out) == n {
			break
		}
	}
	return out
}

func ParseSubmissions(b []byte) (Submissions, error) {
	var s Submissions
	err := json.Unmarshal(b, &s)
	return s, err
}

type infoTable struct {
	Rows []struct {
		Issuer  string `xml:"nameOfIssuer"`
		CUSIP   string `xml:"cusip"`
		Value   string `xml:"value"`
		Shares  string `xml:"shrsOrPrnAmt>sshPrnamt"`
		Type    string `xml:"shrsOrPrnAmt>sshPrnamtType"`
		PutCall string `xml:"putCall"`
	} `xml:"infoTable"`
}

// ParseInfoTable reads a 13F information table and sums rows that are the
// same security held through different sub-managers. Values are dollars
// (filings since 2023 report whole dollars, not thousands).
func ParseInfoTable(b []byte) ([]Holding, error) {
	var t infoTable
	if err := xml.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("smartmoney: parse information table: %w", err)
	}
	type key struct{ cusip, pc string }
	sum := map[key]*Holding{}
	var order []key
	for _, r := range t.Rows {
		if !strings.EqualFold(strings.TrimSpace(r.Type), "SH") && strings.TrimSpace(r.Type) != "" {
			continue // principal amounts of bonds, not shares
		}
		k := key{strings.ToUpper(strings.TrimSpace(r.CUSIP)), strings.ToUpper(strings.TrimSpace(r.PutCall))}
		v, _ := parseNum(r.Value)
		sh, _ := parseNum(r.Shares)
		h, ok := sum[k]
		if !ok {
			h = &Holding{CUSIP: k.cusip, Issuer: strings.TrimSpace(r.Issuer), PutCall: k.pc}
			sum[k] = h
			order = append(order, k)
		}
		h.Value += v
		h.Shares += sh
	}
	out := make([]Holding, 0, len(order))
	for _, k := range order {
		out = append(out, *sum[k])
	}
	return out, nil
}

// FilingIndex is the directory listing of one filing.
type FilingIndex struct {
	Directory struct {
		Item []struct {
			Name string `json:"name"`
		} `json:"item"`
	} `json:"directory"`
}

// InfoTableName picks the information table from a 13F filing's files: an
// XML document other than primary_doc.xml.
func (f FilingIndex) InfoTableName() string {
	for _, it := range f.Directory.Item {
		n := strings.ToLower(it.Name)
		if strings.HasSuffix(n, ".xml") && n != "primary_doc.xml" {
			return it.Name
		}
	}
	return ""
}
