package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/auth"
)

// Authentication.
//
// This deployment is two operators on a self-hosted box reachable from the
// public internet, which is the case a shared passphrase fits: there are no
// roles to model, no user directory to keep, and no password reset flow worth
// building for two people who can edit the environment file.
//
// What it must not be is absent. Before this existed the host answered every
// request from anyone who found it, including the writes — deleting an
// algorithm, editing the watchlist, or spending the month's token budget took
// one unauthenticated POST.
//
// Sessions are stateless HMAC-signed cookies rather than server-side records.
// A restart then does not log everybody out, and there is no session table to
// grow, expire or leak.

const (
	sessionCookie = "tradesys_session"
	// sessionLifetime is long because this is a dashboard someone leaves open
	// on a desk, not a bank. Re-authenticating an operator mid-session costs
	// more than it protects against here.
	sessionLifetime = 30 * 24 * time.Hour
)

// signSession returns a cookie value proving which key the bearer signed in
// with, and until when.
//
// The key's prefix is carried rather than an opaque session id, so every
// request can re-check the key against the database. That is what makes
// revocation immediate: without it a withdrawn key would keep working for the
// life of a cookie nobody can see or cancel.
//
// The prefix is not a secret — it is the public half of the key, stored in
// clear precisely so it can be shown and logged — so putting it in a cookie
// reveals nothing that the key list does not.
func signSession(secret []byte, prefix string, expires time.Time) string {
	payload := prefix + "." + strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validSessionPrefix returns the key prefix a cookie was issued for, when the
// cookie is genuine and current.
func validSessionPrefix(secret []byte, value string, now time.Time) (string, bool) {
	idx := strings.LastIndex(value, ".")
	if idx < 0 {
		return "", false
	}
	payload, sig := value[:idx], value[idx+1:]

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	// Constant time: a byte-by-byte comparison that returns early leaks how
	// much of a forged signature was correct, which is enough to construct one.
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return "", false
	}

	prefix, expiry, ok := strings.Cut(payload, ".")
	if !ok {
		return "", false
	}
	unix, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || !now.Before(time.Unix(unix, 0)) {
		return "", false
	}
	return prefix, true
}

// newSessionSecret generates a random signing key.
//
// Used when none is configured, which means sessions do not survive a restart.
// That is a worse experience than a configured secret and a far better one
// than a predictable key, so it is the default rather than a fixed string.
func newSessionSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not a condition to paper over with a
		// weaker key; the process cannot offer authentication at all.
		panic("server: no source of randomness for the session secret: " + err.Error())
	}
	return b
}

// KeyStore resolves a presented key to its profile.
type KeyStore interface {
	// ProfileForKey takes a whole key and verifies its digest.
	ProfileForKey(ctx context.Context, presented string) (auth.Profile, bool, error)
	// ProfileByPrefix takes only the public prefix and verifies nothing; the
	// caller must already have proved possession, as a signed cookie does.
	ProfileByPrefix(ctx context.Context, prefix string) (auth.Profile, bool, error)
	TouchKey(ctx context.Context, id int64, at time.Time) error
	CountActiveKeys(ctx context.Context) (int, error)
}

type profileKey struct{}

// ProfileFrom returns the profile behind a request, if any.
func ProfileFrom(ctx context.Context) (auth.Profile, bool) {
	p, ok := ctx.Value(profileKey{}).(auth.Profile)
	return p, ok
}

