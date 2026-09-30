package stream

import (
	"testing"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// TestUnchangedQuotesAreNotRepublished.
//
// A stream message must mean something happened. Republishing an identical
// price every five seconds trains a reader — and any client logic keyed on
// arrival — to treat messages as noise, which defeats the purpose of pushing
// them at all. It also puts a hundred pointless messages a minute through
// every open connection.
func TestUnchangedQuotesAreNotRepublished(t *testing.T) {
	q := &QuoteSource{}
	sym := marketdata.Symbol{Ticker: "AAPL"}
	q.last = map[string]marketdata.Quote{}

	first := marketdata.Quote{Price: 1298, Volume: 5_735_384}
	if !q.changed(sym.String(), first) {
		t.Error("the first quote for a symbol must always publish")
	}
	if q.changed(sym.String(), first) {
		t.Error("an identical quote must not publish again")
	}

	// A price move publishes.
	moved := first
	moved.Price = 1298.5
	if !q.changed(sym.String(), moved) {
		t.Error("a changed price must publish")
	}

	// So does volume alone: trades happened even if the price did not move,
	// which is exactly the signal the scanner is built around.
	sameLast := moved
	sameLast.Volume += 1000
	if !q.changed(sym.String(), sameLast) {
		t.Error("volume moving without price must publish")
	}
}

// TestLatencyIsReportedHonestly: this source polls, and anything deciding
// whether to trust it needs to know that rather than infer tick resolution
// from the word "stream".
func TestLatencyIsReportedHonestly(t *testing.T) {
	q := &QuoteSource{}
	if got := q.Latency(); got <= 0 {
		t.Errorf("Latency() = %v, want a real interval", got)
	}
	if q.Name() == "" {
		t.Error("a source must name itself for logs and health output")
	}
}

// TestSymbolsReadEveryCycle: adding an instrument to the watchlist must start
// streaming it without restarting the process.
func TestSymbolsReadEveryCycle(t *testing.T) {
	calls := 0
	q := &QuoteSource{Symbols: func() []marketdata.Symbol {
		calls++
		return nil
	}}
	q.watched()
	q.watched()
	if calls != 2 {
		t.Errorf("Symbols called %d times, want 2 — the list must not be cached", calls)
	}

	// A source with no symbol function must not panic.
	empty := &QuoteSource{}
	if got := empty.watched(); got != nil {
		t.Errorf("watched() = %v with no Symbols func, want nil", got)
	}
}
