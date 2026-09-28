package company

import (
	"slices"
	"testing"
)

func usMaster(t *testing.T) *Master {
	t.Helper()
	m, err := LoadEmbeddedUSMaster()
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

// The resolver no longer holds NSE listings, and this is what that costs.
//
// A one-word US legal name matches a headline about a foreign company that
// merely starts with the same word. Measured against the retained NSE listing
// file, 27 one-word US names are the first word of an Indian company name, and
// 11 of those produce a confident match on a genuinely unrelated company --
// the rest are either caught by the common-word lexicon or are Indian
// subsidiaries of the US parent, where naming the parent is defensible.
//
// This is pinned rather than fixed, deliberately. Every structural guard tried
// was worse than the disease:
//
//   - Dropping a one-word match followed by another capitalised word loses
//     "Apple Vision Pro" and "Tesla Model Y", which are common; these
//     headlines are not, now that no Indian source is polled.
//   - Keying on a foreign legal form does not work either: 11.9% of the US
//     registrants in SEC's own exchange file carry "Limited" or "Ltd",
//     because that file includes foreign ADRs -- Alibaba, BHP, Chubb, and
//     HDB and IBN, which are the Indian bank ADRs.
//   - Blocklisting the phrases would lose the real company: "visa", "hp" and
//     "reliance" are the registered names of Visa, HP and Reliance Inc.
//
// If this is ever worth fixing, the fix is a curated parent/subsidiary table,
// not a heuristic. Until then it is a known, bounded, measured limitation, and
// this test fails loudly if the shape of it changes.
func TestForeignNameCollisionIsAKnownLimitation(t *testing.T) {
	m := usMaster(t)

	for _, tc := range []struct {
		text, wrongly string
	}{
		{"Reliance Industries Limited announced a demerger", "RS"},
		{"VISA Chrome Limited reports a loss", "V"},
		{"HP Adhesives Limited lists on the NSE", "HPQ"},
	} {
		var got []string
		for _, mt := range m.ResolveAbove(tc.text, 0.9) {
			got = append(got, mt.CanonicalSymbol())
		}
		if !slices.Contains(got, tc.wrongly) {
			t.Errorf("%q no longer resolves to %s (got %v).\n"+
				"If that was deliberate, good -- update this test and say what fixed it.",
				tc.text, tc.wrongly, got)
		}
	}

	// The other half of the measurement: generic one-word names are already
	// held out by the common-word lexicon, and must stay that way.
	for _, text := range []string{
		"Eastern Silk Industries Limited restructures",
		"Popular Vehicles and Services Limited files for an IPO",
		"Team India Guaranty Limited appoints a CEO",
	} {
		if got := m.ResolveAbove(text, 0.9); len(got) != 0 {
			t.Errorf("%q resolved to %v; the common-word lexicon should hold these out", text, got)
		}
	}
}

// A real US company still resolves from an ordinary headline -- the control
// for the test above, and the reason none of those guards were added.
func TestOrdinaryUSHeadlinesStillResolve(t *testing.T) {
	m := usMaster(t)
	for text, want := range map[string]string{
		"Visa reported record quarterly earnings": "V",
		"Apple unveils the Vision Pro headset":    "AAPL",
		"Nvidia lifts its data centre guidance":   "NVDA",
	} {
		var got []string
		for _, mt := range m.ResolveAbove(text, 0.9) {
			got = append(got, mt.CanonicalSymbol())
		}
		if !slices.Contains(got, want) {
			t.Errorf("%q -> %v, want %s among them", text, got, want)
		}
	}
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
