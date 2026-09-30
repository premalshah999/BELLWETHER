package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// A deployment with no keys yet stays closed unless the operator opts in.
func TestFreshPersistentDeploymentRequiresKey(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run database-backed server tests")
	}
	db, drop, err := postgres.OpenScratch(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)

	for _, open := range []bool{false, true} {
		s := &Server{deps: Deps{Config: &config.Config{AllowUnauthenticated: open}, Store: db}}
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
