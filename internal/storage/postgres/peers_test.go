package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/fundamentals"
)

func f64(v float64) *float64 { return &v }

// A company is compared only with its own sector. Run against real Postgres:
// a placeholder the query text never references fails there ("could not
// determine data type of parameter"), which no mock catches.
func TestComparePeersScopesBySector(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	software := []string{"SA", "SB", "SC", "SD", "SE", "SF"}
	banks := []string{"BA", "BB", "BC", "BD", "BE", "BF"}
	var rows []ConstituentListing
	for _, s := range software {
		rows = append(rows, ConstituentListing{Symbol: s, Industry: "Software"})
	}
	for _, s := range banks {
		rows = append(rows, ConstituentListing{Symbol: s, Industry: "Banks"})
	}
	if err := db.ReplaceIndexConstituents(ctx, rows); err != nil {
		t.Fatalf("seed index_constituents: %v", err)
	}
	seed := func(syms []string, base float64) {
		for i, s := range syms {
			if err := db.SaveFundamentals(ctx, fundamentals.Company{Snapshot: fundamentals.Snapshot{
				Symbol: s, AsOf: time.Now().UTC(), QuoteCurrency: "USD", FinancialCurrency: "USD",
				PETrailing: f64(base + float64(i)),
			}}); err != nil {
				t.Fatalf("seed fundamentals for %s: %v", s, err)
			}
		}
	}
	seed(software, 10) // 10..15
	seed(banks, 100)   // 100..105, disjoint so a leak moves the median

	pc, err := db.ComparePeers(ctx, "SA")
	if err != nil {
		t.Fatalf("ComparePeers: %v", err)
	}
	if pc.Peers != len(software) {
		t.Fatalf("peer count = %d, want %d (a sector leak would give %d)", pc.Peers, len(software), len(rows))
	}
	for _, st := range pc.Stats {
		if st.Metric == "P/E" && (st.Median == nil || *st.Median < 12 || *st.Median > 13) {
			t.Errorf("P/E median = %v, want ~12.5 (software only)", st.Median)
		}
	}
	if bank, err := db.ComparePeers(ctx, "BA"); err != nil || bank.Peers != len(banks) {
		t.Fatalf("bank peers = %d, %v; want %d", bank.Peers, err, len(banks))
	}
}
