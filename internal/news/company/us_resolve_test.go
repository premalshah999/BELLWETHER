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

// TestExchangePrefixIsNotACompany is the regression for a false positive
// that reached the live feed: "NVIDIA (NASDAQ:NVDA) Shares Up 1.3%"
// resolved to NVDA *and* NDAQ, because Nasdaq Inc. is itself a listed
// company and "NASDAQ" was being read as a mention of it.
func TestExchangePrefixIsNotACompany(t *testing.T) {
	m := usMaster(t)
	for _, text := range []string{
		"NVIDIA (NASDAQ:NVDA) Shares Up 1.3% - Still a Buy?",
		"Amazon.com (NASDAQ:AMZN) Trading 1% Higher - Here's Why",
		"Coca-Cola (NYSE: KO) declares quarterly dividend",
	} {
		var got []string
		for _, mt := range m.ResolveAboveForVenue(text, 0.85, VenueUS) {
			got = append(got, mt.CanonicalSymbol())
		}
		for _, sym := range got {
			if sym == "NDAQ" || sym == "ICE" {
				t.Errorf("Resolve(%q) = %v, must not read the exchange name as a company", text, got)
			}
		}
		if len(got) == 0 {
			t.Errorf("Resolve(%q) = nothing, want the ticker after the exchange prefix", text)
		}
	}
}

// TestShoutedHeadlineYieldsNoBareTickers is the regression for the other
// false positive that reached the feed: an all-capitals press release
// ("PROJECT HEALTHY MINDS SETS THE STAGE FOR ITS FOURTH ANNUAL...")
// matched FOR, a real ticker, because every word in it looks like one.
func TestShoutedHeadlineYieldsNoBareTickers(t *testing.T) {
	m := usMaster(t)
	const shouted = "PROJECT HEALTHY MINDS SETS THE STAGE FOR ITS FOURTH ANNUAL WORLD MENTAL HEALTH DAY"
	for _, mt := range m.ResolveAboveForVenue(shouted, 0.85, VenueUS) {
		if mt.Method == MethodTicker {
			t.Errorf("shouted headline produced a bare-ticker match %s -- capitalisation carries no signal here", mt.Symbol)
		}
	}

	// The same sentence in ordinary case must still resolve a real ticker,
	// so the guard is about shouting and not about the words.
	normal := "Analysts raised estimates for RTX after the contract award"
	found := false
	for _, mt := range m.ResolveAboveForVenue(normal, 0.85, VenueUS) {
		if mt.Symbol == "RTX" {
			found = true
		}
	}
	if !found {
		t.Error("ordinary-case text should still resolve RTX")
	}
}

// TestAmbiguousUSAbbreviationsAreNotTickers is the third false positive
// that reached the live feed: "Bascom Group Acquires 370-Unit Workforce
// Housing Community in Dallas MSA" resolved to MSA Safety, because MSA is
// both a real ticker and the standard abbreviation for a metropolitan
// statistical area.
func TestAmbiguousUSAbbreviationsAreNotTickers(t *testing.T) {
	m := usMaster(t)
	cases := []string{
		"Bascom Group acquires a 370-unit community in the Dallas MSA",
		"The company said its COO will present the plan",
		"Filings with the IRS showed no change in the structure",
	}
	for _, text := range cases {
		for _, mt := range m.ResolveAboveForVenue(text, 0.85, VenueUS) {
			if mt.Method == MethodTicker {
				t.Errorf("Resolve(%q) matched %s as a ticker -- it is an ordinary abbreviation here", text, mt.Symbol)
			}
		}
	}
}
