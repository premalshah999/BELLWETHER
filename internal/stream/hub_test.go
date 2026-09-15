package stream

import (
	"sync"
	"testing"
	"time"
)

func TestHubDeliversToSubscribedTopicsOnly(t *testing.T) {
	h := NewHub(nil)
	quotes, cancelQ := h.Subscribe("a", TopicQuotes)
	defer cancelQ()
	events, cancelE := h.Subscribe("b", TopicEvents)
	defer cancelE()

	h.Publish(TopicQuotes, "RELIANCE", 1234.5)

	select {
	case m := <-quotes.C():
		if m.Key != "RELIANCE" {
			t.Errorf("key = %q, want RELIANCE", m.Key)
		}
	case <-time.After(time.Second):
		t.Fatal("quote subscriber received nothing")
	}

	select {
	case m := <-events.C():
		t.Errorf("event subscriber received a quote: %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSlowSubscriberNeverBlocksTheHub is the property the whole design rests
// on. One browser on a bad connection must not be able to stop the market
// data for every other client, and it must not be able to stall the source
// producing it. The cost is dropped messages for that one client, which is
// counted rather than hidden.
func TestSlowSubscriberNeverBlocksTheHub(t *testing.T) {
	h := NewHub(nil)
	slow, cancel := h.Subscribe("slow", TopicQuotes)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the buffer, from a subscriber that never reads.
		for i := 0; i < subscriberBuffer*20; i++ {
			h.Publish(TopicQuotes, "X", i)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that was not reading")
	}

	if slow.Dropped() == 0 {
		t.Error("expected drops to be counted for a subscriber that never read")
	}
	if got := h.Stats().Dropped; got == 0 {
		t.Error("hub did not record the drops")
	}
}

// TestSequenceIsMonotonicPerTopic: a client uses the id to notice it missed
// something. Sequences that repeat or run backwards make a gap undetectable.
func TestSequenceIsMonotonicPerTopic(t *testing.T) {
	h := NewHub(nil)
	sub, cancel := h.Subscribe("s", TopicQuotes)
	defer cancel()

	const n = 20
	for i := 0; i < n; i++ {
		h.Publish(TopicQuotes, "X", i)
	}
	var last int64
	for i := 0; i < n; i++ {
		m := <-sub.C()
		if m.Seq <= last {
			t.Fatalf("sequence went backwards: %d after %d", m.Seq, last)
		}
		last = m.Seq
	}

	// Topics number independently, so traffic on one does not create
	// phantom gaps on another.
	h.Publish(TopicEvents, "Y", 1)
	ev, cancelE := h.Subscribe("e", TopicEvents)
	defer cancelE()
	h.Publish(TopicEvents, "Y", 2)
	if m := <-ev.C(); m.Seq != 2 {
		t.Errorf("event sequence = %d, want 2 — topics must count separately", m.Seq)
	}
}

// TestUnsubscribeIsIdempotent: the cancel function is deferred in a request
// handler and may also run on an error path. Closing twice would panic.
func TestUnsubscribeIsIdempotent(t *testing.T) {
	h := NewHub(nil)
	_, cancel := h.Subscribe("s", TopicQuotes)
	cancel()
	cancel()

	if got := h.Stats().Subscribers; got != 0 {
		t.Errorf("subscribers = %d after cancelling, want 0", got)
	}
	// Publishing to nobody must not panic.
	h.Publish(TopicQuotes, "X", 1)
}

// TestConcurrentSubscribeAndPublish exercises the lock discipline: clients
// connect and disconnect constantly while a source publishes.
func TestConcurrentSubscribeAndPublish(t *testing.T) {
	h := NewHub(nil)
	var wg sync.WaitGroup

	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				h.Publish(TopicQuotes, "X", 1)
			}
		}
	}()

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub, cancel := h.Subscribe(string(rune('a'+i%26))+string(rune('0'+i/26)), TopicQuotes)
			select {
			case <-sub.C():
			case <-time.After(200 * time.Millisecond):
			}
			cancel()
		}(i)
	}

	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	time.Sleep(300 * time.Millisecond)
	close(stop)

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock between Subscribe, Publish and cancel")
	}
}
