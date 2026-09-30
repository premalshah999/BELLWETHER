package smartmoney

import (
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
