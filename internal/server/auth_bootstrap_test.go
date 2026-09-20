package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/auth"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/storage"
)

type emptyKeyStore struct{ storage.Store }

func (emptyKeyStore) CountActiveKeys(context.Context) (int, error) { return 0, nil }
func (emptyKeyStore) ProfileForKey(context.Context, string) (auth.Profile, bool, error) {
	return auth.Profile{}, false, nil
}
func (emptyKeyStore) ProfileByPrefix(context.Context, string) (auth.Profile, bool, error) {
	return auth.Profile{}, false, nil
}
func (emptyKeyStore) TouchKey(context.Context, int64, time.Time) error { return nil }

func TestFreshPersistentDeploymentRequiresKey(t *testing.T) {
	for _, open := range []bool{false, true} {
		s := &Server{deps: Deps{Config: &config.Config{AllowUnauthenticated: open}, Store: emptyKeyStore{}}}
		called := false
		handler := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/research/ask", nil))
		if called != open {
			t.Fatalf("development opt-in=%v, endpoint called=%v", open, called)
		}
		if !open && w.Code != 401 {
			t.Fatalf("got %d", w.Code)
		}
		w = httptest.NewRecorder()
		s.handleAuthStatus(w, httptest.NewRequest("GET", "/api/auth/status", nil))
		var status map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if !open && (status["required"] != true || status["setup_required"] != true) {
			t.Fatalf("misleading setup status: %v", status)
		}
	}
}
