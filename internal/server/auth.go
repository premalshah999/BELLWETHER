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

// Authentication: API keys with roles, issued from the command line, and
// stateless HMAC-signed session cookies, so a restart logs nobody out and
// there is no session table to grow, expire or leak.

const (
	sessionCookie = "tradesys_session"
	// sessionLifetime is long because this is a dashboard someone leaves open
	// on a desk, not a bank. Re-authenticating an operator mid-session costs
	// more than it protects against here.
	sessionLifetime = 30 * 24 * time.Hour
)

// signSession returns a cookie value proving which key the bearer signed in
// with, and until when. It carries the key's public prefix rather than an
// opaque id, so every request re-checks the key and revoking it ends its
// sessions at once.
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

type profileKey struct{}

// authenticate resolves a request to a profile.
//
// Two credentials are accepted and they are the same credential underneath. A
// browser presents the session cookie it received when it signed in with a
// key; a script presents the key directly in an Authorization header. Scripts
// have nowhere to keep a cookie jar, and browsers should not hold a
// long-lived key in storage a script on the page could read.
func (s *Server) authenticate(r *http.Request) (auth.Profile, bool) {

	if raw := bearerToken(r); raw != "" {
		profile, found, err := s.deps.Store.ProfileForKey(r.Context(), raw)
		if err != nil {
			s.deps.Log.Error("could not check an API key", "err", err)
			return auth.Profile{}, false
		}
		if found {
			// Best effort, and never on the request path's critical route:
			// a failure to record usage must not fail the request.
			if err := s.deps.Store.TouchKey(r.Context(), profile.ID, s.now()); err != nil {
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
	profile, found, err := s.deps.Store.ProfileByPrefix(r.Context(), prefix)
	if err != nil || !found {
		return auth.Profile{}, false
	}
	return profile, true
}

// readOnlyPosts are the POST routes a read-only key may call, each checked to
// contain no write: an ad-hoc screen run, and validating, previewing or
// backtesting an unsaved rule. Their saved-object variants are absent because
// they record a last-run time. Exact paths, so a similarly named route cannot
// fall into the set.
var readOnlyPosts = map[string]bool{
	"/api/screens/run":         true,
	"/api/algorithms/validate": true,
	"/api/algorithms/preview":  true,
	"/api/algorithms/backtest": true,
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

		if s.openAccess(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}

		profile, ok := s.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
			return
		}

		// A viewer may read and nothing else: anything that is not a GET is
		// refused, which includes every model-backed route, since those spend
		// a shared budget. readOnlyPosts is the exception for reads that need
		// a request body.
		if !profile.CanWrite() && r.Method != http.MethodGet && r.Method != http.MethodHead &&
			!readOnlyPosts[r.URL.Path] {
			writeError(w, http.StatusForbidden, "read_only",
				"This key is read-only. Ask an owner for an operator key to make changes.")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), profileKey{}, profile)))
	})
}

// openAccess is the development opt-in: ALLOW_UNAUTHENTICATED is set and no
// key has been issued yet. A deployment with keys always requires one.
func (s *Server) openAccess(ctx context.Context) bool {
	if s.deps.Config == nil || !s.deps.Config.AllowUnauthenticated {
		return false
	}
	n, err := s.deps.Store.CountActiveKeys(ctx)
	return err == nil && n == 0
}

// handleLogin exchanges an API key for a browser session.
//
// The browser then holds a cookie rather than the key itself: a cookie can be
// HttpOnly, so a script on the page cannot read it, where a key kept in local
// storage is readable by anything running on the origin.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body with a key.")
		return
	}

	profile, found, err := s.deps.Store.ProfileForKey(r.Context(), body.Key)
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

// handleAuthStatus tells the front end whether to show a sign-in form and who
// is signed in. It answers without credentials, so the client can tell "not
// signed in" from "server down".
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"required": true, "authenticated": false}

	if n, err := s.deps.Store.CountActiveKeys(r.Context()); err == nil && n == 0 {
		if s.deps.Config == nil || !s.deps.Config.AllowUnauthenticated {
			writeJSON(w, http.StatusOK, map[string]any{"required": true, "authenticated": false, "setup_required": true})
			return
		}
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
