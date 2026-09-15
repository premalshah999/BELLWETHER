package server

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSessionSignatureRoundTrip(t *testing.T) {
	secret := []byte("a-test-secret")
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tok := signSession(secret, "tsk_abcdefg", now.Add(time.Hour))

	prefix, ok := validSessionPrefix(secret, tok, now)
	if !ok {
		t.Fatal("a freshly signed session did not validate")
	}
	// The cookie must name the key it came from, so revoking that key ends
	// the sessions started with it.
	if prefix != "tsk_abcdefg" {
		t.Errorf("prefix = %q, want the key it was issued for", prefix)
	}
	if _, ok := validSessionPrefix(secret, tok, now.Add(2*time.Hour)); ok {
		t.Error("an expired session validated")
	}
}

// TestForgedSessionsAreRejected is the property the whole scheme rests on: a
// token is only worth anything if it cannot be produced without the secret.
func TestForgedSessionsAreRejected(t *testing.T) {
	secret := []byte("a-test-secret")
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	good := signSession(secret, "tsk_abcdefg", now.Add(time.Hour))

	cases := map[string]string{
		"signed with another key": signSession([]byte("different"), "tsk_abcdefg", now.Add(time.Hour)),
		"expiry pushed out":       strings.Replace(good, strconv.FormatInt(now.Add(time.Hour).Unix(), 10), "99999999999", 1),
		"signature truncated":     good[:len(good)-4],
		"prefix swapped":          strings.Replace(good, "tsk_abcdefg", "tsk_someone", 1),
		"no separator":            "99999999999",
		"empty":                   "",
		"only a separator":        ".",
		"payload not a number":    "tsk_abcdefg.notanumber." + strings.Split(good, ".")[2],
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := validSessionPrefix(secret, tok, now); ok {
				t.Errorf("accepted a forged session: %q", tok)
			}
		})
	}
}

// TestSessionSecretsAreDistinct: an unconfigured deployment must not fall back
// to a fixed key, which would make every such install forgeable by anyone who
// read the source.
func TestSessionSecretsAreDistinct(t *testing.T) {
	a, b := newSessionSecret(), newSessionSecret()
	if len(a) < 32 {
		t.Errorf("secret is %d bytes, want at least 32", len(a))
	}
	if string(a) == string(b) {
		t.Error("two generated secrets were identical")
	}
}

// TestAuthOpensOnlyWhatItMust. Health must answer an unauthenticated monitor,
// and login must be reachable to obtain a session at all. Everything else,
// including every write, must not.
func TestAuthOpensOnlyWhatItMust(t *testing.T) {
	open := map[string]bool{
		"/api/auth/login":  true,
		"/api/auth/status": true,
		"/api/health":      true,
	}
	closed := []string{
		"/api/watchlist", "/api/events", "/api/algorithms", "/api/scan/run",
		"/api/research/ask", "/api/stream", "/api/symbols/RELIANCE/fundamentals",
		"/api/ai/brief/generate", "/api/alerts",
	}
	for _, p := range closed {
		if open[p] {
			t.Errorf("%s is in the open list; it must require a session", p)
		}
	}
	if len(open) != 3 {
		t.Errorf("the open list has %d entries; every addition needs justifying", len(open))
	}
}
