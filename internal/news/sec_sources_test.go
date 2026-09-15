package news

import "testing"

// TestSECSources checks the three sources the SEC filings tape ships: that
// they are individually valid, carry the contact User-Agent SEC's
// fair-access policy requires (an empty one gets every request 403'd,
// verified live against this host), are ranked as official, and register
// cleanly alongside the rest of the catalog without an id collision.
func TestSECSources(t *testing.T) {
	const ua = "TradeSys/1.0 (test@example.com)"
	sources := SECSources(ua)
	if len(sources) != 3 {
		t.Fatalf("SECSources returned %d sources, want 3 (8-K, Form 4, 13F)", len(sources))
	}
	wantIDs := map[string]bool{"sec-8k": true, "sec-form4": true, "sec-13f": true}
	for _, s := range sources {
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", s.ID, err)
		}
		if !wantIDs[s.ID] {
			t.Errorf("unexpected source id %q", s.ID)
		}
		delete(wantIDs, s.ID)
		if s.UserAgent != ua {
			t.Errorf("%s: UserAgent = %q, want %q", s.ID, s.UserAgent, ua)
		}
		if s.Method != MethodSECFiling {
			t.Errorf("%s: Method = %s, want %s", s.ID, s.Method, MethodSECFiling)
		}
		if !s.Official() {
			t.Errorf("%s: not ranked official -- SEC filings should carry TimestampExact via Official()", s.ID)
		}
		if s.Trust != TrustOfficial {
			t.Errorf("%s: Trust = %d, want %d", s.ID, s.Trust, TrustOfficial)
		}
	}
	if len(wantIDs) != 0 {
		t.Errorf("missing expected source ids: %v", wantIDs)
	}

	full := append(DefaultSources(), sources...)
	if _, err := NewRegistry(full...); err != nil {
		t.Errorf("SEC sources do not register cleanly alongside the static catalog: %v", err)
	}
}

// TestSECSourcesEmptyUserAgentStillValid documents the actual guard: an
// empty contact is a config-time decision (buildRegistry in cmd/tradesys
// simply does not call SECSources at all when SEC_USER_AGENT is unset), not
// something SECSources itself refuses -- Validate has no opinion on the
// User-Agent field, so this is not a runtime safety net.
func TestSECSourcesEmptyUserAgentStillValid(t *testing.T) {
	for _, s := range SECSources("") {
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", s.ID, err)
		}
	}
}
