package company

import (
	"strings"
	"testing"
)

func testMaster(t *testing.T) *Master {
	t.Helper()
	m, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return m
}

// symbols flattens a match list for comparison.
func symbols(ms []Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Symbol)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestResolveFindsCompanies(t *testing.T) {
	m := testMaster(t)
	cases := []struct {
		name   string
		text   string
		want   string
		method Method
	}{
		{"full legal name", "Reliance Industries Limited reports record refining margin", "RELIANCE", MethodLegalName},
		{"legal name without suffix", "Reliance Industries to invest in new petrochemical capacity", "RELIANCE", MethodLegalName},
		{"house abbreviation", "RIL board approves demerger of financial services arm", "RELIANCE", MethodAlias},
		{"ampersand written as word", "Larsen and Toubro bags metro contract", "LT", MethodLegalName},
		{"ampersand written as symbol", "Larsen & Toubro bags metro contract", "LT", MethodLegalName},
		{"renamed issuer keeps old brand", "Zomato shares rally on food delivery growth", "ETERNAL", MethodAlias},
		{"brand differs from legal name", "Paytm narrows loss in the September quarter", "PAYTM", MethodAlias},
		{"acronym PSU", "SAIL raises steel output guidance for FY27", "SAIL", MethodAlias},
		{"uppercase ticker", "TCS wins a $1 billion deal in North America", "TCS", MethodAlias},
		{"explicit NSE notation", "Buy call on NSE: HINDALCO after the results", "HINDALCO", MethodExplicit},
		{"explicit dotted suffix", "RELIANCE.NS closed 2% higher", "RELIANCE", MethodExplicit},
		{"punctuation in name", "Dr. Reddy's Laboratories gets USFDA clearance", "DRREDDY", MethodLegalName},
		{"single word name", "Infosys raises its revenue guidance", "INFY", MethodLegalName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := m.Resolve(tc.text)
			if !contains(symbols(got), tc.want) {
				t.Fatalf("Resolve(%q) = %v, want it to contain %s", tc.text, symbols(got), tc.want)
			}
			for _, mt := range got {
				if mt.Symbol == tc.want && mt.Method != tc.method {
					t.Errorf("method for %s = %s, want %s", tc.want, mt.Method, tc.method)
				}
			}
		})
	}
}

// TestResolveRejectsFalsePositives is the half of this suite that matters
// most. Attributing a story to a company that it does not concern is the
// failure mode that reaches an operator's screen looking like fact.
func TestResolveRejectsFalsePositives(t *testing.T) {
	m := testMaster(t)
	cases := []struct {
		name string
		text string
		deny string
	}{
		{"ambiguous house name", "The Tata group is restructuring its holdings", "TCS"},
		{"english word as ticker, lowercase", "The winds fail to sail the fleet home", "SAIL"},
		{"english word as ticker, uppercase but blocklisted", "The IDEA was rejected by the board", "IDEA"},
		{"blocklisted common word ticker", "Management retains TOTAL control of the unit", "TOTAL"},
		{"blocklisted single-token name", "The delta between the two estimates widened", "DELTACORP"},
		{"two letter ticker is not evidence", "LT and other terms were agreed", "LT"},
		{"lowercase company acronym in prose", "the gail force winds hit the coast", "GAIL"},
		{"substring must not match", "Reliance on imported crude has fallen", "RELIANCE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := symbols(m.Resolve(tc.text)); contains(got, tc.deny) {
				t.Fatalf("Resolve(%q) = %v, must NOT contain %s", tc.text, got, tc.deny)
			}
		})
	}
}

func TestResolveMultipleCompanies(t *testing.T) {
	m := testMaster(t)
	text := "Infosys and Wipro both lost ground while HDFC Bank gained"
	got := symbols(m.Resolve(text))
	for _, want := range []string{"INFY", "WIPRO", "HDFCBANK"} {
		if !contains(got, want) {
			t.Errorf("Resolve(%q) = %v, missing %s", text, got, want)
		}
	}
}

// TestResolveLongestMatchWins guards the non-overlap rule: once a span is
// consumed by a longer name, its prefix must not also produce a match.
func TestResolveLongestMatchWins(t *testing.T) {
	m := testMaster(t)
	got := m.Resolve("Tata Consultancy Services Limited announced a buyback")
	if !contains(symbols(got), "TCS") {
		t.Fatalf("expected TCS, got %v", symbols(got))
	}
	for _, mt := range got {
		if strings.Contains(mt.Matched, "tata") && mt.Symbol != "TCS" {
			t.Errorf("overlapping match %s via %q should have been consumed by TCS", mt.Symbol, mt.Matched)
		}
	}
}

