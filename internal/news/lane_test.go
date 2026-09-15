package news

import (
	"testing"
	"time"
)

func ist(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	return loc
}

func TestPhaseAt(t *testing.T) {
	loc := ist(t)
	cases := []struct {
		name string
		when time.Time
		want MarketPhase
	}{
		// A Tuesday.
		{"pre-open", time.Date(2026, 8, 25, 8, 0, 0, 0, loc), PhasePreOpen},
		{"just before open", time.Date(2026, 8, 25, 9, 14, 0, 0, loc), PhasePreOpen},
		{"open", time.Date(2026, 8, 25, 9, 15, 0, 0, loc), PhaseOpen},
		{"mid session", time.Date(2026, 8, 25, 12, 0, 0, 0, loc), PhaseOpen},
		{"last minute", time.Date(2026, 8, 25, 15, 29, 0, 0, loc), PhaseOpen},
		// Results land after the close, so this window is not a quiet one.
		{"post close", time.Date(2026, 8, 25, 15, 30, 0, 0, loc), PhasePostClose},
		{"evening filings", time.Date(2026, 8, 25, 19, 30, 0, 0, loc), PhasePostClose},
		{"overnight", time.Date(2026, 8, 25, 23, 0, 0, 0, loc), PhaseOvernight},
		{"early morning", time.Date(2026, 8, 25, 3, 0, 0, 0, loc), PhaseOvernight},
		// A Saturday and a Sunday.
		{"saturday", time.Date(2026, 8, 29, 12, 0, 0, 0, loc), PhaseWeekend},
		{"sunday", time.Date(2026, 8, 30, 12, 0, 0, 0, loc), PhaseWeekend},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PhaseAt(tc.when, loc); got != tc.want {
				t.Errorf("PhaseAt(%s) = %s, want %s", tc.when.Format(time.Kitchen), got, tc.want)
			}
		})
	}
}

// TestGlobalSourcesStayAwakeOvernight is the rule that matters most about
// phases: what happens in Washington at 02:00 IST is what moves the Indian
// open, so global coverage must not be throttled when India is asleep.
func TestGlobalSourcesStayAwakeOvernight(t *testing.T) {
	indian := Source{Country: "IN", Category: "markets", Refresh: 3 * time.Minute}
	global := Source{Country: "", Category: "markets", Refresh: 3 * time.Minute}

	indianNight := adaptiveInterval(indian, 0, PhaseOvernight)
	globalNight := adaptiveInterval(global, 0, PhaseOvernight)

	if globalNight >= indianNight {
		t.Errorf("global overnight interval %v should be shorter than Indian %v",
			globalNight, indianNight)
	}
	if globalNight != adaptiveInterval(global, 0, PhaseOpen) {
		t.Error("global cadence should not change with the Indian session")
	}
}

func TestLaneBoundsAreRespected(t *testing.T) {
	// A filings source configured absurdly slowly is still polled fast.
	filings := Source{Category: "filings", Country: "IN", Refresh: 6 * time.Hour, Usage: UsageOfficial}
	got := adaptiveInterval(filings, 0, PhaseOpen)
	if _, hi := LaneFast.Bounds(); got > hi {
		t.Errorf("filings interval = %v, want at most the fast ceiling %v", got, hi)
	}

	// A quarterly disclosure configured absurdly fast is still polled slowly.
	shareholding := Source{Category: "shareholding", Country: "IN", Refresh: time.Second, Usage: UsageOfficial}
	got = adaptiveInterval(shareholding, 0, PhaseOpen)
	if lo, _ := LaneSlow.Bounds(); got < lo {
		t.Errorf("shareholding interval = %v, want at least the slow floor %v", got, lo)
	}
}

// TestQuietFeedBacksOff covers the adaptation: a feed returning nothing is
// asked less often, but drifts rather than jumping to its ceiling.
func TestQuietFeedBacksOff(t *testing.T) {
	src := Source{Category: "markets", Country: "IN", Refresh: 3 * time.Minute}

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
