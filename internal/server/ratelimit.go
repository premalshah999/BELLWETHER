package server

import (
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Rate limiting for the routes that cost money.
//
// Every model-backed endpoint on this server spends real tokens from a fixed
// monthly budget, and the deployment has no authentication in front of it. A
// loop against /api/ai/classify-events would exhaust a month's allowance in
// minutes, and the first sign of it would be the morning brief refusing to
// generate.
//
// This is not a security control and does not pretend to be — anyone
// determined can change address. It is a spending control: it bounds what a
// mistake, a crawler, or a stuck retry loop can cost.

// visitor is one caller's token bucket.
type visitor struct {
	tokens   float64
	lastSeen time.Time
}

// rateLimiter is a per-caller token bucket.
//
// A bucket rather than a fixed window, because the traffic being shaped is
// bursty by nature: a person opening a page may legitimately trigger three
// calls at once and then nothing for ten minutes. A fixed window would refuse
// the third and permit a hundred a second later.
type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor

	// capacity is the burst allowance; refill is tokens gained per second.
	capacity float64
	refill   float64
	log      *slog.Logger
	now      func() time.Time
}

func newRateLimiter(capacity float64, per time.Duration, log *slog.Logger) *rateLimiter {
	if capacity <= 0 {
		capacity = 1
	}
	if per <= 0 {
		per = time.Minute
	}
	return &rateLimiter{
		visitors: map[string]*visitor{},
		capacity: capacity,
		refill:   capacity / per.Seconds(),
		log:      log,
		now:      time.Now,
	}
}

// allow reports whether a caller may proceed, and how long until they may.
func (r *rateLimiter) allow(key string) (bool, time.Duration) {
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	v, seen := r.visitors[key]
	if !seen {
		// A first-time caller starts with a full bucket minus this request,
		// so the common case of one person opening a page is never delayed.
		r.visitors[key] = &visitor{tokens: r.capacity - 1, lastSeen: now}
		return true, 0
	}

	// Refill for the time elapsed, capped at the burst allowance.
	v.tokens += now.Sub(v.lastSeen).Seconds() * r.refill
	if v.tokens > r.capacity {
		v.tokens = r.capacity
	}
	v.lastSeen = now

	if v.tokens < 1 {
		// How long until one token is available, so the caller can be told
		// rather than left to guess.
		wait := time.Duration((1 - v.tokens) / r.refill * float64(time.Second))
		return false, wait
	}
	v.tokens--
	return true, 0
}

// sweep drops callers that have been idle long enough to have a full bucket.
// Without it the map grows once per distinct address, forever.
func (r *rateLimiter) sweep() {
	cutoff := r.now().Add(-30 * time.Minute)
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, v := range r.visitors {
		if v.lastSeen.Before(cutoff) {
			delete(r.visitors, key)
		}
	}
}

// middleware enforces the limit.
func (r *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		key := callerKey(req)
		ok, wait := r.allow(key)
		if ok {
			next.ServeHTTP(w, req)
			return
		}
		r.log.Warn("rate limited", "caller", key, "path", req.URL.Path,
			"retry_after", wait.Round(time.Second))
		w.Header().Set("Retry-After", formatSeconds(wait))
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			"Too many requests to a model-backed endpoint. Try again in "+
				wait.Round(time.Second).String()+".")
	})
}

// callerKey identifies a caller.
//
// The proxy's forwarded address is preferred where present, because every
// request otherwise arrives from Caddy and the whole internet would share one
// bucket. Only the first entry is used: the rest of an X-Forwarded-For chain
// is caller-supplied and trivially spoofed.
func callerKey(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := indexByte(fwd, ','); i > 0 {
			fwd = fwd[:i]
		}
		if host := trimSpace(fwd); host != "" {
			return host
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func formatSeconds(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return itoa(secs)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
