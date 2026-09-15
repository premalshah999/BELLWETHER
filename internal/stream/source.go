package stream

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// Source produces live updates for a set of instruments.
//
// The interface exists so that the transport, the hub and the browser do not
// know where prices come from. Today the only implementation polls the price
// sidecar, which is what is available without a market data licence. A
// broker's websocket feed — Kite, Upstox, SmartAPI, or an NSE licence — is a
// different implementation of this same interface and changes nothing above
// it.
//
// Being explicit about that boundary is the point. Polling and streaming
// differ in latency, not in shape, and building as though a real feed will
// arrive is far cheaper than retrofitting one.
type Source interface {
	// Name identifies the source in logs and health output.
	Name() string
	// Run produces updates until the context is cancelled. It must return
	// only when the context is done, recovering from its own errors: a source
	// that gives up on the first failure turns a transient network problem
	// into a dead feed until someone notices.
	Run(ctx context.Context, out chan<- Update) error
	// Latency describes what this source can actually deliver, so the
	// interface never implies more freshness than it has.
	Latency() time.Duration
}

// Update is one instrument's state at a moment.
type Update struct {
	Symbol        string  `json:"symbol"`
	Price         float64 `json:"price"`
	Change        float64 `json:"change"`
	ChangePercent float64 `json:"change_percent"`
	Volume        float64 `json:"volume,omitempty"`
	DayHigh       float64 `json:"day_high,omitempty"`
	DayLow        float64 `json:"day_low,omitempty"`
	// At is when the price was observed, not when it was forwarded. A client
	// deciding whether a number is current needs the former.
	At time.Time `json:"at"`
	// Source names where it came from, so a screen can distinguish a licensed
	// tick from a polled quote rather than presenting both as "live".
	Source string `json:"source"`
	// Stale marks an update the source could not refresh. Better to say so
	// than to keep republishing an old price as though it were new.
	Stale bool `json:"stale,omitempty"`
}

// Pump connects a source to the hub, restarting it when it fails.
//
// A source is expected to be long-lived and to fail occasionally — a
// websocket drops, a poller's upstream returns errors. The pump owns that
// lifecycle so no individual source has to implement its own retry loop, and
// so the backoff policy is one thing rather than several.
type Pump struct {
	Hub    *Hub
	Source Source
	Log    *slog.Logger

	mu       sync.RWMutex
	lastAt   time.Time
	restarts int
	running  bool
}

// backoff bounds for restarting a failed source.
const (
	restartMin = 2 * time.Second
	restartMax = 60 * time.Second
)

// Run pumps updates until the context is cancelled.
func (p *Pump) Run(ctx context.Context) {
	delay := restartMin
	for {
		if ctx.Err() != nil {
			return
		}

		out := make(chan Update, 256)
		done := make(chan struct{})

		go func() {
			defer close(done)
			for u := range out {
				p.mu.Lock()
				p.lastAt = time.Now()
				p.mu.Unlock()
				p.Hub.Publish(TopicQuotes, u.Symbol, u)
			}
		}()

		p.setRunning(true)
		err := p.Source.Run(ctx, out)
		close(out)
		<-done
		p.setRunning(false)

		if ctx.Err() != nil {
			return
		}

		p.mu.Lock()
		p.restarts++
		restarts := p.restarts
		p.mu.Unlock()

		p.log().Warn("stream source stopped; restarting",
			"source", p.Source.Name(), "err", err, "restarts", restarts, "in", delay)

		// Jittered so that several sources failing together — which is what a
		// network outage looks like — do not reconnect in lockstep and
		// hammer the far end at the same instant.
		jitter := time.Duration(rand.Int63n(int64(delay / 2)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay + jitter):
		}
		if delay < restartMax {
			delay *= 2
			if delay > restartMax {
				delay = restartMax
			}
		}
	}
}

func (p *Pump) setRunning(v bool) {
	p.mu.Lock()
	p.running = v
	p.mu.Unlock()
}

// Health describes what this pump is doing, for the health surface.
type Health struct {
	Source   string    `json:"source"`
	Running  bool      `json:"running"`
	LastAt   time.Time `json:"last_update_at,omitempty"`
	Restarts int       `json:"restarts"`
	// Latency is what this source can deliver, stated so a reader can tell a
	// five-second poll from a real tick feed.
	Latency string `json:"latency"`
	// Silent is how long since the last update. A feed that is running but
	// silent is a distinct failure from one that has stopped, and the
	// difference is invisible without this.
	Silent string `json:"silent,omitempty"`
}

// Health reports the pump's state.
func (p *Pump) Health() Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	h := Health{
		Source:   p.Source.Name(),
		Running:  p.running,
		LastAt:   p.lastAt,
		Restarts: p.restarts,
		Latency:  p.Source.Latency().String(),
	}
	if !p.lastAt.IsZero() {
		h.Silent = time.Since(p.lastAt).Round(time.Second).String()
	}
	return h
}

func (p *Pump) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}
