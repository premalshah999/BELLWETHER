package ai

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func TestNormaliseScenarios(t *testing.T) {
	tests := []struct {
		name    string
		outlook Outlook
		wantErr bool
		wantSum float64
	}{
		{
			name: "already sums to one",
			outlook: Outlook{
				Base: Scenario{Probability: 0.5, MovePercent: 0},
				Bull: Scenario{Probability: 0.3, MovePercent: 6},
				Bear: Scenario{Probability: 0.2, MovePercent: -6},
			},
			wantSum: 1.0,
		},
		{
			name: "slightly off is left alone",
			outlook: Outlook{
				Base: Scenario{Probability: 0.5, MovePercent: 0},
				Bull: Scenario{Probability: 0.3, MovePercent: 6},
				Bear: Scenario{Probability: 0.21, MovePercent: -6},
			},
			wantSum: 1.01,
		},
		{
			// Models are unreliable at making three numbers sum to one.
			// Renormalising salvages an otherwise useful answer.
			name: "badly off is renormalised",
			outlook: Outlook{
				Base: Scenario{Probability: 60, MovePercent: 0},
				Bull: Scenario{Probability: 25, MovePercent: 6},
				Bear: Scenario{Probability: 15, MovePercent: -6},
			},
			wantSum: 1.0,
		},
		{
			name: "negative probability is rejected",
			outlook: Outlook{
				Base: Scenario{Probability: 1.2, MovePercent: 0},
				Bull: Scenario{Probability: -0.2, MovePercent: 6},
				Bear: Scenario{Probability: 0.0, MovePercent: -6},
			},
			wantErr: true,
		},
		{
			// Incoherent ordering means the model misunderstood the task, and
			// scoring such a forecast would pollute the calibration record.
			name: "bull below bear is rejected",
			outlook: Outlook{
				Base: Scenario{Probability: 0.5, MovePercent: 0},
				Bull: Scenario{Probability: 0.3, MovePercent: -6},
				Bear: Scenario{Probability: 0.2, MovePercent: 6},
			},
			wantErr: true,
		},
		{
			name: "all zero is rejected",
			outlook: Outlook{
				Base: Scenario{MovePercent: 0}, Bull: Scenario{MovePercent: 6}, Bear: Scenario{MovePercent: -6},
			},
			wantErr: true,
		},
		{
			name: "NaN is rejected",
			outlook: Outlook{
				Base: Scenario{Probability: math.NaN(), MovePercent: 0},
				Bull: Scenario{Probability: 0.3, MovePercent: 6},
				Bear: Scenario{Probability: 0.2, MovePercent: -6},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.outlook
			err := normaliseScenarios(&o)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			sum := o.Base.Probability + o.Bull.Probability + o.Bear.Probability
			if math.Abs(sum-tc.wantSum) > 0.001 {
				t.Errorf("probabilities sum to %v, want %v", sum, tc.wantSum)
			}
		})
	}
}

func TestClassifyOutcome(t *testing.T) {
	// Base 0%, bull +8%, bear -8%. The boundaries sit halfway: +4% and -4%.
	o := Outlook{
		Base: Scenario{MovePercent: 0},
		Bull: Scenario{MovePercent: 8},
		Bear: Scenario{MovePercent: -8},
	}
	tests := []struct {
		realised float64
		want     string
	}{
		{realised: 10, want: ScenarioBull},
		{realised: 4, want: ScenarioBull}, // exactly on the boundary
		{realised: 3.9, want: ScenarioBase},
		{realised: 0, want: ScenarioBase},
		{realised: -3.9, want: ScenarioBase},
		{realised: -4, want: ScenarioBear}, // exactly on the boundary
		{realised: -12, want: ScenarioBear},
	}
	for _, tc := range tests {
		if got := classifyOutcome(o, tc.realised); got != tc.want {
			t.Errorf("classifyOutcome(%v%%) = %q, want %q", tc.realised, got, tc.want)
		}
	}
}

func TestClassifyOutcomeIsExhaustive(t *testing.T) {
	// Every possible move must land in exactly one bucket, or the Brier score
	// is not a proper scoring rule.
	o := Outlook{
		Base: Scenario{MovePercent: 2},
		Bull: Scenario{MovePercent: 12},
		Bear: Scenario{MovePercent: -9},
	}
	for move := -50.0; move <= 50.0; move += 0.25 {
		got := classifyOutcome(o, move)
		switch got {
		case ScenarioBase, ScenarioBull, ScenarioBear:
		default:
			t.Fatalf("move %v classified as %q", move, got)
		}
	}
}

