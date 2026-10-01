package research

import (
	"context"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
)

// series builds daily bars from closes, oldest first.
func series(closes []float64) []Bar {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]Bar, len(closes))
	for i, c := range closes {
		out[i] = Bar{
			Time: base.AddDate(0, 0, i),
			Open: c, High: c * 1.01, Low: c * 0.99, Close: c,
			Volume: 1_000_000,
		}
	}
	return out
}

func ret(t *testing.T, st *MarketStats, horizon string) Return {
	t.Helper()
	for _, r := range st.Returns {
		if r.Horizon == horizon {
			return r
		}
	}
	t.Fatalf("no %s return in %v", horizon, st.Returns)
	return Return{}
}

func TestMeasureReturns(t *testing.T) {
	// 300 bars rising 0.1% a day.
	closes := make([]float64, 300)
	p := 100.0
	for i := range closes {
		closes[i] = p
		p *= 1.001
	}
	st := Measure("TEST", series(closes))
	if st == nil {
		t.Fatal("expected a profile from 300 bars")
	}
	// 21 trading days of 0.1% compounding is about 2.12%.
	r := ret(t, st, "1m")
	if math.Abs(r.Percent-2.12) > 0.05 {
		t.Errorf("1m return = %.2f%%, want about 2.12%%", r.Percent)
	}
	// The reader must be able to check the arithmetic.
	if r.From <= 0 || r.FromDate.IsZero() {
		t.Error("a return must carry the close and date it was measured from")
	}
	if st.Bars != 300 {
		t.Errorf("Bars = %d, want the real sample size of 300", st.Bars)
	}
}

// TestMeasureRefusesThinHistory: a profile built from a handful of bars looks
// exactly like a real one, which makes it worse than none.
func TestMeasureRefusesThinHistory(t *testing.T) {
	if st := Measure("TEST", series([]float64{100, 101, 102})); st != nil {
		t.Errorf("expected no profile from 3 bars, got %+v", st)
	}
}

// TestHorizonsAreOmittedNotFaked: with 100 bars there is no year of history,
// and reporting the oldest available bar as a "1y" return would silently
// answer a different question.
func TestHorizonsAreOmittedNotFaked(t *testing.T) {
	closes := make([]float64, 100)
	for i := range closes {
		closes[i] = 100 + float64(i)
	}
	st := Measure("TEST", series(closes))
	if st == nil {
		t.Fatal("expected a profile from 100 bars")
	}
	for _, r := range st.Returns {
		if r.Horizon == "1y" {
			t.Errorf("reported a 1y return from %d bars", st.Bars)
		}
	}
	// The horizons it can honestly cover must still be there.
	if _, ok := hasHorizon(st, "3m"); !ok {
		t.Error("3m is covered by 100 bars and should be reported")
	}
}

func hasHorizon(st *MarketStats, h string) (Return, bool) {
	for _, r := range st.Returns {
		if r.Horizon == h {
			return r, true
		}
	}
	return Return{}, false
}

// TestMaxDrawdown: a return alone says nothing about what an investor would
// have had to sit through to earn it.
func TestMaxDrawdown(t *testing.T) {
	// Up to 200, down to 100, back to 150: a 50% drawdown, ending up 50%.
	var closes []float64
	for p := 100.0; p <= 200; p += 2 {
		closes = append(closes, p)
	}
	for p := 200.0; p >= 100; p -= 2 {
		closes = append(closes, p)
	}
	for p := 100.0; p <= 150; p += 2 {
		closes = append(closes, p)
	}
	st := Measure("TEST", series(closes))
	if st == nil {
		t.Fatal("expected a profile")
	}
	if math.Abs(st.MaxDrawdown-(-50)) > 1 {
		t.Errorf("MaxDrawdown = %.1f%%, want about -50%%", st.MaxDrawdown)
	}
	if st.MaxDrawdown >= 0 {
		t.Error("a drawdown must be negative or zero")
	}
}

// TestVolatilityIsAnnualised: a flat series has none, a jumpy one has a lot,
// and the units must be annualised percent rather than raw daily deviation.
func TestVolatilityIsAnnualised(t *testing.T) {
	flat := make([]float64, 300)
	for i := range flat {
		flat[i] = 100
	}
	if st := Measure("FLAT", series(flat)); st == nil || st.Volatility != 0 {
		t.Errorf("a flat series has zero volatility, got %v", st)
	}

	jumpy := make([]float64, 300)
	for i := range jumpy {
		jumpy[i] = 100
		if i%2 == 1 {
			jumpy[i] = 102
		}
	}
	st := Measure("JUMPY", series(jumpy))
	if st == nil {
		t.Fatal("expected a profile")
	}
	// ~2% daily swings annualise to a large number; the point is the scale.
	if st.Volatility < 20 {
		t.Errorf("annualised volatility = %.2f, want a clearly annualised figure", st.Volatility)
	}
}

