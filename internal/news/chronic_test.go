package news

import (
	"testing"
	"time"
)

// The distinction this draws is the one that was missing while FTC returned
// 403 on 282 consecutive polls and nothing said so. A source that is
// rate-limited and a source that will never work again were the same number.
func TestChronicSeparatesAnOutageFromSomethingBroken(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		h    SourceHealth
		want bool
		why  string
	}{
		{
			name: "rate limited, succeeded two hours ago",
			h:    SourceHealth{ConsecutiveFailures: 4, LastSuccessAt: now.Add(-2 * time.Hour)},
			want: false,
			why:  "GDELT's real shape: a few failures and a recent success is an outage, and it recovers on its own",
		},
		{
			name: "many failures but succeeded an hour ago",
			h:    SourceHealth{ConsecutiveFailures: 40, LastSuccessAt: now.Add(-time.Hour)},
			want: false,
			why:  "a burst of failures around a recent success is a bad afternoon, not a broken configuration",
		},
		{
			name: "wrong user-agent, never once succeeded",
			h:    SourceHealth{ConsecutiveFailures: 282},
			want: true,
			why:  "FTC's real shape: the URL was right and the request was wrong, and waiting would never have fixed it",
		},
		{
			name: "failing for days",
			h:    SourceHealth{ConsecutiveFailures: 60, LastSuccessAt: now.Add(-72 * time.Hour)},
			want: true,
			why:  "three days without a success is past any outage worth waiting out",
		},
		{
			name: "two failures, never polled successfully yet",
			h:    SourceHealth{ConsecutiveFailures: 2},
			want: false,
			why:  "a source added minutes ago has not earned a verdict yet",
		},
		{
			name: "healthy",
			h:    SourceHealth{ConsecutiveFailures: 0, LastSuccessAt: now},
			want: false,
			why:  "nothing is wrong",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.h.Chronic(now); got != tc.want {
				t.Errorf("Chronic = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// Chronic is a stricter condition than failing, never a looser one: a source
// that is up cannot be chronic.
func TestHealthySourcesAreNeverChronic(t *testing.T) {
	now := time.Now()
	h := SourceHealth{ConsecutiveFailures: 0, LastSuccessAt: now.Add(-100 * 24 * time.Hour)}
	if !h.Healthy() {
		t.Fatal("zero consecutive failures is healthy by definition")
	}
	if h.Chronic(now) {
		t.Error("a source that just succeeded must not be reported as chronically broken")
	}
}
