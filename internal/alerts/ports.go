package alerts

import (
	"context"
	"time"

	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// AlgorithmStore persists algorithm definitions.
type AlgorithmStore interface {
	ListAlgorithms(ctx context.Context) ([]*algo.Algorithm, error)
	GetAlgorithm(ctx context.Context, id int64) (*algo.Algorithm, error)
	CreateAlgorithm(ctx context.Context, a *algo.Algorithm) (int64, error)
	UpdateAlgorithm(ctx context.Context, a *algo.Algorithm) error
	DeleteAlgorithm(ctx context.Context, id int64) error
}

// AlertStore persists alerts and the per-symbol state that enforces cooldowns.
type AlertStore interface {
	InsertAlert(ctx context.Context, a *Alert) (int64, error)
	ListAlerts(ctx context.Context, f Filter) ([]Alert, error)
	MarkAlertRead(ctx context.Context, id int64) error
	MarkAllAlertsRead(ctx context.Context) error
	UnreadAlertCount(ctx context.Context) (int, error)

	// LastFiredAt reports when this algorithm last alerted on this symbol.
	// The boolean is false when it never has.
	LastFiredAt(ctx context.Context, algorithmID int64, symbol string) (time.Time, bool, error)
	// RecordEvaluation stores the outcome of an evaluation, firing or not.
	RecordEvaluation(ctx context.Context, rec EvaluationRecord) error
	// LastEvaluations returns the most recent outcome per symbol for an
	// algorithm, for the algorithm detail view.
	LastEvaluations(ctx context.Context, algorithmID int64) ([]EvaluationRecord, error)
}

// CandleSource is the market data the evaluator runs against. The router
// satisfies it, which means evaluations are served from cache whenever
// possible and never blow the provider budget on their own.
type CandleSource interface {
	Candles(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Series, error)
}

// Notifier delivers an alert down one channel.
type Notifier interface {
	// Channel names the delivery route, e.g. "telegram".
	Channel() string
	// Configured reports whether this notifier can actually send. An
	// unconfigured notifier is skipped silently rather than recorded as a
	// failed delivery.
	Configured() bool
	// Send delivers the message. It must respect ctx cancellation.
	Send(ctx context.Context, msg Message) error
}

// Message is a rendered notification.
type Message struct {
	// Title is the first line, e.g. "[ALGO] Momentum watch — RELIANCE".
	Title string
	// Body is the pre-rendered detail.
	Body string
	// Symbol and AlgorithmName let a notifier group or route messages.
	Symbol        string
	AlgorithmName string
}

// AIContextProvider supplies the optional short brief attached to an alert.
// It arrives in M3; until then the pipeline runs with a nil provider and every
// alert carries "AI context unavailable".
type AIContextProvider interface {
	// AlertContext returns a short brief and a status. It must never return
	// an error that blocks the alert: a missing brief degrades the
	// notification, it does not cancel it.
	AlertContext(ctx context.Context, a *Alert) (text string, status string)
}
