package news

import (
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func TestPhaseAt(t *testing.T) {
	et := func(day, h, m int) time.Time { return time.Date(2026, 8, day, h, m, 0, 0, marketdata.Market) }
	for name, tc := range map[string]struct {
		when time.Time
		want MarketPhase
	}{
		// Tuesday the 25th.
		"pre-market":       {et(25, 8, 0), PhasePreOpen},
		"just before open": {et(25, 9, 29), PhasePreOpen},
		"open":             {et(25, 9, 30), PhaseOpen},
		"last minute":      {et(25, 15, 59), PhaseOpen},
		"after hours":      {et(25, 16, 0), PhasePostClose},
		"earnings evening": {et(25, 19, 30), PhasePostClose},
		"overnight":        {et(25, 23, 0), PhaseOvernight},
		"early morning":    {et(25, 3, 0), PhaseOvernight},
		"saturday":         {et(29, 12, 0), PhaseWeekend},
		"sunday":           {et(30, 12, 0), PhaseWeekend},
	} {
		if got := PhaseAt(tc.when); got != tc.want {
			t.Errorf("%s: PhaseAt = %s, want %s", name, got, tc.want)
		}
	}
}

// Coverage runs around the clock: what happens overnight sets the open.
func TestSourcesStayAwakeOvernight(t *testing.T) {
	src := Source{Country: "US", Category: "markets", Refresh: 3 * time.Minute}
	if adaptiveInterval(src, 0, PhaseOvernight) != adaptiveInterval(src, 0, PhaseOpen) {
		t.Error("overnight cadence should match the session")
	}
	if adaptiveInterval(src, 0, PhaseWeekend) <= adaptiveInterval(src, 0, PhaseOpen) {
		t.Error("the weekend should ease off")
	}
}

func TestLaneBoundsAreRespected(t *testing.T) {
	// A filings source configured absurdly slowly is still polled fast.
	filings := Source{Category: "filings", Country: "US", Refresh: 6 * time.Hour, Usage: UsageOfficial}
	got := adaptiveInterval(filings, 0, PhaseOpen)
	if _, hi := LaneFast.Bounds(); got > hi {
		t.Errorf("filings interval = %v, want at most the fast ceiling %v", got, hi)
	}

	// A quarterly disclosure configured absurdly fast is still polled slowly.
	shareholding := Source{Category: "shareholding", Country: "US", Refresh: time.Second, Usage: UsageOfficial}
	got = adaptiveInterval(shareholding, 0, PhaseOpen)
	if lo, _ := LaneSlow.Bounds(); got < lo {
		t.Errorf("shareholding interval = %v, want at least the slow floor %v", got, lo)
	}
}

// TestQuietFeedBacksOff covers the adaptation: a feed returning nothing is
// asked less often, but drifts rather than jumping to its ceiling.
func TestQuietFeedBacksOff(t *testing.T) {
	src := Source{Category: "markets", Country: "US", Refresh: 3 * time.Minute}

	prev := adaptiveInterval(src, 0, PhaseOpen)
	for empty := 1; empty <= 6; empty++ {
		got := adaptiveInterval(src, empty, PhaseOpen)
		if got < prev {
			t.Errorf("interval shrank with %d empty polls: %v then %v", empty, prev, got)
		}
		prev = got
	}
	// And it is bounded.
	_, hi := LaneNormal.Bounds()
	if got := adaptiveInterval(src, 500, PhaseOpen); got > hi {
		t.Errorf("interval %v exceeded the lane ceiling %v", got, hi)
	}
}

func TestLaneClassification(t *testing.T) {
	cases := []struct {
		src  Source
		want Lane
	}{
		{Source{Category: "filings", Usage: UsageOfficial}, LaneFast},
		{Source{Category: "regulatory", Usage: UsageOfficial}, LaneFast},
		{Source{Category: "shareholding", Usage: UsageOfficial}, LaneSlow},
		{Source{Category: "annual_reports", Usage: UsageOfficial}, LaneSlow},
		{Source{Category: "markets", Usage: UsageLegalReview}, LaneNormal},
		{Source{Category: "discovery", Usage: UsageDiscoveryOnly}, LaneNormal},
	}
	for _, tc := range cases {
		if got := tc.src.Lane(); got != tc.want {
			t.Errorf("Lane(%s) = %s, want %s", tc.src.Category, got, tc.want)
		}
	}
}

// TestCatalogLaneDistribution checks the real catalog lands sensibly, which is
// the thing that actually determines the polling budget.
func TestCatalogLaneDistribution(t *testing.T) {
	reg, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[Lane]int{}
	for _, s := range reg.Fetchable() {
		counts[s.Lane()]++
	}
	t.Logf("fast=%d normal=%d slow=%d", counts[LaneFast], counts[LaneNormal], counts[LaneSlow])

	if counts[LaneFast] == 0 {
		t.Error("no sources in the fast lane; exchange filings would arrive late")
	}
	// The fast lane is expensive. If most of the catalog is in it, the lane
	// system is not doing its job.
	if counts[LaneFast] > len(reg.Fetchable())/2 {
		t.Errorf("fast lane holds %d of %d sources, which defeats the point",
			counts[LaneFast], len(reg.Fetchable()))
	}
}

func TestHeatRisesAndDecays(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tr := NewTracker(clock)

	if got := tr.SymbolHeat("SUZLON"); got != HeatNormal {
		t.Fatalf("initial heat = %s, want normal", got)
	}

	// A high-importance official filing should make it hot immediately.
	tr.Observe([]string{"SUZLON"}, nil, 9, true, "order win")
	tr.Observe([]string{"SUZLON"}, nil, 9, true, "order win")
	tr.Observe([]string{"SUZLON"}, nil, 9, true, "order win")
	tr.Observe([]string{"SUZLON"}, nil, 9, true, "order win")
	if got := tr.SymbolHeat("SUZLON"); got != HeatHot {
		t.Errorf("heat after four material filings = %s, want hot", got)
	}

	// And it must cool without anyone resetting it.
	now = now.Add(4 * heatHalfLife)
	if got := tr.SymbolHeat("SUZLON"); got == HeatHot {
		t.Errorf("still hot after four half-lives; heat is not decaying")
	}
	now = now.Add(20 * heatHalfLife)
	if got := tr.SymbolHeat("SUZLON"); got != HeatNormal {
		t.Errorf("heat = %s long after the event, want normal", got)
	}
}

// TestRoutineDisclosureBarelyRegisters is the other half: if everything makes
// a company hot, nothing does.
func TestRoutineDisclosureBarelyRegisters(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(func() time.Time { return now })

	// Newspaper publication notices, importance 2.
	for i := 0; i < 5; i++ {
		tr.Observe([]string{"WIPRO"}, nil, 2, true, "newspaper publication")
	}
	if got := tr.SymbolHeat("WIPRO"); got == HeatHot {
		t.Error("routine disclosures should not make a company hot")
	}
}

func TestSectorHeatRisesSlowerThanCompanyHeat(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(func() time.Time { return now })

	// Observed enough times that both the company and its sector clear the
	// reporting threshold; below it both read as zero and the comparison
	// would be vacuous.
	for i := 0; i < 5; i++ {
		tr.Observe([]string{"TATASTEEL"}, []string{"Metals & Mining"}, 8, true, "acquisition")
	}

	symbols := tr.HotSymbols(10)
	sectors := tr.HotSectors(10)
	symScore, secScore := 0.0, 0.0
	for _, e := range symbols {
		if e.Key == "TATASTEEL" {
			symScore = e.Score
		}
	}
	for _, e := range sectors {
		if e.Key == "Metals & Mining" {
			secScore = e.Score
		}
	}
	if secScore >= symScore {
		t.Errorf("sector score %.2f should be below company score %.2f: one company's news is weak evidence about its industry",
			secScore, symScore)
	}
}

func TestSweepDropsColdEntries(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(func() time.Time { return now })

	tr.Observe([]string{"AAA", "BBB"}, nil, 8, true, "x")
	if removed := tr.Sweep(); removed != 0 {
		t.Errorf("swept %d live entries", removed)
	}
	now = now.Add(40 * heatHalfLife)
	if removed := tr.Sweep(); removed != 2 {
		t.Errorf("swept %d entries, want 2", removed)
	}
}

func TestHeatMultiplierIsBounded(t *testing.T) {
	// Attention may compress a cadence, never below a third: politeness to
	// publishers is what keeps these feeds available at all.
	if m := HeatHot.Multiplier(); m < 1.0/3.0-0.001 {
		t.Errorf("hot multiplier %v is too aggressive", m)
	}
	if m := HeatNormal.Multiplier(); m != 1 {
		t.Errorf("normal multiplier = %v, want 1", m)
	}
}
