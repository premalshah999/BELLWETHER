package company

import "testing"

func usMaster(t *testing.T) *Master {
	t.Helper()
	m, err := LoadEmbeddedWithUS()
	if err != nil {
		t.Fatalf("load master with US: %v", err)
	}
	return m
}

// TestResolvesUSCompanyByName is the regression for the bug this whole
// change exists to fix: the news resolver held NSE names and nothing else,
// so a US publisher's story could never name a company the app recognised,
// and the relevance gate then discarded it for having no entity.
func TestResolvesUSCompanyByName(t *testing.T) {
	m := usMaster(t)

	cases := []struct {
		text string
		want string
	}{
		{"Apple Inc. beat expectations on iPhone revenue", "AAPL"},
		{"NVIDIA Corporation guided higher for the data centre segment", "NVDA"},
		{"Shares of Microsoft Corporation rose after the announcement", "MSFT"},
	}
	for _, tc := range cases {
		matches := m.ResolveAboveForVenue(tc.text, 0.9, VenueUS)
		var got []string
		for _, mt := range matches {
			got = append(got, mt.CanonicalSymbol())
		}
		found := false
		for _, g := range got {
			if g == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("Resolve(%q) = %v, want %s among them", tc.text, got, tc.want)
		}
	}
}

// TestUSMatchesAreNotSuffixedNSE guards the trap that made this change
// necessary in the first place: the processor appended ".NSE" to every
// match, so a US company would have been filed under an NSE symbol.
func TestUSMatchesAreNotSuffixedNSE(t *testing.T) {
	m := usMaster(t)
	for _, mt := range m.ResolveAboveForVenue("Apple Inc. reported record revenue", 0.9, VenueUS) {
		if mt.Symbol != "AAPL" {
			continue
		}
		if got := mt.CanonicalSymbol(); got != "AAPL" {
			t.Errorf("CanonicalSymbol() = %q, want bare AAPL for a US listing", got)
		}
		if mt.Venue != VenueUS {
			t.Errorf("Venue = %q, want %q", mt.Venue, VenueUS)
		}
		return
	}
	t.Fatal("AAPL did not resolve at all")
}

// TestNSEResolutionUnchanged is the back-compat half: merging ten thousand
// US registrants must not cost a single NSE match that worked before.
func TestNSEResolutionUnchanged(t *testing.T) {
	m := usMaster(t)
	matches := m.ResolveAboveForVenue("Reliance Industries Limited announced a demerger", 0.9, VenueNSE)
	for _, mt := range matches {
		if mt.CanonicalSymbol() == "RELIANCE.NSE" {
			if mt.Venue != VenueNSE {
				t.Errorf("Venue = %q, want %q", mt.Venue, VenueNSE)
			}
			return
		}
	}
	var got []string
	for _, mt := range matches {
		got = append(got, mt.CanonicalSymbol())
	}
	t.Errorf("RELIANCE.NSE not resolved; got %v", got)
}

// TestCrossVenueTickerNeedsAPreference covers the genuinely ambiguous case
// the venue hint exists for: INFY is a real listing on both venues, so it
// resolves to whichever venue the asking source belongs to, and to nothing
// at all without a preference.
func TestCrossVenueTickerNeedsAPreference(t *testing.T) {
	m := usMaster(t)
	// IEX is listed on both venues and carries no curated NSE alias, so the
	// bare-ticker path is the only thing that can match it -- which is
	// exactly the path the venue preference was added for. (INFY is the
	// famous dual listing but has an NSE-only alias, so a phrase match
	// fires alongside the ticker match and muddies what is being tested.)
	const ticker = "IEX"
	nse, okNSE := m.LookupVenue(ticker, VenueNSE)
	us, okUS := m.LookupVenue(ticker, VenueUS)
	if !okNSE || !okUS {
		t.Skipf("%s is not dual-listed in the shipped data (nse=%v us=%v)", ticker, okNSE, okUS)
	}
	if nse.Venue != VenueNSE || us.Venue != VenueUS {
		t.Fatalf("lookup venues wrong: nse=%q us=%q", nse.Venue, us.Venue)
	}

	const text = "IEX announced a new contract today"
	pick := func(prefer string) []string {
		var out []string
		for _, mt := range m.ResolveForVenue(text, prefer) {
			if mt.Symbol == ticker {
				out = append(out, mt.CanonicalSymbol())
			}
		}
		return out
	}
	if got := pick(VenueUS); len(got) != 1 || got[0] != ticker {
		t.Errorf("US preference gave %v, want [%s]", got, ticker)
	}
	if got := pick(VenueNSE); len(got) != 1 || got[0] != ticker+".NSE" {
		t.Errorf("NSE preference gave %v, want [%s.NSE]", got, ticker)
	}
	if got := pick(""); len(got) != 0 {
		t.Errorf("no preference gave %v, want nothing -- a dual listing is ambiguous", got)
	}
}
