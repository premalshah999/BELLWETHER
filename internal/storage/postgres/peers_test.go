package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/fundamentals"
)

func f64(v float64) *float64 { return &v }

// TestComparePeersScopesByTaxonomy is the regression for two real bugs found
// while verifying the venue-qualification migration against production:
// (1) ComparePeers pooled peer groups across venues whenever an NSE
// industry label and a GICS sector label happened to read the same, and
// (2) the fix's first attempt passed an unused positional parameter to the
// peer-count query, which Postgres rejects outright ("could not determine
// data type of parameter") because the placeholder never appears in that
// query's own text -- a class of bug no amount of go vet or unit testing
// against a mock catches, only a real Postgres connection.
func TestComparePeersScopesByTaxonomy(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// index_constituents is replaced wholesale by ReplaceIndexConstituents,
	// so every row for this test is built up front and written once.
	rows := []ConstituentListing{}
	nse := []string{"NA", "NB", "NC", "ND", "NE", "NF"}
	us := []string{"UA", "UB", "UC", "UD", "UE", "UF"}
	for _, s := range nse {
		rows = append(rows, ConstituentListing{Symbol: s + ".NSE", Industry: "Software", Venue: "NSE", Taxonomy: "nse-industry"})
	}
	for _, s := range us {
		rows = append(rows, ConstituentListing{Symbol: s, Industry: "Software", Venue: "US", Taxonomy: "gics"})
	}
	if err := db.ReplaceIndexConstituents(ctx, rows); err != nil {
		t.Fatalf("seed index_constituents: %v", err)
	}

	for i, s := range nse {
		if err := db.SaveFundamentals(ctx, fundamentals.Company{
			Snapshot: fundamentals.Snapshot{
				Symbol: s + ".NSE", AsOf: time.Now().UTC(),
				QuoteCurrency: "INR", FinancialCurrency: "INR",
				PETrailing: f64(float64(10 + i)), // 10..15
			},
		}); err != nil {
			t.Fatalf("seed fundamentals for %s.NSE: %v", s, err)
		}
	}
	for i, s := range us {
		if err := db.SaveFundamentals(ctx, fundamentals.Company{
			Snapshot: fundamentals.Snapshot{
				Symbol: s, AsOf: time.Now().UTC(),
				QuoteCurrency: "USD", FinancialCurrency: "USD",
				PETrailing: f64(float64(100 + i)), // 100..105, deliberately disjoint from the NSE range
			},
		}); err != nil {
			t.Fatalf("seed fundamentals for %s: %v", s, err)
		}
	}

	pc, err := db.ComparePeers(ctx, "NA.NSE")
	if err != nil {
		t.Fatalf("ComparePeers: %v", err)
	}
	if pc.Peers != len(nse) {
		t.Fatalf("peer count = %d, want %d (the NSE group only -- a taxonomy leak would give %d)",
			pc.Peers, len(nse), len(nse)+len(us))
	}

	var pe *PeerStat
	for i := range pc.Stats {
		if pc.Stats[i].Metric == "P/E" {
			pe = &pc.Stats[i]
		}
	}
	if pe == nil || pe.Value == nil || pe.Median == nil {
		t.Fatalf("P/E stat missing or incomplete: %+v", pc.Stats)
	}
	// The median of 10..15 is 12.5. If the US group (100..105) leaked in,
	// the median would be pulled far higher.
	if *pe.Median < 12 || *pe.Median > 13 {
		t.Errorf("P/E median = %v, want ~12.5 (NSE group only)", *pe.Median)
	}

	usPC, err := db.ComparePeers(ctx, "UA")
	if err != nil {
		t.Fatalf("ComparePeers(US): %v", err)
	}
	if usPC.Peers != len(us) {
		t.Fatalf("US peer count = %d, want %d", usPC.Peers, len(us))
	}
}
