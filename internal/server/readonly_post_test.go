package server

import (
	"maps"
	"os"
	"strings"
	"testing"
)

// The allowlist is a security exception, so its contents are pinned: a POST
// added here without checking that its handler writes nothing is how a
// read-only key quietly gains a write. The saved-object variants
// (/api/screens/{id}/..., /api/algorithms/{id}/...) must never appear.
func TestReadOnlyPostsHoldsOnlyTheCheckedRoutes(t *testing.T) {
	want := map[string]bool{
		"/api/screens/run":         true,
		"/api/algorithms/preview":  true,
		"/api/algorithms/backtest": true,
	}
	if !maps.Equal(readOnlyPosts, want) {
		t.Errorf("readOnlyPosts = %v, want exactly %v", readOnlyPosts, want)
	}

	// Each must be a real POST route, or the list protects nothing.
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read routing table: %v", err)
	}
	for path := range readOnlyPosts {
		tail := path[strings.LastIndex(path, "/"):]
		if !strings.Contains(string(src), `r.Post("`+tail+`"`) &&
			!strings.Contains(string(src), `r.Post("`+strings.TrimPrefix(path, "/api")+`"`) {
			t.Errorf("%s is allowlisted but no matching r.Post is declared in server.go", path)
		}
	}
}
