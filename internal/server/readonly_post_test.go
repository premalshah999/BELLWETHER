package server

import (
	"os"
	"strings"
	"testing"
)

// The allowlist is a security exception, so its contents are pinned. A route
// added here without the check that earned the others their place is how a
// read-only key quietly gains a write.
func TestReadOnlyPostsHoldsOnlyTheCheckedRoutes(t *testing.T) {
	want := map[string]bool{
		"/api/screens/run":         true,
		"/api/algorithms/validate": true,
		"/api/algorithms/preview":  true,
		"/api/algorithms/backtest": true,
	}
	for path := range readOnlyPosts {
		if !want[path] {
			t.Errorf("%s was added to readOnlyPosts. Confirm the handler performs no write "+
				"-- no Save, Insert, Update, Delete, Upsert or Touch -- and add it here with why.", path)
		}
	}
	for path := range want {
		if !readOnlyPosts[path] {
			t.Errorf("%s was removed from readOnlyPosts; a read-only key can no longer run it", path)
		}
	}
}

// The two near misses. Both are the saved variant of a route that is on the
// list, and both write: the saved screen records when it last ran, and the
// saved backtest persists its evaluation. Being one path segment away from an
// allowed route is exactly how a mistake would look.
func TestSavedVariantsAreNotAllowlisted(t *testing.T) {
	for _, path := range []string{
		"/api/screens/42/run",
		"/api/algorithms/42/backtest",
	} {
		if readOnlyPosts[path] {
			t.Errorf("%s is allowlisted, but the saved variants write", path)
		}
	}
}

// Prefix matching would have admitted both of those. Exact matching is the
// property that keeps the list meaning what it says.
func TestAllowlistIsMatchedExactlyNotByPrefix(t *testing.T) {
	for path := range readOnlyPosts {
		for _, suffix := range []string{"/42", "/extra", "x"} {
			if readOnlyPosts[path+suffix] {
				t.Errorf("%s matched by extension; the lookup must be exact", path+suffix)
			}
		}
	}
}

// Every allowlisted path must be a real POST route, or the list is protecting
// something that no longer exists.
func TestAllowlistedPathsAreRealRoutes(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read routing table: %v", err)
	}
	routes := string(src)
	for path := range readOnlyPosts {
		// Routes are declared relative to their chi sub-router, so the last
		// segment is what appears literally in the table.
		tail := path[strings.LastIndex(path, "/"):]
		if !strings.Contains(routes, `r.Post("`+tail+`"`) &&
			!strings.Contains(routes, `r.Post("`+strings.TrimPrefix(path, "/api")+`"`) {
			t.Errorf("%s is allowlisted but no matching r.Post is declared in server.go", path)
		}
	}
}
