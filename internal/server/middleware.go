package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// requestLogger logs one line per request at debug level, and elevates slow or
// failing requests so they are visible at the default level.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			dur := time.Since(start)
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration", dur.Round(time.Millisecond),
				"request_id", middleware.GetReqID(r.Context()),
			}
			switch {
			case ww.Status() >= 500:
				log.Error("request failed", attrs...)
			case ww.Status() >= 400, dur > 5*time.Second:
				log.Warn("request", attrs...)
			default:
				log.Debug("request", attrs...)
			}
		})
	}
}

// recoverer turns a panic in a handler into a 500 rather than killing the
// process. A crashed goroutine in one endpoint must not take the dashboard
// down for the other operator.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil && rec != http.ErrAbortHandler {
					log.Error("handler panicked",
						"path", r.URL.Path,
						"panic", rec,
						"request_id", middleware.GetReqID(r.Context()))
					writeError(w, http.StatusInternalServerError, "internal_error",
						"Something broke while handling this request.")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// csp allows only this origin's own scripts. Inline styles stay allowed for
// React's style attributes; there is no inline script anywhere.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; " +
	"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// securityHeaders sets the browser protections on every response, so they
// hold even when the app is reached without the reverse proxy.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
