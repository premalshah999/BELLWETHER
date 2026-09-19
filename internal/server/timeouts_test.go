package server

import (
	"testing"
	"time"
)

// LongestRouteTimeout is only useful while it really is the longest. If a new
// route gets a deadline above it, main's WriteTimeout is derived from a value
// that is no longer the maximum, and the connection is torn down before that
// handler can answer -- which looks like a failed request to the caller while
// the server logs a completed one.
//
// Listed explicitly rather than reflected out of the router: chi does not
// expose middleware deadlines, and having to add a line here alongside a new
// timeout constant is the point.
func TestLongestRouteTimeoutIsActuallyTheLongest(t *testing.T) {
	routeTimeouts := map[string]time.Duration{
		"standardRequestTimeout": standardRequestTimeout,
		"aiRequestTimeout":       aiRequestTimeout,
		"scanRequestTimeout":     scanRequestTimeout,
	}

	for name, d := range routeTimeouts {
		if d > LongestRouteTimeout {
			t.Errorf("%s (%v) exceeds LongestRouteTimeout (%v); raise LongestRouteTimeout, "+
				"or main's WriteTimeout will be shorter than this route's deadline",
				name, d, LongestRouteTimeout)
		}
	}
}
