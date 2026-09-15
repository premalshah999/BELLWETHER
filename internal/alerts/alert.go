// Package alerts turns algorithm evaluations into notifications.
//
// The pipeline is: evaluate -> cooldown check -> enrich -> optional AI context
// -> deliver -> persist. Persisting happens last but unconditionally: an alert
// that could not be delivered to Telegram is still recorded in the in-app
// feed, because a notification the operator never sees is worse than one that
// arrives by only one route.
package alerts

import (
	"time"

	"github.com/tradesys/dashboard/internal/algo"
)

// Alert is one triggered algorithm, recorded in full.
type Alert struct {
	ID            int64     `json:"id"`
	AlgorithmID   int64     `json:"algorithm_id"`
	AlgorithmName string    `json:"algorithm_name"`
	Symbol        string    `json:"symbol"`
	Interval      string    `json:"interval"`
	FiredAt       time.Time `json:"fired_at"`
	// BarTime is the candle the evaluation ran against, which can be
	// meaningfully older than FiredAt on a daily interval.
	BarTime time.Time `json:"bar_time"`
	Price   float64   `json:"price"`
	Summary string    `json:"summary"`

	// Conditions is the complete evaluation snapshot. It is stored so that a
	// surprising alert can be audited months later against the numbers that
	// actually produced it.
	Conditions []algo.ConditionResult `json:"conditions"`

	// AIContext is the optional model-written brief. It is always rendered
	// with a visible AI badge and never blended into the data display.
	AIContext string `json:"ai_context,omitempty"`
	// AIStatus is one of "", "ok", "unavailable", "budget_reached".
	AIStatus string `json:"ai_status,omitempty"`

	Delivery []DeliveryRecord `json:"delivery"`
	ReadAt   *time.Time       `json:"read_at,omitempty"`
}

// Read reports whether an operator has seen this alert.
func (a Alert) Read() bool { return a.ReadAt != nil }

// DeliveryRecord is one attempt to send an alert down one channel.
type DeliveryRecord struct {
	Channel string    `json:"channel"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"`
}

// Filter narrows an alert listing.
type Filter struct {
	// AlgorithmID of 0 means any algorithm.
	AlgorithmID int64
	// Symbol empty means any symbol.
	Symbol string
	// UnreadOnly restricts to alerts nobody has acknowledged.
	UnreadOnly bool
	Limit      int
	// Before pages backwards through history.
	Before time.Time
}

// EvaluationRecord is the outcome of evaluating one algorithm against one
// symbol, stored whether or not it fired.
//
// Keeping the non-firing evaluations is what makes "why didn't this alert?"
// answerable. Without it, a rule that silently reports insufficient data every
// morning looks identical to one whose conditions are simply not met.
type EvaluationRecord struct {
	AlgorithmID int64
	Symbol      string
	At          time.Time
	Status      algo.Status
	Reason      string
	Summary     string
}
