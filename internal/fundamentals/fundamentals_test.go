package fundamentals

import (
	"math"
	"testing"
)

func f(v float64) *float64 { return &v }

// TestMarginRefusesToInventZero: a company with no reported revenue has no
// margin. Returning zero would place it alongside a company that genuinely
// broke even, which is a different fact entirely.
func TestMarginRefusesToInventZero(t *testing.T) {
	if m := Margin(f(100), nil); m != nil {
		t.Errorf("margin with unknown revenue = %v, want nil", *m)
	}
	if m := Margin(nil, f(1000)); m != nil {
		t.Errorf("margin with unknown numerator = %v, want nil", *m)
	}
	if m := Margin(f(100), f(0)); m != nil {
		t.Errorf("margin on zero revenue = %v, want nil", *m)
	}
	m := Margin(f(120), f(1000))
	if m == nil || math.Abs(*m-12) > 1e-9 {
		t.Errorf("margin = %v, want 12", m)
	}
}

// TestTrendNeedsEnoughPoints guards the opposite failure from the one that
// shipped. Requiring every period to report a metric produced no trends at
// all; accepting two points would call one weak quarter a collapse.
func TestTrendNeedsEnoughPoints(t *testing.T) {
	if tr := BuildTrend("Revenue", []float64{100, 90}, true); tr != nil {
		t.Errorf("two points produced a trend: %+v", tr)
	}
	if tr := BuildTrend("Revenue", []float64{100, 105, 110, 118}, true); tr == nil {
		t.Error("four points should produce a trend")
	}
}

// TestDebtRisingIsNotAnImprovement: "improving" must mean good for an owner,
// not "went up". A screen showing "Total debt · improving +33.2%" inverts the
// one word a reader is most likely to act on.
func TestDebtRisingIsNotAnImprovement(t *testing.T) {
	rising := []float64{100, 112, 124, 133}
	if tr := BuildTrend("Total debt", rising, false); tr == nil || tr.Direction != "deteriorating" {
		t.Errorf("rising debt = %v, want deteriorating", tr)
	}
	falling := []float64{133, 124, 112, 100}
	tr := BuildTrend("Total debt", falling, false)
	if tr == nil || tr.Direction != "improving" {
		t.Errorf("falling debt = %v, want improving", tr)
	}
	if tr.Rising {
		t.Error("Rising must report what the number did, separately from whether it is good")
	}
	// Revenue keeps the ordinary reading.
	if tr := BuildTrend("Revenue", rising, true); tr == nil || tr.Direction != "improving" {
		t.Errorf("rising revenue = %v, want improving", tr)
	}
}

// TestTrendDirection: ordinary quarter-to-quarter wobble is not a finding.
func TestTrendDirection(t *testing.T) {
	cases := []struct {
		name   string
		points []float64
		want   string
	}{
		{"clear growth", []float64{100, 108, 116, 124}, "improving"},
		{"clear decline", []float64{124, 116, 108, 100}, "deteriorating"},
		{"noise within the band", []float64{100, 101, 99, 102}, "flat"},
		{"margin drift that is not a story", []float64{11.6, 11.9, 11.9, 11.8}, "flat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := BuildTrend("m", tc.points, true)
			if tr == nil {
				t.Fatal("expected a trend")
			}
			if tr.Direction != tc.want {
				got := 0.0
				if tr.ChangePercent != nil {
					got = *tr.ChangePercent
				}
				t.Errorf("direction = %s (%.1f%%), want %s", tr.Direction, got, tc.want)
			}
		})
	}
}

// TestSnapshotRatiosArePointers is the invariant behind every screen built on
// this data: a loss-making company must not sort to the top of "cheapest by
// P/E" because its missing ratio was stored as zero.
func TestSnapshotRatiosArePointers(t *testing.T) {
	var s Snapshot
	if s.PETrailing != nil || s.ReturnOnEquity != nil || s.MarketCap != nil {
		t.Error("a zero-value Snapshot must report every ratio as absent, not as zero")
	}
}

