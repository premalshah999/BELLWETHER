package news

import (
	"testing"
	"time"
)

// TestValidatePublishedRejectsImpossibleTimes covers a defect found in
// production: an article published in March 2026 was stamped 25 August 2026 by
// Google News and shown as the day's top watchlist story. Its claimed
// publication time postdated our own fetch of the feed, which cannot happen.
func TestValidatePublishedRejectsImpossibleTimes(t *testing.T) {
	discovered := time.Date(2026, 8, 25, 12, 37, 0, 0, time.UTC)

	cases := []struct {
		name      string
		published time.Time
		trust     TimestampTrust
		wantZero  bool
	}{
		{
			name:      "the observed defect: stamped four hours after we fetched it",
			published: time.Date(2026, 8, 25, 16, 36, 36, 0, time.UTC),
			trust:     TimestampObserved,
			wantZero:  true,
		},
		{
			name:      "an exchange filing shortly before we saw it",
			published: discovered.Add(-90 * time.Second),
			trust:     TimestampExact,
			wantZero:  false,
		},
		{
			name:      "small clock skew is tolerated",
			published: discovered.Add(2 * time.Minute),
			trust:     TimestampPublisher,
			wantZero:  false,
		},
		{
			name:      "large skew is not",
			published: discovered.Add(3 * time.Hour),
			trust:     TimestampPublisher,
			wantZero:  true,
		},
		{
			// An aggregator reporting the moment it noticed something tells
			// us nothing about when it was published.
			name:      "aggregator reporting its own sighting",
			published: discovered.Add(-10 * time.Second),
			trust:     TimestampObserved,
			wantZero:  true,
		},
		{
			// The same value from a publisher is a real publication time.
			name:      "publisher reporting a moment before we fetched",
			published: discovered.Add(-10 * time.Second),
			trust:     TimestampPublisher,
			wantZero:  false,
		},
		{
			name:      "genuinely older aggregator item is kept",
			published: discovered.Add(-6 * time.Hour),
			trust:     TimestampObserved,
			wantZero:  false,
		},
		{
			name:      "no claim at all",
			published: time.Time{},
			trust:     TimestampPublisher,
			wantZero:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, note := ValidatePublished(tc.published, discovered, tc.trust)
			if got.IsZero() != tc.wantZero {
				t.Errorf("got %v (zero=%v), want zero=%v (note %q)",
					got, got.IsZero(), tc.wantZero, note)
			}
			// A discarded timestamp must say why. Silently nulling it would
			// leave the next person to rediscover the whole problem.
			if got.IsZero() && !tc.published.IsZero() && note == "" {
				t.Error("a discarded timestamp must carry a reason")
			}
		})
	}
}

// TestSourceTimestampTrust pins which sources may be believed.
func TestSourceTimestampTrust(t *testing.T) {
	reg, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range reg.All() {
		got := s.TimestampTrust()
		switch s.Method {
		case MethodGoogleNews, MethodGDELT:
			if got != TimestampObserved {
				t.Errorf("%s (%s) = %s, want observed: an aggregator reports when it noticed an item",
					s.ID, s.Method, got)
			}
			if got.Reliable() {
				t.Errorf("%s must not be treated as a reliable publication time", s.ID)
			}
		case MethodSECFiling:
			if got != TimestampExact {
				t.Errorf("%s = %s, want exact: a filing carries its own filing time", s.ID, got)
			}
		}
	}
}

// TestLatencyIgnoresObservedTimestamps: measuring how far behind a publisher
// we are is meaningless when the "publication" time is another observer's
// sighting. Reporting it anyway would make the ingestion latency metric
// describe the gap between two aggregators.
func TestLatencyIgnoresObservedTimestamps(t *testing.T) {
	discovered := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	item := RawItem{
		PublishedAt:    discovered.Add(-30 * time.Minute),
		DiscoveredAt:   discovered,
		TimestampTrust: TimestampObserved,
	}
	if _, ok := item.Latency(); ok {
		t.Error("latency should not be reported against an observed timestamp")
	}
	item.TimestampTrust = TimestampPublisher
	if d, ok := item.Latency(); !ok || d != 30*time.Minute {
		t.Errorf("latency = %v (%v), want 30m against a publisher timestamp", d, ok)
	}
}
