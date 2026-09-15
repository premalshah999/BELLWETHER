package postgres

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/scanner"
)

// TestScanRoundTrip exercises the scan write and read paths against a real
// PostgreSQL, because the interesting failures here are all in SQL: a TEXT[]
// that does not scan back, a numeric that loses precision, a NULL that
// becomes a false. None of those are visible to a mock.
func boolPtr(b bool) *bool { return &b }

func TestScanRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, dsn, WithGoMigration(goMigrationNameForTests, noopGoMigration))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Closed via Cleanup rather than defer, because defers run before
	// cleanups: with a deferred Close the row-deleting cleanup below runs
	// against a closed pool and silently leaves its rows behind. Cleanups
	// run last-registered-first, so registering Close here puts it after
	// the delete.
	t.Cleanup(func() { _ = db.Close() })

	// Cleared up front rather than only on the way out: a test that depends
	// on its own teardown having run is a test that fails for the wrong
	// reason the first time teardown is skipped.
	if _, err := db.db.ExecContext(ctx,
		`DELETE FROM scans WHERE id IN (SELECT scan_id FROM scan_findings WHERE symbol LIKE 'ZZTEST%')`); err != nil {
		t.Fatalf("clear previous test rows: %v", err)
	}

	asOf := time.Now().UTC().Truncate(time.Second)
	res := scanner.Result{
		AsOf: asOf, Universe: 750, Scanned: 748, Failed: 2, Elapsed: "96.0s",
		Findings: []scanner.Finding{
			{
				Metrics: scanner.Metrics{
					Symbol: "ZZTESTA", Close: 431.25, Return1D: 8.44, Return5D: 11.2,
					ReturnZ: 8.76, Volume: 9_120_004, VolumeRatio: 11.88, VolumeZ: 5.31,
					GapPercent: 2.1, PctFrom52WHigh: -0.4, PctFrom52WLow: 61.3, Bars: 125,
				},
				Signals: []scanner.Signal{scanner.SignalVolumeSpike, scanner.SignalPriceMove, scanner.SignalNear52WHigh},
				Score:   17.78,
			},
			{
				Metrics:   scanner.Metrics{Symbol: "ZZTESTB", Close: 88.1, VolumeZ: 3.43, ReturnZ: 0.11, VolumeRatio: 5.99},
				Signals:   []scanner.Signal{scanner.SignalSilentVolume},
				Score:     6.83,
				Explained: boolPtr(false),
			},
		},
	}

	scanID, err := db.SaveScan(ctx, res)
	if err != nil {
		t.Fatalf("save scan: %v", err)
	}
	if scanID == 0 {
		t.Fatal("save scan returned no id")
	}
	t.Cleanup(func() {
		if _, err := db.db.ExecContext(ctx, `DELETE FROM scans WHERE id = $1`, scanID); err != nil {
			t.Errorf("cleanup left rows behind: %v", err)
		}
	})

	got, err := db.LatestScan(ctx, 10)
	if err != nil {
		t.Fatalf("latest scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("latest scan returned %d findings, want 2", len(got))
	}

	first := got[0]
	if first.Symbol != "ZZTESTA" {
		t.Errorf("first finding = %s, want ZZTESTA (highest score first)", first.Symbol)
	}
	// The float columns must come back exactly, not merely close: these are
	// the numbers a ranking is built on.
	if first.ReturnZ != 8.76 || first.VolumeZ != 5.31 || first.Close != 431.25 {
		t.Errorf("floats did not round-trip: ret_z=%v vol_z=%v close=%v",
			first.ReturnZ, first.VolumeZ, first.Close)
	}
	if first.Volume != 9_120_004 {
		t.Errorf("volume = %v, want 9120004", first.Volume)
	}
	if len(first.Signals) != 3 {
		t.Fatalf("signals = %v, want 3", first.Signals)
	}
	if first.Signals[0] != scanner.SignalVolumeSpike || first.Signals[2] != scanner.SignalNear52WHigh {
		t.Errorf("signals came back in the wrong order or shape: %v", first.Signals)
	}

	// Unchecked must stay distinguishable from checked-and-found-nothing.
	// This is the pair that regressed once already: the runner resolved
	// explained, used it to set attention, and never wrote it, so every row
	// in the table read NULL and the distinction the column exists for was
	// lost.
	if first.Explained != nil {
		t.Errorf("explained = %v, want nil when the finding carried no verdict", *first.Explained)
	}
	second := got[1]
	if second.Explained == nil {
		t.Fatal("explained is nil for a finding that carried a false verdict; the write path dropped it")
	}
	if *second.Explained {
		t.Error("explained = true, want the false that was written")
	}
	if second.ExplainedAt == nil || second.ExplainedAt.IsZero() {
		t.Error("explained_at was not stamped alongside a written verdict")
	}
	if err := db.MarkExplained(ctx, first.ID, false); err != nil {
		t.Fatalf("mark explained: %v", err)
	}
	got, err = db.LatestScan(ctx, 10)
	if err != nil {
		t.Fatalf("latest scan after mark: %v", err)
	}
	if got[0].Explained == nil {
		t.Fatal("explained is still nil after being marked")
	}
	if *got[0].Explained {
		t.Error("explained = true, want false: we looked and found nothing")
	}
	if got[0].ExplainedAt == nil || got[0].ExplainedAt.IsZero() {
		t.Error("explained_at was not set")
	}

	hist, err := db.SymbolScanHistory(ctx, "ZZTESTA", 10)
	if err != nil {
		t.Fatalf("symbol history: %v", err)
	}
	if len(hist) != 1 || hist[0].Symbol != "ZZTESTA" {
		t.Fatalf("symbol history = %v, want one ZZTESTA row", hist)
	}
	if !hist[0].AsOf.Equal(asOf) {
		t.Errorf("as_of = %v, want %v — the market time must survive the round trip", hist[0].AsOf, asOf)
	}

	// A symbol nothing is known about must be reported unexplained rather
	// than erroring, since that is the common case.
	explained, err := db.ExplainsMove(ctx, "ZZTESTA", time.Now().Add(-36*time.Hour))
	if err != nil {
		t.Fatalf("explains move: %v", err)
	}
	if explained {
		t.Error("a symbol with no events must not be reported as explained")
	}
}

