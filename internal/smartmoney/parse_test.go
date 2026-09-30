package smartmoney

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseForm4RealFiling(t *testing.T) {
	doc, err := os.ReadFile("testdata/form4_brk.txt")
	if err != nil {
		t.Fatal(err)
	}
	filed := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	d, trades, err := ParseForm4(doc, "0001-26-000001", filed)
	if err != nil {
		t.Fatal(err)
	}
	if d.Issuer.Symbol != "BRK.A" {
		t.Fatalf("issuer symbol = %q", d.Issuer.Symbol)
	}
	if len(trades) == 0 {
		t.Fatal("no trades parsed")
	}
	tr := trades[0]
	if tr.OwnerName != "Jain Ajit" || tr.Code != "S" || tr.Acquired || tr.Shares != 39700 {
		t.Fatalf("unexpected first trade: %+v", tr)
	}
	if tr.Price == nil || *tr.Price < 503 || *tr.Price > 504 {
		t.Fatalf("price = %v", tr.Price)
	}
	if tr.Value == nil || *tr.Value < 19_900_000 || *tr.Value > 20_000_000 {
		t.Fatalf("value = %v", tr.Value)
	}
	if !tr.IsDirector || tr.OfficerTitle != "Vice Chairman" || tr.Direct {
		t.Fatalf("relationship not read: %+v", tr)
	}
	if tr.Role() != "Vice Chairman" {
		t.Fatalf("role = %q", tr.Role())
	}
}

func TestParseDailyIndex(t *testing.T) {
	f, err := os.Open("testdata/form.idx")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	es := ParseDailyIndex(f)
	if len(es) == 0 {
		t.Fatal("no entries")
	}
	e := es[0]
	if e.CIK == "" || strings.HasPrefix(e.CIK, "0") || !strings.HasPrefix(e.Path, "edgar/data/") || e.Date.IsZero() {
		t.Fatalf("bad entry: %+v", e)
	}
	if e.Accession() == "" || strings.HasSuffix(e.Accession(), ".txt") {
		t.Fatalf("bad accession %q", e.Accession())
	}
}

func TestParseInfoTableSumsSubManagers(t *testing.T) {
	b, err := os.ReadFile("testdata/13f_brk.xml")
	if err != nil {
		t.Fatal(err)
	}
	hs, err := ParseInfoTable(b)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var ally *Holding
	for i := range hs {
		k := hs[i].CUSIP + "/" + hs[i].PutCall
		if seen[k] {
			t.Fatalf("cusip %s appears twice after summing", k)
		}
		seen[k] = true
		if hs[i].CUSIP == "02005N100" {
			ally = &hs[i]
		}
	}
	if ally == nil || ally.Shares < 12_561_737+2_803_875+4_228_200 {
		t.Fatalf("Ally rows were not summed: %+v", ally)
	}
	if len(hs) >= 89 {
		t.Fatalf("expected fewer holdings than raw rows, got %d", len(hs))
	}
}

func TestLatest13FSkipsAmendments(t *testing.T) {
	s, err := ParseSubmissions([]byte(`{"name":"X","filings":{"recent":{
		"form":["13F-HR/A","13F-HR","4","13F-HR"],
		"accessionNumber":["a","b","c","d"],
		"reportDate":["2026-06-30","2026-06-30","","2026-03-31"],
		"filingDate":["2026-08-20","2026-08-14","2026-08-01","2026-05-15"],
		"primaryDocument":["","","",""]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := s.Latest13F(2)
	if len(got) != 2 || got[0].Accession != "b" || got[1].Accession != "d" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseInsiderDatasetKeepsOpenMarketTradesInKnownIssuers(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	write("SUBMISSION.tsv", "ACCESSION_NUMBER\tFILING_DATE\tDOCUMENT_TYPE\tISSUERCIK\tISSUERNAME\tISSUERTRADINGSYMBOL\tAFF10B5ONE\n"+
		"A-1\t02-JAN-2026\t4\t0000320193\tApple Inc.\taapl\tfalse\n"+
		"A-2\t02-JAN-2026\t4\t0000000001\tUnknown Co\tZZZZ\tfalse\n"+
		"A-3\t03-JAN-2026\t3\t0000320193\tApple Inc.\tAAPL\tfalse\n")
	write("REPORTINGOWNER.tsv", "ACCESSION_NUMBER\tRPTOWNERCIK\tRPTOWNERNAME\tRPTOWNER_RELATIONSHIP\tRPTOWNER_TITLE\n"+
		"A-1\t0001\tCook Timothy\tDirector,Officer\tCEO\n"+
		"A-1\t0002\tSecond Owner\tDirector\t\n")
	write("NONDERIV_TRANS.tsv", "ACCESSION_NUMBER\tSECURITY_TITLE\tTRANS_DATE\tTRANS_CODE\tTRANS_SHARES\tTRANS_PRICEPERSHARE\tTRANS_ACQUIRED_DISP_CD\tSHRS_OWND_FOLWNG_TRANS\tDIRECT_INDIRECT_OWNERSHIP\n"+
		"A-1\tCommon\t31-DEC-2025\tM\t100\t0\tA\t1100\tD\n"+
		"A-1\tCommon\t31-DEC-2025\tS\t1000\t250.5\tD\t100\tD\n"+
		"A-2\tCommon\t31-DEC-2025\tP\t10\t5\tA\t10\tD\n")
	_ = zw.Close()

	known := func(s string) (string, bool) { return s, s == "AAPL" }
	trades, err := ParseInsiderDataset(buf.Bytes(), known)
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 {
		t.Fatalf("got %d trades, want only the open-market sale in a known issuer: %+v", len(trades), trades)
	}
	tr := trades[0]
	if tr.Symbol != "AAPL" || tr.Code != "S" || tr.Acquired || tr.Line != 1 || tr.OwnerName != "Cook Timothy" ||
		!tr.IsOfficer || !tr.IsDirector || tr.OfficerTitle != "CEO" || tr.Value == nil || *tr.Value != 250500 {
		t.Errorf("trade = %+v", tr)
	}
}

func TestInsiderDatasetURLsNewestFirst(t *testing.T) {
	page := []byte(`<a href="/files/structureddata/data/insider-transactions-data-sets/2025q4_form345.zip">` +
		`<a href="/files/datastandardsinnovation/data/insider-transactions-data-sets/2026q2_form345.zip">` +
		`<a href="/files/structureddata/data/insider-transactions-data-sets/2026q1_form345.zip">`)
	got := InsiderDatasetURLs(page)
	if len(got) != 3 || !strings.HasSuffix(got[0], "2026q2_form345.zip") || !strings.HasSuffix(got[2], "2025q4_form345.zip") {
		t.Errorf("urls = %v", got)
	}
}

func TestParseInfoTableAcceptsDeclaredLatin1(t *testing.T) {
	doc := []byte(`<?xml version="1.0" encoding="ISO-8859-1"?>
<informationTable xmlns="http://www.sec.gov/edgar/document/thirteenf/informationtable">
<infoTable><nameOfIssuer>APPLE INC</nameOfIssuer><cusip>037833100</cusip><value>1000</value>
<shrsOrPrnAmt><sshPrnamt>10</sshPrnamt><sshPrnamtType>SH</sshPrnamtType></shrsOrPrnAmt></infoTable>
</informationTable>`)
	hs, err := ParseInfoTable(doc)
	if err != nil || len(hs) != 1 || hs[0].CUSIP != "037833100" {
		t.Fatalf("holdings = %+v, %v", hs, err)
	}
}
