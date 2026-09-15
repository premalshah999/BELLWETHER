package news

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestEventJSONOmitsZeroTimes covers a bug that reached production.
//
// `omitempty` does nothing for a time.Time, so an unclassified event was
// serialised with classified_at set to the year 1. A client checking for the
// field's presence concluded every event had been classified.
func TestEventJSONOmitsZeroTimes(t *testing.T) {
	e := Event{
		ID: 1, Headline: "Something happened",
		DiscoveredAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, field := range []string{"classified_at", "confirmed_at", "occurred_at", "published_at"} {
		if strings.Contains(body, field) {
			t.Errorf("unset %s must be omitted, got: %s", field, body)
		}
	}
	if strings.Contains(body, "0001-01-01") {
		t.Errorf("a zero time leaked into the payload: %s", body)
	}
	if !strings.Contains(body, "discovered_at") {
		t.Error("discovered_at must always be present: it is the one timestamp we can vouch for")
	}

	// A set time must survive.
	e.ClassifiedAt = time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC)
	b, _ = json.Marshal(e)
	if !strings.Contains(string(b), "classified_at") {
		t.Errorf("a set classified_at must be present: %s", b)
	}
}

func TestRawItemJSONOmitsZeroTimes(t *testing.T) {
	r := RawItem{ID: 1, Title: "x", DiscoveredAt: time.Now(), FetchedAt: time.Now()}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "0001-01-01") {
		t.Errorf("zero time leaked: %s", b)
	}
}
