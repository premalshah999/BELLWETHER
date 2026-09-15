package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/congress"
)

// TestCongressFilingRoundTrip covers the two things most likely to break
// silently: the nullable earliest_transaction_date / disclosure_delay_days
// pair (a filing with no readable date must round-trip as zero/nil, not as
// some other sentinel), and the symbol-array containment query a listing
// page depends on to find "everything Congress traded in AAPL".
func TestCongressFilingRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	delay := 12
	withDate := congress.StoredFiling{
		Filing: congress.Filing{
			Last: "Alford", First: "Mark", StateDistrict: "MO04",
			FilingType: "P", FilingDate: date(2026, 3, 31), Year: 2026, DocID: "20034201",
		},
		Chamber:                 "house",
		Symbols:                 []string{"AAPL", "AMZN"},
		UnresolvedTickers:       []string{"ZZZZ"},
		EarliestTransactionDate: date(2026, 3, 16),
		DisclosureDelayDays:     &delay,
		DiscoveredAt:            time.Now().UTC(),
	}
	noDate := congress.StoredFiling{
		Filing: congress.Filing{
			Last: "Doe", First: "Jane", StateDistrict: "CA01",
			FilingType: "P", FilingDate: date(2026, 4, 1), Year: 2026, DocID: "99999999",
		},
		Chamber:      "house",
		Symbols:      []string{"AAPL"},
		DiscoveredAt: time.Now().UTC(),
	}

	for _, f := range []congress.StoredFiling{withDate, noDate} {
		if err := db.SaveCongressFiling(ctx, f); err != nil {
			t.Fatalf("save %s: %v", f.DocID, err)
		}
	}

	// Saving again must be a no-op, not an error or a duplicate row -- a
	// re-run of the sync job against an already-seen doc_id is the normal
	// case, not an edge case.
	if err := db.SaveCongressFiling(ctx, withDate); err != nil {
		t.Fatalf("re-save should be a silent no-op: %v", err)
	}

	got, err := db.ListCongressFilings(ctx, CongressFilingFilter{Symbol: "AAPL"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d filings for AAPL, want 2", len(got))
	}

	byID := map[string]congress.StoredFiling{}
	for _, f := range got {
		byID[f.DocID] = f
	}

	alford, ok := byID["20034201"]
	if !ok {
		t.Fatal("missing the Alford filing")
	}
	if alford.EarliestTransactionDate.Format("2006-01-02") != "2026-03-16" {
		t.Errorf("EarliestTransactionDate = %v, want 2026-03-16", alford.EarliestTransactionDate)
	}
	if alford.DisclosureDelayDays == nil || *alford.DisclosureDelayDays != 12 {
		t.Errorf("DisclosureDelayDays = %v, want 12", alford.DisclosureDelayDays)
	}
	if len(alford.UnresolvedTickers) != 1 || alford.UnresolvedTickers[0] != "ZZZZ" {
		t.Errorf("UnresolvedTickers = %v, want [ZZZZ]", alford.UnresolvedTickers)
	}
	if alford.DocURL() != "https://disclosures-clerk.house.gov/public_disc/ptr-pdfs/2026/20034201.pdf" {
		t.Errorf("DocURL = %q", alford.DocURL())
	}

	doe, ok := byID["99999999"]
	if !ok {
		t.Fatal("missing the Doe filing")
	}
	if !doe.EarliestTransactionDate.IsZero() {
		t.Errorf("EarliestTransactionDate = %v, want zero (no date was extracted)", doe.EarliestTransactionDate)
	}
	if doe.DisclosureDelayDays != nil {
		t.Errorf("DisclosureDelayDays = %v, want nil (no date was extracted)", *doe.DisclosureDelayDays)
	}

	// A symbol nothing names must come back empty, not every filing.
	none, err := db.ListCongressFilings(ctx, CongressFilingFilter{Symbol: "NOPE"})
	if err != nil {
		t.Fatalf("list NOPE: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("got %d filings for a symbol nothing names, want 0", len(none))
	}

	ids, err := db.ExistingCongressFilingDocIDs(ctx, 2026)
	if err != nil {
		t.Fatalf("existing doc ids: %v", err)
	}
	if !ids["20034201"] || !ids["99999999"] {
		t.Errorf("existing doc ids = %v, want both seeded ids present", ids)
	}
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
