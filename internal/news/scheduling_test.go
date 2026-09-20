package news

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSlowFeedDoesNotHoldUpNextSchedulingTick(t *testing.T) {
	fast := make(chan struct{}, 4)
	slowStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(slowStarted)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, rssFixture)
		fast <- struct{}{}
	}))
	defer srv.Close()
	reg, err := NewRegistry(testSource(srv.URL+"/fast", func(s *Source) { s.ID = "fast" }), testSource(srv.URL+"/slow", func(s *Source) { s.ID = "slow" }))
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(reg, newFakeStore(), WithWorkers(2), WithEngineLogger(quietLogger()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { e.Run(ctx, 5*time.Millisecond); close(finished) }()
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow feed never started")
	}
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("fast feed never started")
	}
	// Wait for the first fast request's accounting, then make it due again.
	deadline := time.Now().Add(time.Second)
	for {
		e.mu.Lock()
		ready := !e.inWork["fast"]
		if ready {
			e.state["fast"].nextDue = time.Now().Add(-time.Second)
		}
		e.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fast request did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("slow feed blocked the next tick")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("scheduler failed to stop")
	}
}
