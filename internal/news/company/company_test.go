package company

import (
	"slices"
	"sync"
	"testing"
)

var loadOnce = sync.OnceValues(Load)

func master(t *testing.T) *Master {
	t.Helper()
	m, err := loadOnce()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return m
}

func symbols(ms []Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Symbol)
	}
	return out
}

func TestLoadEmbeddedUS(t *testing.T) {
	tickers, listings, err := LoadEmbeddedUS()
	if err != nil {
		t.Fatal(err)
	}
	if len(tickers) < 10000 {
		t.Errorf("tickers = %d, want the full SEC universe (10,000+)", len(tickers))
	}
	// The scan universe is the S&P 1500: the default feed admits an event
	// only when one of its companies is a member, so narrowing it to the
	// S&P 500 hid 71% of correctly resolved US news.
	if len(listings) < 1400 {
		t.Errorf("listings = %d, want the S&P 1500", len(listings))
	}
	for _, l := range listings {
		if l.Sector == "" || l.CIK == "" {
			t.Errorf("%s is missing its sector or CIK", l.Symbol)
		}
	}
	if !IsUSTicker("brk-b") || IsUSTicker("ZZZZZ") {
		t.Error("IsUSTicker should know BRK-B and not an unlisted ticker")
	}
}

func TestResolvesByName(t *testing.T) {
	m := master(t)
	for text, want := range map[string]string{
		"Apple Inc. beat expectations on iPhone revenue":              "AAPL",
		"NVIDIA Corporation guided higher for the data centre":        "NVDA",
		"Shares of Microsoft Corporation rose after the announcement": "MSFT",
		"Visa reported record quarterly earnings":                     "V",
		"Apple unveils the Vision Pro headset":                        "AAPL",
		// SEC's "/DE/" and "& Co" forms must not stop a legal-name match.
		"Bank of America raised its dividend":  "BAC",
		"JPMorgan Chase posted record revenue": "JPM",
		"Wells Fargo settles with regulators":  "WFC",
		"Eli Lilly wins approval for its drug": "LLY",
		// Share classes are one issuer: the name resolves to the primary.
		"Alphabet shares rallied after earnings":    "GOOGL",
		"Berkshire Hathaway bought more Occidental": "BRK-B",
		// Curated press names.
		"Google faces a new antitrust suit": "GOOGL",
		"McDonald's raised menu prices":     "MCD",
	} {
		if got := symbols(m.ResolveAbove(text, 0.9)); !slices.Contains(got, want) {
			t.Errorf("%q -> %v, want %s among them", text, got, want)
		}
	}
}

// "NVIDIA (NASDAQ:NVDA)" once resolved NDAQ too: the exchange is not a
// company, but the ticker after it is an explicit identification.
func TestExchangePrefixIsNotACompany(t *testing.T) {
	m := master(t)
	for _, text := range []string{
		"NVIDIA (NASDAQ:NVDA) Shares Up 1.3% - Still a Buy?",
		"Coca-Cola (NYSE: KO) declares quarterly dividend",
	} {
		got := symbols(m.ResolveAbove(text, 0.85))
		if slices.Contains(got, "NDAQ") || slices.Contains(got, "ICE") || len(got) == 0 {
			t.Errorf("%q -> %v", text, got)
		}
	}
}

// An all-caps press release makes every word look like a ticker ("...SETS
// THE STAGE FOR ITS FOURTH..." matched FOR); the same words in ordinary case
// still resolve a real ticker.
func TestShoutedTextYieldsNoBareTickers(t *testing.T) {
	m := master(t)
	for _, mt := range m.ResolveAbove("PROJECT HEALTHY MINDS SETS THE STAGE FOR ITS FOURTH ANNUAL WORLD MENTAL HEALTH DAY", 0.85) {
		if mt.Method == MethodTicker {
			t.Errorf("shouted text produced bare ticker %s", mt.Symbol)
		}
	}
	if got := symbols(m.ResolveAbove("Analysts raised estimates for RTX after the award", 0.85)); !slices.Contains(got, "RTX") {
		t.Errorf("ordinary text lost RTX: %v", got)
	}
}