// TestEventEvidenceRoundTrip covers the query that broke every event detail
// view: the qualified column list was derived by replacing "id," with "ri.id,"
// in the unqualified one, which also rewrote the "id," inside "source_id," and
// produced a reference to a table called source_ri. Nothing else reads
// evidence, so it failed only on that one endpoint and only at runtime.
func TestEventEvidenceRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, dsn, WithGoMigration(goMigrationNameForTests, noopGoMigration))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var id int64
	err = db.db.QueryRowContext(ctx,
		`SELECT event_id FROM event_evidence ORDER BY event_id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		t.Skip("no evidence rows to read")
	}

	ev, err := db.GetEvent(ctx, id)
	if err != nil {
		t.Fatalf("GetEvent(%d): %v", id, err)
	}
	if ev.ID != id {
		t.Errorf("GetEvent returned id %d, want %d", ev.ID, id)
	}
	if len(ev.Evidence) == 0 {
		t.Error("an event selected by having evidence came back with none")
	}
	for _, e := range ev.Evidence {
		if e.SourceID == "" {
			t.Error("evidence came back with an empty source id; the column list is misaligned")
		}
	}
}

// TestPeerPercentileScale checks the rank arithmetic that every valuation
// judgement rests on: the cheapest company in a group must read as 0 and the
// most expensive as 100, with the company itself excluded from the divisor.
func TestPeerPercentileScale(t *testing.T) {
	cases := []struct {
		name      string
		below     int
		reporting int
		want      float64
	}{
		{"lowest of seventeen", 0, 17, 0},
		{"highest of seventeen", 16, 17, 100},
		{"exact middle of nine", 4, 9, 50},
		{"twelve of seventeen below", 12, 17, 75},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := float64(tc.below) / float64(tc.reporting-1) * 100
			if math.Abs(got-tc.want) > 0.01 {
				t.Errorf("percentile = %.2f, want %.2f", got, tc.want)
			}
		})
	}
}
