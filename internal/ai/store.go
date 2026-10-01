package ai

import (
	"context"
	"time"
)

// Output is a stored AI response, kept so the archive page can show what the
// model said and when, and so a brief is not regenerated on every page load.
type Output struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Symbol    string    `json:"symbol,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Model     string    `json:"model"`
	Tokens    int       `json:"tokens"`
	// Content is the feature-specific payload, stored as JSON.
	Content []byte `json:"content"`
}

// Scenario is one branch of an outlook.
type Scenario struct {
	Probability float64 `json:"probability"`
	MovePercent float64 `json:"move_percent"`
	Reasoning   string  `json:"reasoning"`
}

// Outlook is a logged scenario forecast, later scored against what happened.
//
// Every outlook is recorded with its horizon precisely so the model's stated
// confidence can be checked. An AI feature that says "70% likely" without
// anyone ever measuring whether those 70%s come true is decoration; this is
// the part that makes it accountable.
type Outlook struct {
	ID              int64     `json:"id"`
	Symbol          string    `json:"symbol"`
	CreatedAt       time.Time `json:"created_at"`
	HorizonDays     int       `json:"horizon_days"`
	PriceAtCreation float64   `json:"price_at_creation"`

	Base Scenario `json:"base"`
	Bull Scenario `json:"bull"`
	Bear Scenario `json:"bear"`

	KeyRisk string `json:"key_risk"`
	Model   string `json:"model"`

	// Quant is the engine's distribution the outlook started from, Final the
	// distribution after the writer's adjustment, and Adjustment what was
	// changed and on what evidence. Outlooks written before the engine have
	// none of them.
	Quant      *Prior      `json:"quant,omitempty"`
	Final      *Prior      `json:"final,omitempty"`
	Adjustment *Adjustment `json:"adjustment,omitempty"`
	// QuantBrier is the prior's own Brier score on the same outcome, so the
	// writer's adjustments can be judged against leaving the model alone.
	QuantBrier *float64 `json:"quant_brier,omitempty"`

	// Resolution, filled once the horizon has passed.
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	RealizedPrice  *float64   `json:"realized_price,omitempty"`
	RealizedMove   *float64   `json:"realized_move_percent,omitempty"`
	ActualScenario string     `json:"actual_scenario,omitempty"`
	BrierScore     *float64   `json:"brier_score,omitempty"`
}

// Resolved reports whether this outlook has been scored.
func (o Outlook) Resolved() bool { return o.ResolvedAt != nil }

// OutputStore persists AI outputs and outlooks.
type OutputStore interface {
	SaveOutput(ctx context.Context, out *Output) (int64, error)
	LatestOutput(ctx context.Context, kind, symbol string) (Output, bool, error)

	SaveOutlook(ctx context.Context, o *Outlook) (int64, error)
	ListOutlooks(ctx context.Context, symbol string, limit int) ([]Outlook, error)
	// DueOutlooks returns unresolved outlooks whose horizon has passed.
	DueOutlooks(ctx context.Context, before time.Time) ([]Outlook, error)
	ResolveOutlook(ctx context.Context, o *Outlook) error
}
