package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestRateLimiterAllowsABurstThenThrottles covers the shape this is meant to
// have: a person opening a page fires several calls at once and must not be
// refused, while a loop must be.
func TestRateLimiterAllowsABurstThenThrottles(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	rl := newRateLimiter(5, time.Minute, quietLog())
	rl.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if ok, _ := rl.allow("1.2.3.4"); !ok {
			t.Fatalf("request %d refused inside the burst allowance", i+1)
		}
	}
	ok, wait := rl.allow("1.2.3.4")
	if ok {
		t.Fatal("the sixth request should have been refused")
	}
	if wait <= 0 {
		t.Error("a refusal must say how long to wait")
	}

	// Refill: after a fifth of the window, one token is back.
	now = now.Add(12 * time.Second)
	if ok, _ := rl.allow("1.2.3.4"); !ok {
		t.Error("a token should have refilled")
	}
}

// TestRateLimiterIsPerCaller: one noisy caller must not throttle everyone.
func TestRateLimiterIsPerCaller(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	rl := newRateLimiter(2, time.Minute, quietLog())
	rl.now = func() time.Time { return now }

	rl.allow("1.1.1.1")
	rl.allow("1.1.1.1")
	if ok, _ := rl.allow("1.1.1.1"); ok {
		t.Fatal("the noisy caller should be throttled")
	}
	if ok, _ := rl.allow("2.2.2.2"); !ok {
		t.Error("a different caller must not be affected")
	}
}

// TestCallerKeyPrefersTheForwardedAddress: behind a reverse proxy every
// request arrives from the proxy, so without this the whole internet would
// share one bucket.
func TestCallerKeyPrefersTheForwardedAddress(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	if got := callerKey(r); got != "10.0.0.1" {
		t.Errorf("without a forwarded header, key = %q, want the remote address", got)
	}

	// The proxy wrote the last entry. Anything before it came from the
	// caller, who could otherwise take a fresh bucket with every request.
	r.Header.Set("X-Forwarded-For", "1.2.3.4 ,  203.0.113.9  ")
	if got := callerKey(r); got != "203.0.113.9" {
		t.Errorf("key = %q, want the trimmed last forwarded entry", got)
	}
	r.Header.Set("X-Forwarded-For", "198.51.100.4")
	if got := callerKey(r); got != "198.51.100.4" {
		t.Errorf("key = %q, want the only forwarded entry", got)
	}
}

func TestRateLimiterSweepsIdleCallers(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	rl := newRateLimiter(5, time.Minute, quietLog())
	rl.now = func() time.Time { return now }

	rl.allow("1.1.1.1")
	rl.sweep()
	if len(rl.visitors) != 1 {
		t.Errorf("swept a live caller")
	}
	now = now.Add(2 * time.Hour)
	rl.sweep()
	if len(rl.visitors) != 0 {
		t.Errorf("idle caller not swept: %d remain", len(rl.visitors))
	}
}

// TestRateLimitedRequestSaysWhen: a bare 429 leaves the caller guessing.
func TestRateLimitedRequestSaysWhen(t *testing.T) {
	rl := newRateLimiter(1, time.Minute, quietLog())
	h := rl.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/ai/brief/generate", nil)
		req.RemoteAddr = "9.9.9.9:1234"
		h.ServeHTTP(rec, req)

		if i == 0 && rec.Code != http.StatusOK {
			t.Fatalf("first request = %d, want 200", rec.Code)
		}
		if i == 1 {
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("second request = %d, want 429", rec.Code)
			}
			if rec.Header().Get("Retry-After") == "" {
				t.Error("a 429 must carry Retry-After")
			}
		}
	}
}