// TestSanitiseRejectsImpossibleRatios guards against shipping numbers the
// provider got wrong. Measured on real data, its EV/EBITDA for Indian
// listings ranged from -141 to 1257 against a median of 17.8 — for Infosys it
// reported EBITDA about a hundredth of the real figure, giving a multiple of
// 1011. A reader shown that discounts every other number on the screen too.
func TestSanitiseRejectsImpossibleRatios(t *testing.T) {
	s := &Snapshot{
		EVToEBITDA:     f(1011.481), // the real Infosys reading
		PETrailing:     f(-12),      // a loss-making company has no P/E
		PriceToBook:    f(4.95),     // plausible, must survive
		ReturnOnEquity: f(0.32),     // plausible, must survive
		DividendYield:  f(4.37),
		ProfitMargin:   f(0.164),
	}
	sanitise(s)

	if s.EVToEBITDA != nil {
		t.Errorf("EV/EBITDA of 1011 was kept as %v", *s.EVToEBITDA)
	}
	if s.PETrailing != nil {
		t.Errorf("negative P/E was kept as %v", *s.PETrailing)
	}
	if s.PriceToBook == nil || *s.PriceToBook != 4.95 {
		t.Error("a plausible P/B was discarded")
	}
	if s.ReturnOnEquity == nil || *s.ReturnOnEquity != 0.32 {
		t.Error("a plausible ROE was discarded")
	}
	if s.DividendYield == nil || *s.DividendYield != 4.37 {
		t.Error("a plausible dividend yield was discarded")
	}
	if s.ProfitMargin == nil {
		t.Error("a plausible margin was discarded")
	}
}

// TestSanitiseLeavesAbsentAbsent: rejecting a bad value must not invent one,
// and an absent ratio must stay absent rather than becoming zero.
func TestSanitiseLeavesAbsentAbsent(t *testing.T) {
	var s Snapshot
	sanitise(&s)
	if s.PETrailing != nil || s.EVToEBITDA != nil || s.ReturnOnEquity != nil {
		t.Error("sanitise invented values for absent ratios")
	}
}

// TestMixedCurrencyDetected guards the fault that made Infosys look like it
// generated almost no cash. Its statements are filed in US dollars and its
// shares trade in rupees, so free cash flow of $3.73bn over a market
// capitalisation of ₹4.53 lakh crore produced a yield of 0.1% where the real
// figure is around 5%. Nothing about that number looked wrong.
func TestMixedCurrencyDetected(t *testing.T) {
	infy := Snapshot{QuoteCurrency: "INR", FinancialCurrency: "USD"}
	if !infy.MixedCurrency() {
		t.Error("USD statements against INR quotes must be reported as mixed")
	}

	reliance := Snapshot{QuoteCurrency: "INR", FinancialCurrency: "INR"}
	if reliance.MixedCurrency() {
		t.Error("matching currencies must not be reported as mixed")
	}

	// An unknown currency is not evidence of a mismatch. Refusing every ratio
	// wherever the provider omitted the field would discard most of the data
	// over something that is usually present and usually matches.
	for _, s := range []Snapshot{
		{QuoteCurrency: "INR"},
		{FinancialCurrency: "INR"},
		{},
	} {
		if s.MixedCurrency() {
			t.Errorf("unknown currency treated as a mismatch: %+v", s)
		}
	}
}

// TestMixedCurrencyDropsOnlyTheAffectedRatios: the currency mismatch corrupts
// the enterprise-value multiples, which mix a rupee market value with a dollar
// statement figure. It does not touch P/E or P/B, whose terms are both in the
// quote currency, nor margins, whose terms both come from the same filing.
// Dropping more than necessary would throw away most of what is known about
// the company.
func TestMixedCurrencyDropsOnlyTheAffectedRatios(t *testing.T) {
	s := &Snapshot{
		QuoteCurrency: "INR", FinancialCurrency: "USD",
		EVToEBITDA:      f(11.9),
		EVToRevenue:     f(4.2),
		PETrailing:      f(14.44),
		PriceToBook:     f(4.95),
		ReturnOnEquity:  f(0.32),
		OperatingMargin: f(0.212),
		DividendYield:   f(4.37),
	}
	dropMixedCurrencyRatios(s)

	if s.EVToEBITDA != nil || s.EVToRevenue != nil {
		t.Error("enterprise-value multiples must be dropped when currencies differ")
	}
	for name, v := range map[string]*float64{
		"P/E": s.PETrailing, "P/B": s.PriceToBook,
		"ROE": s.ReturnOnEquity, "operating margin": s.OperatingMargin,
		"dividend yield": s.DividendYield,
	} {
		if v == nil {
			t.Errorf("%s was dropped, but both its terms are in one currency", name)
		}
	}

	// A matching-currency company keeps everything.
	ok := &Snapshot{QuoteCurrency: "INR", FinancialCurrency: "INR", EVToEBITDA: f(11.9)}
	dropMixedCurrencyRatios(ok)
	if ok.EVToEBITDA == nil {
		t.Error("dropped a valid ratio for a single-currency company")
	}
}