func TestBrierScore(t *testing.T) {
	tests := []struct {
		name    string
		outlook Outlook
		actual  string
		want    float64
	}{
		{
			name: "perfect confident forecast",
			outlook: Outlook{
				Base: Scenario{Probability: 1}, Bull: Scenario{}, Bear: Scenario{},
			},
			actual: ScenarioBase,
			want:   0,
		},
		{
			name: "confidently wrong is the worst possible",
			outlook: Outlook{
				Base: Scenario{Probability: 1}, Bull: Scenario{}, Bear: Scenario{},
			},
			actual: ScenarioBull,
			want:   2,
		},
		{
			name: "uniform guess",
			outlook: Outlook{
				Base: Scenario{Probability: 1.0 / 3}, Bull: Scenario{Probability: 1.0 / 3}, Bear: Scenario{Probability: 1.0 / 3},
			},
			actual: ScenarioBase,
			want:   uniformBrier,
		},
		{
			name: "moderately confident and right",
			outlook: Outlook{
				Base: Scenario{Probability: 0.6}, Bull: Scenario{Probability: 0.25}, Bear: Scenario{Probability: 0.15},
			},
			actual: ScenarioBase,
			// (0.6-1)^2 + 0.25^2 + 0.15^2 = 0.16 + 0.0625 + 0.0225
			want: 0.245,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := brierScore(tc.outlook, tc.actual); math.Abs(got-tc.want) > 0.0001 {
				t.Errorf("brierScore = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBrierRewardsCalibration(t *testing.T) {
	// A confident forecast that is right beats a hedged one; a confident
	// forecast that is wrong loses to it. That asymmetry is what discourages
	// the model from always claiming high confidence.
	confident := Outlook{
		Base: Scenario{Probability: 0.9}, Bull: Scenario{Probability: 0.05}, Bear: Scenario{Probability: 0.05},
	}
	hedged := Outlook{
		Base: Scenario{Probability: 0.4}, Bull: Scenario{Probability: 0.3}, Bear: Scenario{Probability: 0.3},
	}

	if brierScore(confident, ScenarioBase) >= brierScore(hedged, ScenarioBase) {
		t.Error("being confident and right should score better than hedging")
	}
	if brierScore(confident, ScenarioBull) <= brierScore(hedged, ScenarioBull) {
		t.Error("being confident and wrong should score worse than hedging")
	}
}

func TestRealisedVolatility(t *testing.T) {
	t.Run("a flat series has no volatility", func(t *testing.T) {
		closes := make([]float64, 40)
		for i := range closes {
			closes[i] = 100
		}
		if got := realisedVolatility(closes, 20); got != 0 {
			t.Errorf("volatility = %v, want 0", got)
		}
	})

	t.Run("a noisier series is more volatile", func(t *testing.T) {
		calm := make([]float64, 40)
		wild := make([]float64, 40)
		for i := range calm {
			calm[i] = 100 + float64(i%2)*0.1
			wild[i] = 100 + float64(i%2)*10
		}
		if realisedVolatility(wild, 20) <= realisedVolatility(calm, 20) {
			t.Error("the wilder series should measure as more volatile")
		}
	})

	t.Run("degenerate input", func(t *testing.T) {
		if got := realisedVolatility([]float64{100}, 20); got != 0 {
			t.Errorf("volatility of a single point = %v, want 0", got)
		}
		if got := realisedVolatility(nil, 20); got != 0 {
			t.Errorf("volatility of nothing = %v, want 0", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Calibration
// ---------------------------------------------------------------------------

type memOutputStore struct {
	mu       sync.Mutex
	outputs  []*Output
	outlooks []Outlook
	nextID   int64
}

func (m *memOutputStore) SaveOutput(_ context.Context, out *Output) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	out.ID = m.nextID
	m.outputs = append(m.outputs, out)
	return out.ID, nil
}

func (m *memOutputStore) LatestOutput(context.Context, string, string) (Output, bool, error) {
	return Output{}, false, nil
}
func (m *memOutputStore) ListOutputs(context.Context, string, int) ([]Output, error) {
	return nil, nil
}

func (m *memOutputStore) SaveOutlook(_ context.Context, o *Outlook) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	o.ID = m.nextID
	m.outlooks = append(m.outlooks, *o)
	return o.ID, nil
}

func (m *memOutputStore) ListOutlooks(_ context.Context, symbol string, _ int) ([]Outlook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Outlook, 0, len(m.outlooks))
	for _, o := range m.outlooks {
		if symbol == "" || o.Symbol == symbol {
			out = append(out, o)
		}
	}
	return out, nil
}

func (m *memOutputStore) DueOutlooks(_ context.Context, before time.Time) ([]Outlook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Outlook
	for _, o := range m.outlooks {
		if !o.Resolved() && o.CreatedAt.AddDate(0, 0, o.HorizonDays*7/5).Before(before) {
			out = append(out, o)
		}
	}
	return out, nil
}

func (m *memOutputStore) ResolveOutlook(_ context.Context, o *Outlook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.outlooks {
		if m.outlooks[i].ID == o.ID {
			m.outlooks[i] = *o
			return nil
		}
	}
	return nil
}

// stubMarket returns a fixed closing price.
type stubMarket struct {
	price float64
	err   error
}

func (s stubMarket) Candles(_ context.Context, sym marketdata.Symbol, iv marketdata.Interval, _ int) (marketdata.Series, error) {
	if s.err != nil {
		return marketdata.Series{}, s.err
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := make([]marketdata.Candle, 60)
	for i := range candles {
		candles[i] = marketdata.Candle{
			Time: base.AddDate(0, 0, i), Open: s.price, High: s.price,
			Low: s.price, Close: s.price, Volume: 1000,
		}
	}
	return marketdata.Series{Symbol: sym, Interval: iv, Candles: candles}, nil
}

func (s stubMarket) Quote(context.Context, marketdata.Symbol) (marketdata.QuoteResult, error) {
	return marketdata.QuoteResult{}, nil
}

func newCalibService(store OutputStore, market MarketSource, now time.Time) *Service {
	return NewService(nil, market, store, time.UTC,
		WithServiceLogger(quietLogger()),
		WithServiceClock(func() time.Time { return now }))
}

func TestResolveDueOutlooks(t *testing.T) {
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store := &memOutputStore{}

	// Created at 100, base 0%, bull +10%, bear -10%.
	o := Outlook{
		Symbol: "AAPL", CreatedAt: created, HorizonDays: 10, PriceAtCreation: 100,
		Base: Scenario{Probability: 0.6, MovePercent: 0},
		Bull: Scenario{Probability: 0.25, MovePercent: 10},
		Bear: Scenario{Probability: 0.15, MovePercent: -10},
	}
	store.SaveOutlook(context.Background(), &o)

	tests := []struct {
		name         string
		now          time.Time
		price        float64
		wantResolved int
		wantScenario string
	}{
		{
			name: "before the horizon nothing resolves",
			now:  created.AddDate(0, 0, 3), price: 120, wantResolved: 0,
		},
		{
			name: "after the horizon, a big rise is the bull case",
			now:  created.AddDate(0, 0, 20), price: 118, wantResolved: 1, wantScenario: ScenarioBull,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newCalibService(store, stubMarket{price: tc.price}, tc.now)
			got, err := svc.ResolveDueOutlooks(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.wantResolved {
				t.Fatalf("resolved %d, want %d", got, tc.wantResolved)
			}
			if tc.wantResolved == 0 {
				return
			}
			list, _ := store.ListOutlooks(context.Background(), "", 10)
			r := list[0]
			if !r.Resolved() {
				t.Fatal("the outlook was not marked resolved")
			}
			if r.ActualScenario != tc.wantScenario {
				t.Errorf("scenario = %q, want %q", r.ActualScenario, tc.wantScenario)
			}
			if r.RealizedMove == nil || math.Abs(*r.RealizedMove-18) > 0.001 {
				t.Errorf("realised move = %v, want 18", r.RealizedMove)
			}
			if r.BrierScore == nil {
				t.Fatal("no Brier score was recorded")
			}
		})
	}
}

func TestResolveSkipsWhenPriceIsUnavailable(t *testing.T) {
	// Scoring against a price we do not have would corrupt the record
	// permanently, so an unresolvable outlook waits for the next pass.
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store := &memOutputStore{}
	o := Outlook{
		Symbol: "AAPL", CreatedAt: created, HorizonDays: 10, PriceAtCreation: 100,
		Base: Scenario{Probability: 1, MovePercent: 0},
		Bull: Scenario{MovePercent: 10}, Bear: Scenario{MovePercent: -10},
	}
	store.SaveOutlook(context.Background(), &o)

	svc := newCalibService(store, stubMarket{err: context.DeadlineExceeded}, created.AddDate(0, 0, 30))
	got, err := svc.ResolveDueOutlooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("resolved %d, want 0", got)
	}
	list, _ := store.ListOutlooks(context.Background(), "", 10)
	if list[0].Resolved() {
		t.Error("an outlook was resolved without a price")
	}
}

func TestCalibration(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	store := &memOutputStore{}
	ctx := context.Background()

	// Ten outlooks that each claimed 70% on the base case. Seven of them
	// happened — so this model's 70% really is 70%, and the calibration page
	// should say so.
	for i := 0; i < 10; i++ {
		actual := ScenarioBase
		if i >= 7 {
			actual = ScenarioBull
		}
		resolvedAt := now
		score := 0.0
		o := Outlook{
			Symbol: "AAPL", CreatedAt: now.AddDate(0, 0, -30), HorizonDays: 10, PriceAtCreation: 100,
			Base:           Scenario{Probability: 0.7, MovePercent: 0},
			Bull:           Scenario{Probability: 0.2, MovePercent: 10},
			Bear:           Scenario{Probability: 0.1, MovePercent: -10},
			ResolvedAt:     &resolvedAt,
			ActualScenario: actual,
			BrierScore:     &score,
		}
		store.SaveOutlook(ctx, &o)
	}
	// One still inside its horizon.
	pending := Outlook{
		Symbol: "AAPL", CreatedAt: now, HorizonDays: 10, PriceAtCreation: 100,
		Base: Scenario{Probability: 0.5}, Bull: Scenario{Probability: 0.3}, Bear: Scenario{Probability: 0.2},
	}
	store.SaveOutlook(ctx, &pending)

	svc := newCalibService(store, stubMarket{price: 100}, now)
	cal, err := svc.Calibrate(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if cal.Resolved != 10 {
		t.Errorf("resolved = %d, want 10", cal.Resolved)
	}
	if cal.Pending != 1 {
		t.Errorf("pending = %d, want 1", cal.Pending)
	}
	if cal.BaselineBrier == 0 {
		t.Error("no baseline was reported; a Brier number alone means nothing to a reader")
	}

	// The 0.7 forecasts land in the 0.7–0.8 band.
	var band CalibrationBucket
	for _, b := range cal.Buckets {
		if b.Lower == 0.7 {
			band = b
		}
	}
	if band.Forecasts != 10 {
		t.Fatalf("the 70%% band holds %d forecasts, want 10", band.Forecasts)
	}
	if band.Occurred != 7 {
		t.Errorf("occurred = %d, want 7", band.Occurred)
	}
	if math.Abs(band.ObservedRate-0.7) > 0.001 {
		t.Errorf("observed rate = %v, want 0.7 — the model's 70%% really was 70%%", band.ObservedRate)
	}
	if math.Abs(band.MeanStated-0.7) > 0.001 {
		t.Errorf("mean stated = %v, want 0.7", band.MeanStated)
	}

	// Each resolved outlook contributes three forecasts across the bands.
	total := 0
	for _, b := range cal.Buckets {
		total += b.Forecasts
	}
	if total != 30 {
		t.Errorf("total forecasts = %d, want 30 (three per resolved outlook)", total)
	}
}

func TestCalibrationWithNoData(t *testing.T) {
	svc := newCalibService(&memOutputStore{}, stubMarket{price: 100}, time.Now())
	cal, err := svc.Calibrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cal.Resolved != 0 || cal.Pending != 0 {
		t.Errorf("counts = %d/%d, want 0/0", cal.Resolved, cal.Pending)
	}
	if len(cal.Buckets) != 10 {
		t.Errorf("got %d buckets, want 10 even when empty so the chart has an axis", len(cal.Buckets))
	}
}
