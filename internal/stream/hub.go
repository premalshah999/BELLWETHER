// Package stream delivers live data to connected clients, replacing a dozen
// polling timers. It separates three concerns:
//
// - A Source produces updates: today a poller against the price sidecar, with
// a licensed feed a websocket consumer.
// - The Hub fans updates out so a slow subscriber affects neither a fast one
// nor the source.
// - The transport is server-sent events: the data flows one way and SSE
// reconnects by itself.
package stream

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Topic identifies a stream of related updates.
type Topic string

const (
	// TopicQuotes carries price updates, one message per instrument.
	TopicQuotes Topic = "quotes"
	// TopicEvents carries newly discovered market events.
	TopicEvents Topic = "events"
	// TopicScanner carries scanner findings as they are produced.
	TopicScanner Topic = "scanner"
	// TopicHealth carries pipeline state, so an operator can see a stalled
	// feed rather than inferring it from data that stopped changing.
	TopicHealth Topic = "health"
)

// Message is one update.
type Message struct {
	Topic Topic `json:"topic"`
	// Seq increases monotonically per topic, so a client can tell that it
	// missed something rather than silently carrying on with a gap.
	Seq int64 `json:"seq"`
	// Key identifies what the message is about — a symbol, usually. Used to
	// collapse superseded updates: a subscriber that has fallen behind wants
	// the latest price for a symbol, not every price it missed.
	Key  string    `json:"key,omitempty"`
	At   time.Time `json:"at"`
	Data any       `json:"data"`
}

// subscriberBuffer is how many messages may queue for one client.
//
// Deliberately small. A client that is more than this far behind on a price
// feed does not want the backlog — it wants the current price — and a large
// buffer only delays the moment we admit that.
const subscriberBuffer = 64

// Subscriber receives messages for a set of topics.
type Subscriber struct {
	ID     string
	Topics map[Topic]bool
	ch     chan Message

	// dropped counts messages this subscriber was too slow to receive. Kept
	// because a silent drop is indistinguishable from an absent update, and
	// the difference matters when the data is a price.
	dropped atomic.Int64
}

// C is the channel to read messages from.
func (s *Subscriber) C() <-chan Message { return s.ch }

// Dropped reports how many messages were discarded for this subscriber.
func (s *Subscriber) Dropped() int64 { return s.dropped.Load() }

// Hub fans messages out to subscribers.
//
// Safe for concurrent use. Publishing never blocks: a source producing ticks
// must not be slowed by a browser on a bad connection, and the alternative to
// dropping is unbounded memory growth followed by the whole process failing.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]*Subscriber
	seq  map[Topic]*atomic.Int64

	log *slog.Logger

	published atomic.Int64
	dropped   atomic.Int64
}

// NewHub builds a hub.
func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	h := &Hub{
		subs: map[string]*Subscriber{},
		seq:  map[Topic]*atomic.Int64{},
		log:  log,
	}
	for _, t := range []Topic{TopicQuotes, TopicEvents, TopicScanner, TopicHealth} {
		h.seq[t] = &atomic.Int64{}
	}
	return h
}

// Subscribe registers a client for a set of topics.
//
// The returned cancel function must be called when the client goes away;
// forgetting it leaks a channel and, worse, leaves the hub trying to deliver
// to a reader that will never read again.
func (h *Hub) Subscribe(id string, topics ...Topic) (*Subscriber, func()) {
	set := make(map[Topic]bool, len(topics))
	for _, t := range topics {
		set[t] = true
	}
	s := &Subscriber{
		ID:     id,
		Topics: set,
		ch:     make(chan Message, subscriberBuffer),
	}

	h.mu.Lock()
	h.subs[id] = s
	h.mu.Unlock()

	var once sync.Once
	return s, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			h.mu.Unlock()
			close(s.ch)
		})
	}
}

// Publish sends a message to every subscriber of its topic.
//
// Never blocks and never fails. A subscriber whose buffer is full has the
// message dropped and the drop counted, because the alternatives are worse:
// blocking would let one stalled browser stop the market data for everyone,
// and growing the buffer would defer that failure rather than avoid it.
func (h *Hub) Publish(topic Topic, key string, data any) {
	counter, ok := h.seq[topic]
	if !ok {
		counter = &atomic.Int64{}
	}
	msg := Message{
		Topic: topic,
		Seq:   counter.Add(1),
		Key:   key,
		At:    time.Now().UTC(),
		Data:  data,
	}
	h.published.Add(1)

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.subs {
		if !s.Topics[topic] {
			continue
		}
		select {
		case s.ch <- msg:
		default:
			s.dropped.Add(1)
			h.dropped.Add(1)
		}
	}
}

// Stats reports hub throughput, for the health surface.
type Stats struct {
	Subscribers int             `json:"subscribers"`
	Published   int64           `json:"published"`
	Dropped     int64           `json:"dropped"`
	PerTopic    map[Topic]int64 `json:"per_topic"`
}

// Stats returns a snapshot of hub activity.
func (h *Hub) Stats() Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	per := make(map[Topic]int64, len(h.seq))
	for t, c := range h.seq {
		per[t] = c.Load()
	}
	return Stats{
		Subscribers: len(h.subs),
		Published:   h.published.Load(),
		Dropped:     h.dropped.Load(),
		PerTopic:    per,
	}
}
