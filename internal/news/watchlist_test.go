package news

import (
	"strings"
	"testing"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// nse is shorthand for the NSE symbol these tests build watched sources for.
func nse(ticker string) marketdata.Symbol {
	return marketdata.Symbol{Ticker: ticker, Exchange: marketdata.ExchangeNSE}
}

// TestWatchlistSourceQuery covers the query a watched instrument gets.
//
// A bare ticker is a poor search — "TCS" reaches a great deal that is not Tata
// Consultancy — so the registered name leads where one is known.
func TestWatchlistSourceQuery(t *testing.T) {
	withName := WatchlistSource(nse("RELIANCE"), "Reliance Industries Limited")
	if !strings.Contains(withName.URL, "Reliance+Industries") {
		t.Errorf("query should lead with the company name: %s", withName.URL)
	}
	// The legal suffix is noise: publishers write "Reliance Industries".
	if strings.Contains(withName.URL, "Limited") {
		t.Errorf("the legal suffix should be trimmed from the query: %s", withName.URL)
	}

	// The bare ticker must not appear as an alternative: "Reliance" reaches
	// "self-reliance", and every such result would then be attributed to the
	// company because the query named it.
	if strings.Contains(withName.URL, "OR") {
		t.Errorf("query should not offer the bare ticker as an alternative: %s", withName.URL)
	}

	bare := WatchlistSource(nse("XYZ"), "")
	if !strings.Contains(bare.URL, "XYZ") {
		t.Errorf("a nameless instrument must still search its ticker: %s", bare.URL)
	}
}

// TestWatchlistSourceCarriesItsSymbol is what makes heat escalation possible:
// the scheduler can only poll a source harder for an eventful company if the
// source says which company it follows.
func TestWatchlistSourceCarriesItsSymbol(t *testing.T) {
	s := WatchlistSource(nse("SUZLON"), "Suzlon Energy Limited")
	if len(s.Symbols) != 1 || s.Symbols[0] != "SUZLON" {
		t.Errorf("Symbols = %v, want [SUZLON]", s.Symbols)
	}
	if s.Lane() != LaneFast {
		t.Errorf("lane = %s, want fast: a watched position is where being late matters", s.Lane())
	}
	if s.Usage != UsageDiscoveryOnly {
		t.Errorf("usage = %s, want discovery_only — same terms as the rest of that layer", s.Usage)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("generated source is invalid: %v", err)
	}
}

func TestWatchlistSourcesDeduplicate(t *testing.T) {
	// The same company held on two exchanges is one company as far as news
	// is concerned; polling twice would double the traffic to learn the same
	// thing.
	got := WatchlistSources([]Watched{
		{Ticker: "RELIANCE", Venue: marketdata.ExchangeNSE, Company: "Reliance Industries Limited"},
		{Ticker: "reliance", Venue: marketdata.ExchangeNSE, Company: "Reliance Industries Limited"},
		{Ticker: "TCS", Venue: marketdata.ExchangeNSE},
		{Ticker: "", Venue: marketdata.ExchangeNSE},
	})
	if len(got) != 2 {
		t.Fatalf("got %d sources, want 2", len(got))
	}
	ids := map[string]bool{}
	for _, s := range got {
		if ids[s.ID] {
			t.Errorf("duplicate source id %s", s.ID)
		}
		ids[s.ID] = true
	}
}

// TestRegistryReplaceSwapsOnlyItsCategory is the guarantee that makes the
// registry safe to rebuild while the scheduler is reading it: a watchlist
// change must never drop a catalogue feed.
func TestRegistryReplaceSwapsOnlyItsCategory(t *testing.T) {
	reg, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	before := reg.Len()

	added, removed, err := reg.Replace("watchlist",
		WatchlistSources([]Watched{{Ticker: "AAPL", Venue: marketdata.ExchangeUS}, {Ticker: "MSFT", Venue: marketdata.ExchangeUS}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 || len(removed) != 0 {
		t.Errorf("added %v removed %v, want two added", added, removed)
	}
	if reg.Len() != before+2 {
		t.Errorf("registry has %d sources, want %d", reg.Len(), before+2)
	}
	if _, ok := reg.Get("fed-press"); !ok {
		t.Fatal("replacing the watchlist dropped a catalogue feed")
	}

	// Shrinking the watchlist removes only what left it.
	added, removed, err = reg.Replace("watchlist", WatchlistSources([]Watched{{Ticker: "MSFT", Venue: marketdata.ExchangeUS}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 || len(removed) != 1 || removed[0] != "watch-aapl" {
		t.Errorf("added %v removed %v, want watch-aapl removed", added, removed)
	}
	if _, ok := reg.Get("watch-msft"); !ok {
		t.Error("watch-msft should have survived")
	}
	if _, ok := reg.Get("watch-aapl"); ok {
		t.Error("watch-aapl should have been removed")
	}
	if _, ok := reg.Get("fed-press"); !ok {
		t.Fatal("the curated catalog must survive every rebuild")
	}
}

// TestRegistryReplaceRejectsWrongCategory guards against a caller handing in
// sources that would then be invisible to the next rebuild.
func TestRegistryReplaceRejectsWrongCategory(t *testing.T) {
	reg, _ := DefaultRegistry()
	bad := WatchlistSource(nse("RELIANCE"), "Reliance Industries")
	bad.Category = "markets"
	if _, _, err := reg.Replace("watchlist", []Source{bad}); err == nil {
		t.Error("expected a category mismatch to be rejected")
	}
}

// Every per-instrument query carries a window.
//
// Without one, Google News ranks by relevance rather than recency and returns
// whatever it likes: a bare "Reliance Industries" measured 81 items, 32 of
// them older than a week and the oldest 1,541 days. Those arrive on every
// poll, about the companies the operator cares most about.
func TestWatchlistQueriesAreWindowed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		src     Source
		want    string
		notWant string
	}{
		{"followed", WatchlistSource(nse("RELIANCE"), "Reliance Industries Limited"), "when%3A3d", "when%3A1d"},
		{"no company name", WatchlistSource(nse("XYZ"), ""), "when%3A3d", ""},
		{
			"flagged by the scanner",
			WatchlistSources([]Watched{{Ticker: "SUZLON", Venue: marketdata.ExchangeNSE, Company: "Suzlon Energy Limited", Attention: true}})[0],
			"when%3A1d", "when%3A3d",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.src.URL, tc.want) {
				t.Errorf("query has no %s window:\n  %s", tc.want, tc.src.URL)
			}
			if tc.notWant != "" && strings.Contains(tc.src.URL, tc.notWant) {
				t.Errorf("query carries %s, which belongs to the other intent:\n  %s", tc.notWant, tc.src.URL)
			}
		})
	}
}

// An instrument the scanner flagged is polled harder than one merely being
// followed: the move already happened, and the explanation is arriving now.
func TestFlaggedInstrumentsArePolledHarder(t *testing.T) {
	followed := WatchlistSources([]Watched{{Ticker: "TCS", Venue: marketdata.ExchangeNSE, Company: "Tata Consultancy Services"}})[0]
	flagged := WatchlistSources([]Watched{{Ticker: "TCS", Venue: marketdata.ExchangeNSE, Company: "Tata Consultancy Services", Attention: true}})[0]
	if flagged.Refresh >= followed.Refresh {
		t.Errorf("flagged refresh %s is not faster than followed %s", flagged.Refresh, followed.Refresh)
	}
	if !flagged.Attention() || followed.Attention() {
		t.Error("Attention() does not distinguish the two")
	}
}

// A ticker that is both watched and flagged must produce one source, and it
// must be the flagged one — the tighter query is the more useful of the two,
// and whoever is following it still gets the news.
func TestFlagWinsOverFollowForTheSameTicker(t *testing.T) {
	got := WatchlistSources([]Watched{
		{Ticker: "SUZLON", Venue: marketdata.ExchangeNSE, Company: "Suzlon Energy"},
		{Ticker: "SUZLON", Venue: marketdata.ExchangeNSE, Company: "Suzlon Energy", Attention: true},
	})
	if len(got) != 1 {
		t.Fatalf("got %d sources, want 1", len(got))
	}
	if !got[0].Attention() {
		t.Error("the follow entry won; the scanner's tighter query was dropped")
	}
}
