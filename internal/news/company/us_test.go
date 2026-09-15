package company

import "testing"

func TestLoadEmbeddedUS(t *testing.T) {
	tickers, listings, err := LoadEmbeddedUS()
	if err != nil {
		t.Fatal(err)
	}
	if len(tickers) < 10000 {
		t.Errorf("tickers = %d, want at least 10000 (the full SEC exchange-listed universe)", len(tickers))
	}
	if len(listings) < 400 {
		t.Errorf("listings = %d, want at least 400 (the S&P 500 scan universe)", len(listings))
	}

	byTicker := make(map[string]USTicker, len(tickers))
	for _, tk := range tickers {
		byTicker[tk.Symbol] = tk
	}
	for _, want := range []string{"AAPL", "MSFT", "AMZN", "NVDA", "INFY", "HSBC", "BRK-B"} {
		if _, ok := byTicker[want]; !ok {
			t.Errorf("expected %q in the US ticker reference", want)
		}
	}

	byListing := make(map[string]USListing, len(listings))
	for _, l := range listings {
		byListing[l.Symbol] = l
	}
	infy, ok := byListing["INFY"]
	if !ok {
		t.Fatal("INFY must be in the scan universe: it is the documented US/NSE ticker collision")
	}
	if infy.Exchange != "NYSE" {
		t.Errorf("INFY exchange = %q, want NYSE", infy.Exchange)
	}
	for _, l := range listings {
		if l.Sector == "" {
			t.Errorf("%s has no sector", l.Symbol)
		}
		if l.CIK == "" {
			t.Errorf("%s has no CIK", l.Symbol)
		}
	}
}