// authenticate resolves a request to a profile.
//
// Two credentials are accepted and they are the same credential underneath. A
// browser presents the session cookie it received when it signed in with a
// key; a script presents the key directly in an Authorization header. Scripts
// have nowhere to keep a cookie jar, and browsers should not hold a
// long-lived key in storage a script on the page could read.
func (s *Server) authenticate(r *http.Request) (auth.Profile, bool) {
	store, ok := s.keyStore()
	if !ok {
		return auth.Profile{}, false
	}

	if raw := bearerToken(r); raw != "" {
		profile, found, err := store.ProfileForKey(r.Context(), raw)
		if err != nil {
			s.deps.Log.Error("could not check an API key", "err", err)
			return auth.Profile{}, false
		}
		if found {
			// Best effort, and never on the request path's critical route:
			// a failure to record usage must not fail the request.
			if err := store.TouchKey(r.Context(), profile.ID, s.now()); err != nil {
				// Warn, not debug. This write is best-effort and must never
				// fail a request, but a version of it that failed every time
				// went unnoticed precisely because the failure was logged
				// below the level anyone runs at.
				s.deps.Log.Warn("could not record key usage", "key", profile.Prefix, "err", err)
			}
			return profile, true
		}
		return auth.Profile{}, false
	}

	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return auth.Profile{}, false
	}
	prefix, ok := validSessionPrefix(s.sessionSecret, c.Value, s.now())
	if !ok {
		return auth.Profile{}, false
	}
	// The cookie names the key it was issued for, so revoking a key ends the
	// browser sessions started with it — otherwise a withdrawn key would keep
	// working for thirty days through a cookie nobody could see.
	//
	// Looked up by prefix, not by key: the cookie carries only the public
	// half, and the HMAC above is what proved the bearer had the secret.
	profile, found, err := store.ProfileByPrefix(r.Context(), prefix)
	if err != nil || !found {
		return auth.Profile{}, false
	}
	return profile, true
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	if v, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// requireAuth rejects unauthenticated requests.
//
// Open endpoints are the login route itself and the health check, which a
// monitor needs to reach without credentials and which reveals only whether
// providers are reachable.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login", "/api/auth/status", "/api/health":
			next.ServeHTTP(w, r)
			return
		}

		// Two states leave the API open, and both are reported identically by
		// the status endpoint so the interface and the gate can never
		// disagree about whether a sign-in is needed.
		//
		// A store that cannot hold keys at all — the in-memory one the tests
		// use — cannot authenticate anybody, so demanding credentials would
		// lock the door and throw away every key. And a store with no keys
		// issued yet must stay reachable long enough to issue the first one,
		// or a fresh install is a brick. Startup warns about the second, and
		// it ends the moment a key exists.
		store, ok := s.keyStore()
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if n, err := store.CountActiveKeys(r.Context()); err == nil && n == 0 {
			next.ServeHTTP(w, r)
			return
		}

		profile, ok := s.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
			return
		}

		// A viewer may read and nothing else. Anything that is not a plain
		// read is refused, including the model-backed routes: those are GET-
		// shaped in some cases but spend a shared budget, which is a write in
		// every sense that matters.
		if !profile.CanWrite() && r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusForbidden, "read_only",
				"This key is read-only. Ask an owner for an operator key to make changes.")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), profileKey{}, profile)))
	})
}

// keyStore returns the credential store, when one is wired.
func (s *Server) keyStore() (KeyStore, bool) {
	st, ok := s.deps.Store.(KeyStore)
	return st, ok
}

// handleLogin exchanges an API key for a browser session.
//
// The browser then holds a cookie rather than the key itself: a cookie can be
// HttpOnly, so a script on the page cannot read it, where a key kept in local
// storage is readable by anything running on the origin.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	store, ok := s.keyStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Sign-in is not available.")
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body with a key.")
		return
	}

	profile, found, err := store.ProfileForKey(r.Context(), body.Key)
	if err != nil {
		s.deps.Log.Error("could not check a key at sign-in", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not verify that key.")
		return
	}
	if !found {
		// Deliberately slow, and deliberately vague. The delay is the only
		// cost an attacker pays for a guess, and distinguishing "no such key"
		// from "revoked key" would confirm which prefixes exist.
		time.Sleep(750 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "unauthorized", "That key is not valid.")
		return
	}

	expires := s.now().Add(sessionLifetime)
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: signSession(s.sessionSecret, profile.Prefix, expires),
		Path:  "/",
		// The cookie is the credential; script must never be able to read it.
		HttpOnly: true,
		// Lax rather than Strict: the dashboard is opened from bookmarks and
		// links, and Strict would drop the session on every such arrival.
		SameSite: http.SameSiteLaxMode,
		// Set only over TLS in production. Behind the reverse proxy the app
		// sees plain HTTP, so this follows the forwarded scheme.
		Secure:  r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil,
		Expires: expires,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"profile":       profile,
	})
}

// handleLogout clears the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

// handleAuthStatus tells the front end whether it needs to show a sign-in
// form, and who is signed in.
//
// Answers without credentials on purpose: a client that cannot tell the
// difference between "not signed in" and "server down" shows the wrong screen
// for both.
//
// When no key has been issued yet the deployment is open, because a fresh
// install with no way in is not a secure install, it is a brick. Startup logs
// that state loudly and it ends the moment the first key exists.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"required": true, "authenticated": false}

	store, ok := s.keyStore()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"required": false, "authenticated": true})
		return
	}
	if n, err := store.CountActiveKeys(r.Context()); err == nil && n == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"required":      false,
			"authenticated": true,
			"note":          "No keys have been issued, so this deployment is open. Issue one with: tradesys -issue-key -name \"you\" -role owner",
		})
		return
	}
	if profile, ok := s.authenticate(r); ok {
		out["authenticated"] = true
		out["profile"] = profile
	}
	writeJSON(w, http.StatusOK, out)
}