// Ordinary words and abbreviations that happen to be tickers or one-word
// company names must not resolve.
func TestOrdinaryWordsAreNotCompanies(t *testing.T) {
	m := master(t)
	for _, text := range []string{
		"Bascom Group acquires a 370-unit community in the Dallas MSA",
		"The company said its COO will present the plan",
		"Filings with the IRS showed no change",
		"The delta between the two estimates narrowed",
		"an apple a day, and the winds fail to sail",
	} {
		for _, mt := range m.ResolveAbove(text, 0.85) {
			t.Errorf("%q resolved %s via %s (%q)", text, mt.Symbol, mt.Method, mt.Matched)
		}
	}
}

// A registrant whose whole name is an ordinary word, a place, a person or a
// market term must not resolve on that word. Each of these was found tagged in
// the archive: every market wrap was a story about Dow Inc.
func TestMarketTermsAreNotCompanies(t *testing.T) {
	m := master(t)
	for text, wrong := range map[string]string{
		"Stock Market Today: Dow Rises On Surprise Inflation Data":    "DOW",
		"These Nasdaq 100 Stocks Are Quietly Paying Real Dividends":   "NDAQ",
		"Robinhood Unveils Crypto Futures Trading":                    "CRCW",
		"Robinhood Stock Draws Bullish Targets":                       "BLSH",
		"Groundfish Reserves in the Bering Sea":                       "SE",
		"Citizens reiterates Immix Biopharma stock rating":            "CIA",
		"The Gap Between the Rich and the Very Rich Is Getting Wider": "GAP",
		"BioStem Technologies prices $3M private placement":           "MMM",
		"MXL Stock Set To Snap Four-Day Winning Streak":               "SNAP",
		"Farmers & Merchants Bancorp raises quarterly dividend":       "TBBK",
		"OpenAI Unveils Always-On AI Agent, New $500 Paid Tier":       "PAYD",
		"Powell says the Fed is in no hurry to cut rates":             "POWL",
		"Commerce Department adds chip firms to entity list":          "CBSH",
		"Under pressure, regional banks raise capital":                "UAA",
		"Salt Lake County approves tax increase":                      "LAKE",
	} {
		if got := symbols(m.Resolve(text)); slices.Contains(got, wrong) {
			t.Errorf("%q resolved %s (all: %v)", text, wrong, got)
		}
	}
	// The same companies still resolve by the name the press prints.
	for text, want := range map[string]string{
		"3M raises its full-year forecast":                "MMM",
		"Dow Inc. to close three plants in Europe":        "DOW",
		"Target Corp cuts its full-year forecast":         "TGT",
		"Gap Inc. names a new chief executive":            "GAP",
		"Philip Morris raises guidance on Zyn demand":     "PM",
		"Raymond James upgrades the chipmaker":            "RJF",
		"Martin Marietta to buy quarry assets":            "MLM",
		"Super Micro shares plunge after auditor resigns": "SMCI",
		"Alaska Airlines grounds fleet after IT outage":   "ALK",
		"Under Armour cuts its outlook":                   "UAA",
		"Goldman cuts its S&P 500 target":                 "GS",
	} {
		if got := symbols(m.ResolveAbove(text, 0.85)); !slices.Contains(got, want) {
			t.Errorf("%q -> %v, want %s among them", text, got, want)
		}
	}
}

func TestLookupAndSectors(t *testing.T) {
	m := master(t)
	if c, ok := m.Lookup(" aapl "); !ok || c.Symbol != "AAPL" {
		t.Errorf("Lookup(aapl) = %+v, %v", c, ok)
	}
	if s, ok := m.Sector("JPM"); !ok || s != "Financials" {
		t.Errorf("Sector(JPM) = %q, %v", s, ok)
	}
	if got := m.SectorsMentioned("Which banks are cheapest after the stress test?"); !slices.Equal(got, []string{"Financials"}) {
		t.Errorf("SectorsMentioned = %v, want [Financials]", got)
	}
	if got := m.SectorsMentioned("How did it do with guidance?"); len(got) != 0 {
		t.Errorf("SectorsMentioned matched a word inside another: %v", got)
	}
	banks := m.SymbolsInSector("Financials")
	if len(banks) < 50 || !slices.Contains(banks[:10], "JPM") {
		t.Errorf("SymbolsInSector(Financials) should list the largest first, got %v", banks[:min(10, len(banks))])
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"BANK OF AMERICA CORP /DE/":  "bank of america",
		"COSTCO WHOLESALE CORP /NEW": "costco wholesale",
		"JPMORGAN CHASE & CO":        "jpmorgan chase",
		"The Walt Disney Company":    "walt disney",
		"McDonald's Corp.":           "mcdonald",
		"AT&T INC.":                  "at and t",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
