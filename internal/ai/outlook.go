package ai

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/tradesys/dashboard/internal/indicators"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// DefaultHorizonDays is the outlook window: two trading weeks.
//
// It is deliberately short. A horizon long enough to be interesting is also
// long enough that nobody ever checks it, and an unchecked forecast cannot be
// calibrated.
const DefaultHorizonDays = 10

// probabilityTolerance is how far the three scenario probabilities may sum
// from 1.0 before being renormalised.
const probabilityTolerance = 0.02

type outlookPayload struct {
	Base    Scenario `json:"base"`
	Bull    Scenario `json:"bull"`
	Bear    Scenario `json:"bear"`
	KeyRisk string   `json:"key_risk"`
}

// GenerateOutlook produces a scenario outlook and logs it for later scoring.
func (s *Service) GenerateOutlook(ctx context.Context, sym marketdata.Symbol) (Outlook, Status, error) {
	if status, err := s.guard(ctx); err != nil {
		return Outlook{Symbol: sym.String()}, status, err
	}

	// A year of history: enough for a 52-week range and a stable volatility
	// estimate.
	series, err := s.market.Candles(ctx, sym, marketdata.Interval1d, 260)
	if err != nil || len(series.Candles) < 30 {
		return Outlook{Symbol: sym.String()}, StatusUnavailable,
			fmt.Errorf("ai: not enough price history for an outlook on %s: %w", sym, err)
	}
	candles := series.Candles
	closes := indicators.Closes(candles)
	last := closes[len(closes)-1]

	data := map[string]any{
		"Symbol":      sym.String(),
		"HorizonDays": DefaultHorizonDays,
		"Price":       formatFloat(last),
		"Volatility":  fmt.Sprintf("%.2f%% daily", realisedVolatility(closes, 20)*100),
		"TrendNote":   trendNote(candles),
		"RSI":         formatIndicator(indicators.RSI(closes, 14).Last()),
		"FromHigh":    distanceFrom(last, indicators.RollingMax(indicators.Highs(candles), 252).Last()),
		"FromLow":     distanceFrom(last, indicators.RollingMin(indicators.Lows(candles), 252).Last()),
		"News":        s.recentNewsFor(ctx, sym, 5),
	}

	prompt, err := UserPrompt(PromptOutlook, data)
	if err != nil {
		return Outlook{Symbol: sym.String()}, StatusUnavailable, err
	}

	var payload outlookPayload
	resp, err := s.client.CompleteJSON(ctx, Request{
		Feature:     FeatureOutlook,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.4,
		MaxTokens:   3500,
	}, &payload)
	if err != nil {
		return Outlook{Symbol: sym.String()}, statusFor(err), err
	}

	out := Outlook{
		Symbol:          sym.String(),
		CreatedAt:       s.now(),
		HorizonDays:     DefaultHorizonDays,
		PriceAtCreation: last,
		Base:            payload.Base,
		Bull:            payload.Bull,
		Bear:            payload.Bear,
		KeyRisk:         payload.KeyRisk,
		Model:           resp.Model,
	}

	if err := normaliseScenarios(&out); err != nil {
		return out, StatusUnavailable, err
	}

	if s.store != nil {
		if _, err := s.store.SaveOutlook(context.WithoutCancel(ctx), &out); err != nil {
			// Losing the log means losing the calibration record, which is the
			// point of the feature — but the operator still gets their answer.
			s.log.Error("could not log outlook for calibration", "symbol", sym, "err", err)
		}
	}
	return out, StatusOK, nil
}

// normaliseScenarios repairs what the model got structurally wrong.
//
// Models are unreliable at making three numbers sum to one. Rather than
// discard an otherwise useful answer, renormalise — but reject the answer
// outright if a probability is negative or the scenarios are ordered
// incoherently, because those indicate the model misunderstood the task and
// scoring such a forecast would pollute the calibration record.
func normaliseScenarios(o *Outlook) error {
	for name, sc := range map[string]Scenario{"base": o.Base, "bull": o.Bull, "bear": o.Bear} {
		if sc.Probability < 0 || math.IsNaN(sc.Probability) || math.IsInf(sc.Probability, 0) {
			return fmt.Errorf("ai: %s scenario has an invalid probability %v", name, sc.Probability)
		}
		if math.IsNaN(sc.MovePercent) || math.IsInf(sc.MovePercent, 0) {
			return fmt.Errorf("ai: %s scenario has an invalid move %v", name, sc.MovePercent)
		}
	}

	sum := o.Base.Probability + o.Bull.Probability + o.Bear.Probability
	if sum <= 0 {
		return fmt.Errorf("ai: scenario probabilities sum to %v", sum)
	}
	if math.Abs(sum-1.0) > probabilityTolerance {
		o.Base.Probability /= sum
		o.Bull.Probability /= sum
		o.Bear.Probability /= sum
	}

	if o.Bull.MovePercent <= o.Bear.MovePercent {
		return fmt.Errorf("ai: bull move %v is not above bear move %v", o.Bull.MovePercent, o.Bear.MovePercent)
	}
	return nil
}