func TestResolveConfidenceOrdering(t *testing.T) {
	m := testMaster(t)
	got := m.Resolve("NSE: INFY and Wipro rose")
	if len(got) < 2 {
		t.Fatalf("expected at least two matches, got %v", symbols(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Confidence < got[i].Confidence {
			t.Fatalf("matches not sorted by confidence: %v", got)
		}
	}
	if got[0].Symbol != "INFY" || got[0].Method != MethodExplicit {
		t.Errorf("explicit notation should rank first, got %+v", got[0])
	}
}

func TestResolveEmptyInput(t *testing.T) {
	m := testMaster(t)
	for _, in := range []string{"", "   ", "\n\t"} {
		if got := m.Resolve(in); len(got) != 0 {
			t.Errorf("Resolve(%q) = %v, want none", in, got)
		}
	}
}

func TestResolveAboveFiltersByConfidence(t *testing.T) {
	m := testMaster(t)
	text := "Infosys gained today"
	if len(m.ResolveAbove(text, 0.5)) == 0 {
		t.Fatal("expected a match at a low floor")
	}
	if got := m.ResolveAbove(text, 0.95); len(got) != 0 {
		t.Errorf("single-word name should not clear a 0.95 floor, got %v", symbols(got))
	}
}

func TestDisambiguateDVR(t *testing.T) {
	m := testMaster(t)
	// Jain Irrigation lists an ordinary and a differential-voting-rights
	// class under one name; the ordinary class is the right answer.
	got := symbols(m.Resolve("Jain Irrigation Systems reported higher exports"))
	if !contains(got, "JISLJALEQS") {
		t.Fatalf("expected ordinary class JISLJALEQS, got %v", got)
	}
	if contains(got, "JISLDVREQS") {
		t.Errorf("DVR class must not be returned alongside the ordinary one: %v", got)
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Reliance Industries Limited", "reliance industries"},
		{"The Indian Hotels Company Limited", "indian hotels"},
		{"Mahindra & Mahindra Limited", "mahindra and mahindra"},
		{"Dr. Reddy's Laboratories Ltd.", "dr reddy laboratories"}, // possessive dropped on both sides
		{"Some Firm Private Limited", "some firm"},
		{"  Extra   Spaces  Limited ", "extra spaces"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTickerTokensPreservesCase(t *testing.T) {
	got := tickerTokens("TCS and Infosys beat M&M estimates")
	want := map[string]bool{"TCS": true, "I": true, "M&M": true}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected ticker token %q in %v", g, got)
		}
	}
	if !contains(got, "M&M") {
		t.Errorf("ampersand ticker lost: %v", got)
	}
}

// TestAliasesPointAtListedSymbols keeps the curated table honest. A symbol
// that has been delisted or renamed leaves an alias pointing at nothing, and
// that should surface here rather than as silently missing coverage.
func TestAliasesPointAtListedSymbols(t *testing.T) {
	m := testMaster(t)
	var dangling []string
	for alias, sym := range brandAliases {
		if _, ok := m.Lookup(sym); !ok {
			dangling = append(dangling, alias+" -> "+sym)
		}
	}
	if len(dangling) > 0 {
		t.Errorf("aliases pointing at unlisted symbols:\n  %s", strings.Join(dangling, "\n  "))
	}
}

func TestMasterLoadsFullUniverse(t *testing.T) {
	m := testMaster(t)
	if m.Len() < 2000 {
		t.Fatalf("master has %d companies, expected the full NSE list", m.Len())
	}
	c, ok := m.Lookup("reliance") // lookup is case-insensitive
	if !ok || c.Symbol != "RELIANCE" {
		t.Fatalf("Lookup(reliance) = %+v, %v", c, ok)
	}
	if c.ISIN == "" {
		t.Error("expected an ISIN on the master record")
	}
}

// TestLeadWordsAvoidCommonEnglish guards the lexicon that keeps ordinary words
// from standing in for companies. Without it, Solar Industries claims every
// headline containing "Solar" and Network People claims every "Network".
func TestLeadWordsAvoidCommonEnglish(t *testing.T) {
	m := testMaster(t)
	if len(m.leadPhrase) < 500 {
		t.Fatalf("only %d lead words indexed; the lexicon is over-filtering", len(m.leadPhrase))
	}
	for word := range m.leadPhrase {
		if commonWords[word] {
			t.Errorf("lead word %q is ordinary English and must not identify %v", word, m.byPhrase[word])
		}
		if len(word) < 5 {
			t.Errorf("lead word %q is too short to be distinctive", word)
		}
	}
}

// TestLeadWordResolution covers the case the lexicon exists to make safe: a
// distinctive first word standing in for the full registered name.
func TestLeadWordResolution(t *testing.T) {
	m := testMaster(t)
	got := m.Resolve("Suzlon commissions a new wind project in Gujarat")
	if !contains(symbols(got), "SUZLON") {
		t.Fatalf("expected SUZLON from a lead word, got %v", symbols(got))
	}
	for _, mt := range got {
		if mt.Symbol == "SUZLON" {
			if mt.Method != MethodLeadWord {
				t.Errorf("method = %s, want %s", mt.Method, MethodLeadWord)
			}
			// A lead is explicitly not strong enough to fire an alert.
			if mt.Confidence >= confAlias {
				t.Errorf("lead confidence %.2f should rank below an alias match", mt.Confidence)
			}
		}
	}
	// The same mechanism must not fire on ordinary prose.
	for _, prose := range []string{
		"Solar capacity additions slowed in the June quarter",
		"Network effects favour the incumbent platform",
		"General insurance premiums rose 12% year on year",
	} {
		for _, mt := range m.Resolve(prose) {
			if mt.Method == MethodLeadWord {
				t.Errorf("Resolve(%q) produced lead-word match %s via %q", prose, mt.Symbol, mt.Matched)
			}
		}
	}
}

// TestPossessiveNormalization covers "L&T's", where deleting only the
// apostrophe would weld the possessive onto the name and lose the match.
func TestPossessiveNormalization(t *testing.T) {
	if got, want := Normalize("L&T's orderbook"), "l and t orderbook"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
	m := testMaster(t)
	if got := symbols(m.Resolve("L&T's West Asia orderbook swells")); !contains(got, "LT") {
		t.Errorf("expected LT, got %v", got)
	}
}

// TestIndustriesMentioned covers the mapping from how people speak to how NSE
// classifies. Nobody searches for "Construction Materials"; they search for
// cement.
func TestIndustriesMentioned(t *testing.T) {
	m := testMaster(t)
	cases := []struct {
		question string
		want     string
	}{
		{"Which Indian cement companies are exposed to the coal price?", "Construction Materials"},
		{"How are private banks placed after the RBI decision?", "Financial Services"},
		{"Which pharma companies have USFDA exposure?", "Healthcare"},
		{"What is happening in the auto sector?", "Automobile and Auto Components"},
		{"Steel makers after the tariff decision", "Metals & Mining"},
	}
	for _, tc := range cases {
		got := m.IndustriesMentioned(tc.question)
		found := false
		for _, g := range got {
			if g == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("IndustriesMentioned(%q) = %v, want it to include %q", tc.question, got, tc.want)
		}
	}

	// A question naming no industry must enumerate nothing. Returning a
	// hundred companies against a question that named none is worse than
	// returning none.
	for _, q := range []string{
		"What did Reliance announce today?",
		"How did the market close?",
	} {
		if got := m.IndustriesMentioned(q); len(got) > 0 {
			t.Errorf("IndustriesMentioned(%q) = %v, want none", q, got)
		}
	}

	// Word boundaries: "it" is an industry alias and must not fire inside
	// other words.
	if got := m.IndustriesMentioned("What is the outlook with respect to profits?"); len(got) > 0 {
		t.Errorf("matched %v on a question containing no industry", got)
	}
}

func TestSymbolsInIndustry(t *testing.T) {
	m := testMaster(t)
	cement := m.SymbolsInIndustry("Construction Materials")
	if len(cement) < 5 {
		t.Fatalf("Construction Materials has %d symbols, expected the listed cement universe", len(cement))
	}
	// The obvious names must be there, or the mapping is not usable.
	want := map[string]bool{"ULTRACEMCO": false, "AMBUJACEM": false, "SHREECEM": false}
	for _, s := range cement {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for sym, found := range want {
		if !found {
			t.Errorf("%s missing from Construction Materials: %v", sym, cement)
		}
	}
}
