package server

import (
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/eventstudy"
)

func TestStudyCacheExpires(t *testing.T) {
	var c studyCache
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	if _, ok := c.get("EARNINGS/5", t0); ok {
		t.Fatal("empty cache returned a hit")
	}
	c.put("EARNINGS/5", eventstudy.Result{EventType: "EARNINGS"}, t0)

	if res, ok := c.get("EARNINGS/5", t0.Add(studyTTL-time.Second)); !ok || res.EventType != "EARNINGS" {
		t.Fatalf("fresh entry missed: ok=%v res=%+v", ok, res)
	}
	if _, ok := c.get("EARNINGS/10", t0); ok {
		t.Fatal("a different holding period shared the entry")
	}
	if _, ok := c.get("EARNINGS/5", t0.Add(studyTTL+time.Second)); ok {
		t.Fatal("expired entry was still served")
	}
}