// realisedVolatility is the standard deviation of daily log returns over the
// trailing window.
func realisedVolatility(closes []float64, period int) float64 {
	if len(closes) < period+1 {
		period = len(closes) - 1
	}
	if period < 2 {
		return 0
	}

	returns := make([]float64, 0, period)
	for i := len(closes) - period; i < len(closes); i++ {
		if i < 1 || closes[i-1] <= 0 || closes[i] <= 0 {
			continue
		}
		returns = append(returns, math.Log(closes[i]/closes[i-1]))
	}
	if len(returns) < 2 {
		return 0
	}

	var mean float64
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	var variance float64
	for _, r := range returns {
		variance += (r - mean) * (r - mean)
	}
	variance /= float64(len(returns) - 1)
	return math.Sqrt(variance)
}

func distanceFrom(price, level float64) string {
	if !indicators.IsDefined(level) || level == 0 {
		return "unknown"
	}
	return fmt.Sprintf("%+.1f%%", (price-level)/level*100)
}

func formatIndicator(v float64) string {
	if !indicators.IsDefined(v) {
		return "not available"
	}
	return fmt.Sprintf("%.1f", v)
}

// newsItem is the shape the prompt templates iterate over.
type newsItem struct {
	Symbol string
	Title  string
	Source string
	Age    string
}

