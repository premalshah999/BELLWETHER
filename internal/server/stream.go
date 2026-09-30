package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/stream"
)

// Server-sent events.
//
// Chosen over websockets because the data travels one way and SSE reconnects
// on its own: the browser's EventSource retries with no client code, which
// removes the single most common source of bugs in a hand-rolled websocket
// client. What it costs is the ability for the client to talk back, which
// nothing here needs.

const (
	// heartbeatInterval keeps intermediaries from closing an idle connection
	// and lets the client distinguish "nothing is happening" from "the
	// connection died". Without it, an overnight stream looks identical to a
	// broken one.
	heartbeatInterval = 20 * time.Second
)

// handleStream opens a server-sent event stream.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.deps.Stream == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Streaming is not enabled.")
		return
	}

	topics := parseTopics(r.URL.Query().Get("topics"))
	if len(topics) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request",
			"Name at least one topic: quotes, events, scanner, health.")
		return
	}

	// Clear the write deadline for this connection only. WriteTimeout is right
	// for ordinary requests, but an event stream never completes, and the
	// deadline would sever every client on a fixed schedule.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		s.deps.Log.Warn("could not clear the stream write deadline; "+
			"connections will be cut when it expires", "err", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	// Nginx and some proxies buffer responses by default, which turns a
	// stream into a very slow download. This header disables that where it is
	// honoured; the Caddy configuration disables it explicitly too.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub, cancel := s.deps.Stream.Subscribe(callerKey(r)+"/"+r.Header.Get("Last-Event-ID"), topics...)
	defer cancel()

	// The retry field tells the browser how long to wait before reconnecting.
	// Set explicitly rather than relying on the default, which varies.
	fmt.Fprintf(w, "retry: 3000\n\n")
	// Flushed through ResponseController rather than a direct type assertion
	// on http.Flusher: middleware wraps the ResponseWriter, and a wrapper
	// that forgets to implement Flusher would otherwise turn the stream into
	// a download that never arrives.
	if err := rc.Flush(); err != nil {
		return
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return

		case msg, open := <-sub.C():
			if !open {
				return
			}
			payload, err := json.Marshal(msg.Data)
			if err != nil {
				s.deps.Log.Warn("could not encode stream message",
					"topic", msg.Topic, "err", err)
				continue
			}
			// The event id is the topic sequence, so a reconnecting client
			// can report where it got to via Last-Event-ID and a gap becomes
			// visible rather than silent.
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", msg.Seq, msg.Topic, payload)
			if err := rc.Flush(); err != nil {
				return
			}

		case <-heartbeat.C:
			// A comment line: valid SSE, ignored by EventSource, and enough
			// to keep the connection alive and detect a dead peer.
			fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix())
			if err := rc.Flush(); err != nil {
				// The peer has gone. Returning here is what releases the
				// subscription; without it a dead browser holds a hub slot
				// until the process restarts.
				return
			}
		}
	}
}

// parseTopics turns the query parameter into topics, ignoring anything it does
// not recognise rather than failing the whole subscription.
func parseTopics(raw string) []stream.Topic {
	known := map[string]stream.Topic{
		"quotes":  stream.TopicQuotes,
		"events":  stream.TopicEvents,
		"scanner": stream.TopicScanner,
		"health":  stream.TopicHealth,
	}
	var out []stream.Topic
	seen := map[stream.Topic]bool{}
	for _, part := range strings.Split(raw, ",") {
		t, ok := known[strings.ToLower(strings.TrimSpace(part))]
		if !ok || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// handleStreamTest publishes a synthetic message onto the live hub.
//
// Exists because the streaming path cannot otherwise be exercised outside
// market hours, and a delivery pipeline that has only ever been tested when
// it had nothing to deliver has not been tested. It publishes to the health
// topic specifically so a test message can never be mistaken for a price.
func (s *Server) handleStreamTest(w http.ResponseWriter, r *http.Request) {
	if s.deps.Stream == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Streaming is not enabled.")
		return
	}
	s.deps.Stream.Publish(stream.TopicHealth, "test", map[string]any{
		"kind": "test",
		"at":   time.Now().UTC(),
		"note": "synthetic message; not market data",
	})
	writeJSON(w, http.StatusOK, map[string]any{"published": true})
}

// handleStreamHealth reports the state of every live pipeline.
//
// Separate from the general health endpoint because these answer a different
// question: not "is the provider reachable" but "is data still arriving". A
// feed that is connected and silent is the failure mode that costs money, and
// it is invisible unless something reports the silence.
func (s *Server) handleStreamHealth(w http.ResponseWriter, r *http.Request) {
	if s.deps.Stream == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Streaming is not enabled.")
		return
	}
	out := map[string]any{"hub": s.deps.Stream.Stats()}
	if s.deps.StreamPumps != nil {
		pumps := make([]stream.Health, 0, len(s.deps.StreamPumps))
		for _, p := range s.deps.StreamPumps {
			pumps = append(pumps, p.Health())
		}
		out["sources"] = pumps
	}
	writeJSON(w, http.StatusOK, out)
}
