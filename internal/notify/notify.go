// Package notify holds the delivery channels for alerts and the small
// decorators that wrap them.
package notify

import (
	"context"

	"github.com/tradesys/dashboard/internal/alerts"
)

// Observer receives the outcome of a delivery attempt. It matches the health
// tracker's Observe method, so a notifier's liveness lights the same status
// dot as a market data provider's.
type Observer func(ctx context.Context, provider string, ok, skipped bool, err error)

// WithHealth wraps a notifier so every send reports to the health tracker.
//
// An unconfigured channel is never reported at all: it has no dot to turn red,
// because the operator simply has not set it up.
func WithHealth(inner alerts.Notifier, observe Observer) alerts.Notifier {
	return &healthReporting{inner: inner, observe: observe}
}

type healthReporting struct {
	inner   alerts.Notifier
	observe Observer
}

func (h *healthReporting) Channel() string  { return h.inner.Channel() }
func (h *healthReporting) Configured() bool { return h.inner.Configured() }

func (h *healthReporting) Send(ctx context.Context, msg alerts.Message) error {
	err := h.inner.Send(ctx, msg)
	if h.observe != nil && h.inner.Configured() {
		h.observe(ctx, h.inner.Channel(), err == nil, false, err)
	}
	return err
}