func (s *Service) recentNewsFor(ctx context.Context, sym marketdata.Symbol, limit int) []newsItem {
	if s.news == nil {
		return nil
	}
	articles, err := s.news.ListArticles(ctx, sym.String(), limit)
	if err != nil {
		s.log.Debug("no stored news", "symbol", sym, "err", err)
		return nil
	}
	now := s.now()
	out := make([]newsItem, 0, len(articles))
	for _, a := range articles {
		out = append(out, newsItem{
			Symbol: a.Symbol, Title: a.Title, Source: a.Source, Age: a.Age(now),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Calibration
// ---------------------------------------------------------------------------

// Scenario labels used in the calibration record.
const (
	ScenarioBase = "base"
	ScenarioBull = "bull"
	ScenarioBear = "bear"
)

// classifyOutcome decides which scenario a realised move corresponds to.
//
// The boundaries sit midway between the base case and each wing, so every
// possible outcome lands in exactly one bucket and the three are exhaustive —
// which is what makes the Brier score meaningful.
func classifyOutcome(o Outlook, realisedMove float64) string {
	bullBoundary := (o.Base.MovePercent + o.Bull.MovePercent) / 2
	bearBoundary := (o.Base.MovePercent + o.Bear.MovePercent) / 2

	switch {
	case realisedMove >= bullBoundary:
		return ScenarioBull
	case realisedMove <= bearBoundary:
		return ScenarioBear
	default:
		return ScenarioBase
	}
}

// brierScore is the multi-category Brier score: the summed squared error
// across the three mutually exclusive scenarios. Zero is perfect; 2 is as
// wrong as it is possible to be. Lower is better.
func brierScore(o Outlook, actual string) float64 {
	score := 0.0
	for label, sc := range map[string]Scenario{
		ScenarioBase: o.Base, ScenarioBull: o.Bull, ScenarioBear: o.Bear,
	} {
		outcome := 0.0
		if label == actual {
			outcome = 1.0
		}
		diff := sc.Probability - outcome
		score += diff * diff
	}
	return score
}

// ResolveDueOutlooks scores every outlook whose horizon has elapsed.
//
// This is what turns stated confidence into a measurable track record. It runs
// on a schedule and needs no LLM call, so it keeps working when the AI budget
// is spent.
func (s *Service) ResolveDueOutlooks(ctx context.Context) (resolved int, err error) {
	if s.store == nil {
		return 0, nil
	}
	due, err := s.store.DueOutlooks(ctx, s.now())
	if err != nil {
		return 0, fmt.Errorf("ai: list due outlooks: %w", err)
	}

	for i := range due {
		o := due[i]
		sym, err := marketdata.ParseSymbol(o.Symbol)
		if err != nil {
			s.log.Warn("outlook has an unparseable symbol", "symbol", o.Symbol, "err", err)
			continue
		}

		series, err := s.market.Candles(ctx, sym, marketdata.Interval1d, 5)
		if err != nil || len(series.Candles) == 0 {
			// Leave it unresolved and try again on the next pass rather than
			// scoring it against a price we do not have.
			s.log.Debug("cannot resolve outlook yet", "symbol", o.Symbol, "err", err)
			continue
		}

		realisedPrice := series.Candles[len(series.Candles)-1].Close
		if o.PriceAtCreation == 0 {
			continue
		}
		realisedMove := (realisedPrice - o.PriceAtCreation) / o.PriceAtCreation * 100
		actual := classifyOutcome(o, realisedMove)
		score := brierScore(o, actual)
		now := s.now()

		o.ResolvedAt = &now
		o.RealizedPrice = &realisedPrice
		o.RealizedMove = &realisedMove
		o.ActualScenario = actual
		o.BrierScore = &score

		if err := s.store.ResolveOutlook(ctx, &o); err != nil {
			s.log.Error("could not store outlook resolution", "symbol", o.Symbol, "err", err)
			continue
		}
		resolved++
	}

	if resolved > 0 {
		s.log.Info("scored outlooks against realised prices", "count", resolved)
	}
	return resolved, nil
}

// CalibrationBucket is one probability band on the calibration page.
type CalibrationBucket struct {
	// Lower and Upper bound the stated-probability band, e.g. 0.6 to 0.7.
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
	// Forecasts is how many scenario predictions fell in this band.
	Forecasts int `json:"forecasts"`
	// Occurred is how many of them actually happened.
	Occurred int `json:"occurred"`
	// MeanStated is the average probability the model claimed in this band.
	MeanStated float64 `json:"mean_stated"`
	// ObservedRate is the fraction that actually happened. The whole question
	// the page exists to answer is whether this tracks MeanStated.
	ObservedRate float64 `json:"observed_rate"`
}

// Calibration is the model's measured track record.
type Calibration struct {
	Buckets []CalibrationBucket `json:"buckets"`
	// Resolved is how many outlooks have been scored.
	Resolved int `json:"resolved"`
	// Pending is how many are still inside their horizon.
	Pending int `json:"pending"`
	// MeanBrier is the average Brier score across resolved outlooks. Lower is
	// better; 0.67 is what always guessing one-third-each would score.
	MeanBrier float64 `json:"mean_brier"`
	// BaselineBrier is that uniform-guess score, for comparison. Without it a
	// Brier number means nothing to a reader.
	BaselineBrier float64   `json:"baseline_brier"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// uniformBrier is what a forecaster scores by always saying one-third each:
// 2 x (1/3)^2 + (2/3)^2 = 0.667.
const uniformBrier = 2.0/9.0 + 4.0/9.0

// Calibrate computes the model's track record from resolved outlooks.
func (s *Service) Calibrate(ctx context.Context) (Calibration, error) {
	out := Calibration{GeneratedAt: s.now(), BaselineBrier: uniformBrier}
	if s.store == nil {
		return out, nil
	}

	outlooks, err := s.store.ListOutlooks(ctx, "", 1000)
	if err != nil {
		return out, fmt.Errorf("ai: list outlooks: %w", err)
	}

	// Ten bands of ten percentage points each.
	const bands = 10
	buckets := make([]CalibrationBucket, bands)
	for i := range buckets {
		buckets[i].Lower = float64(i) / bands
		buckets[i].Upper = float64(i+1) / bands
	}

	var brierSum float64
	for _, o := range outlooks {
		if !o.Resolved() {
			out.Pending++
			continue
		}
		out.Resolved++
		if o.BrierScore != nil {
			brierSum += *o.BrierScore
		}

		// Each outlook contributes three forecasts: one per scenario.
		for label, sc := range map[string]Scenario{
			ScenarioBase: o.Base, ScenarioBull: o.Bull, ScenarioBear: o.Bear,
		} {
			idx := int(sc.Probability * bands)
			if idx >= bands {
				idx = bands - 1
			}
			if idx < 0 {
				idx = 0
			}
			buckets[idx].Forecasts++
			buckets[idx].MeanStated += sc.Probability
			if label == o.ActualScenario {
				buckets[idx].Occurred++
			}
		}
	}

	for i := range buckets {
		if buckets[i].Forecasts == 0 {
			continue
		}
		buckets[i].MeanStated /= float64(buckets[i].Forecasts)
		buckets[i].ObservedRate = float64(buckets[i].Occurred) / float64(buckets[i].Forecasts)
	}
	if out.Resolved > 0 {
		out.MeanBrier = brierSum / float64(out.Resolved)
	}
	out.Buckets = buckets
	return out, nil
}