func Test52WeekExtremes(t *testing.T) {
	closes := make([]float64, 300)
	for i := range closes {
		closes[i] = 100 + float64(i) // ends at its high
	}
	st := Measure("TEST", series(closes))
	if st == nil {
		t.Fatal("expected a profile")
	}
	if st.PctFrom52WHigh > 0 {
		t.Errorf("PctFrom52WHigh = %.2f, a close cannot be above its own high", st.PctFrom52WHigh)
	}
	if st.PctFrom52WLow <= 0 {
		t.Errorf("PctFrom52WLow = %.2f, a rising series must be above its low", st.PctFrom52WLow)
	}
}

// TestQuestionSubjectIsMeasuredFirst guards the defect where a question about
// one company priced six others: symbols were taken from the retrieved
// articles and truncated alphabetically, so "how has XOM performed" was
// answered with "the computed series covers AAPL, ABBV, ..." and no
// data on the company asked about.
// TestNamedSubjectIsTheOnlyOneMeasured: what the question names is measured;
// what its articles happen to mention is not. Measuring the mentions is how
// "how has XOM performed" once priced AAPL and ABBV ahead of XOM.
func TestNamedSubjectIsTheOnlyOneMeasured(t *testing.T) {
	findings := []Finding{
		{Relevance: 40, Symbols: []string{"AAPL", "XOM"}},
		{Relevance: 30, Symbols: []string{"AAPL"}},
		{Relevance: 20, Symbols: []string{"AAPL"}},
	}
	got := subjectsOf([]string{"XOM", "XOM"}, findings)
	if len(got) != 1 || got[0] != "XOM" {
		t.Fatalf("subjects = %v, want [XOM]", got)
	}
}

// TestNoSubjectForAGeneralQuestion: a question naming no company gets a
// company analysis only when its sources are plainly about one.
func TestNoSubjectForAGeneralQuestion(t *testing.T) {
	// The 401(k) question: one article each mentions Apple, AMD and ADAM.
	stray := []Finding{
		{Relevance: 90, Symbols: []string{"AAPL"}},
		{Relevance: 80, Symbols: []string{"AMD"}},
		{Relevance: 70, Symbols: []string{"ADAM"}},
		{Relevance: 60},
		{Relevance: 50},
	}
	if got := subjectsOf(nil, stray); len(got) != 0 {
		t.Fatalf("subjects = %v, want none", got)
	}
	// A question about "the chipmaker that just joined the trillion-dollar
	// club" names nothing, but its sources are about AMD.
	about := []Finding{
		{Relevance: 90, Symbols: []string{"AMD"}},
		{Relevance: 80, Symbols: []string{"AMD", "NVDA"}},
		{Relevance: 70, Symbols: []string{"AMD"}},
		{Relevance: 5, Symbols: []string{"NVDA"}},
	}
	if got := subjectsOf(nil, about); len(got) != 1 || got[0] != "AMD" {
		t.Fatalf("subjects = %v, want [AMD]", got)
	}
	if got := mentioned(about, nil); len(got) != 1 || got[0] != "AMD" {
		t.Fatalf("mentioned = %v, want [AMD]: NVDA is in one relevant source", got)
	}
}

func containsFold(haystack, needle string) bool {
	return len(haystack) >= len(needle) && strings.Contains(strings.ToUpper(haystack), needle)
}

// TestMeasurePreservesRank: the ranked order must survive the concurrent
// fetch. Sorting results alphabetically afterwards buried XOM beneath
// five companies the reader had not asked about.
func TestMeasurePreservesRank(t *testing.T) {
	e := &Engine{
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		prices: stubPrices{},
	}
	got := e.measure(context.Background(), []string{"XOM", "ABBV", "BLK"})
	if len(got) != 3 {
		t.Fatalf("measured %d instruments, want 3", len(got))
	}
	if got[0].Symbol != "XOM" {
		t.Errorf("order = %s, %s, %s; want the ranked order with XOM first",
			got[0].Symbol, got[1].Symbol, got[2].Symbol)
	}
}

type stubPrices struct{}

func (stubPrices) DailyBars(_ context.Context, _ string, _ int) ([]Bar, error) {
	closes := make([]float64, 300)
	for i := range closes {
		closes[i] = 100 + float64(i)
	}
	return series(closes), nil
}
