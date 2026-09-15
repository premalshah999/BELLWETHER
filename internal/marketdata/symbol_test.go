package marketdata

import (
	"testing"
	"time"
)

func TestParseSymbol(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		want      Symbol
		wantErr   bool
		canonical string
		indian    bool
		currency  string
	}{
		{name: "us plain", in: "AAPL", want: Symbol{"AAPL", ExchangeUS}, canonical: "AAPL", currency: "USD"},
		{name: "us lowercase", in: "aapl", want: Symbol{"AAPL", ExchangeUS}, canonical: "AAPL", currency: "USD"},
		{name: "us padded", in: "  AAPL  ", want: Symbol{"AAPL", ExchangeUS}, canonical: "AAPL", currency: "USD"},
		{name: "bse", in: "RELIANCE.BSE", want: Symbol{"RELIANCE", ExchangeBSE}, canonical: "RELIANCE.BSE", indian: true, currency: "INR"},
		{name: "nse", in: "TCS.NSE", want: Symbol{"TCS", ExchangeNSE}, canonical: "TCS.NSE", indian: true, currency: "INR"},
		{name: "nse lowercase", in: "tcs.nse", want: Symbol{"TCS", ExchangeNSE}, canonical: "TCS.NSE", indian: true, currency: "INR"},
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: "   ", wantErr: true},
		{name: "no ticker", in: ".BSE", wantErr: true},
		{name: "unknown exchange", in: "FOO.LSE", wantErr: true},
		{name: "yahoo dialect rejected", in: "RELIANCE.NS", wantErr: true},
		{name: "class share with dot", in: "BRK.B", want: Symbol{"BRK-B", ExchangeUS}, canonical: "BRK-B", currency: "USD"},
		{name: "class share with dot lowercase", in: "bf.b", want: Symbol{"BF-B", ExchangeUS}, canonical: "BF-B", currency: "USD"},
		{name: "class share already hyphenated", in: "BRK-B", want: Symbol{"BRK-B", ExchangeUS}, canonical: "BRK-B", currency: "USD"},
		{name: "index caret", in: "^GSPC", want: Symbol{"GSPC", ExchangeIndex}, canonical: "GSPC.INDEX", currency: "USD"},
		{name: "index caret lowercase", in: "^vix", want: Symbol{"VIX", ExchangeIndex}, canonical: "VIX.INDEX", currency: "USD"},
		{name: "index canonical form", in: "GSPC.INDEX", want: Symbol{"GSPC", ExchangeIndex}, canonical: "GSPC.INDEX", currency: "USD"},
		{name: "two-letter suffix still rejected", in: "FOO.XX", wantErr: true},
		{name: "empty class suffix rejected", in: "FOO.", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSymbol(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSymbol(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSymbol(%q): unexpected error %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseSymbol(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			if got.String() != tc.canonical {
				t.Errorf("String() = %q, want %q", got.String(), tc.canonical)
			}
			if got.IsIndian() != tc.indian {
				t.Errorf("IsIndian() = %v, want %v", got.IsIndian(), tc.indian)
			}
			if got.Currency() != tc.currency {
				t.Errorf("Currency() = %q, want %q", got.Currency(), tc.currency)
			}
		})
	}
}

func TestParseInterval(t *testing.T) {
	for _, ok := range []string{"1m", "5m", "15m", "1h", "1d", "1wk"} {
		if _, err := ParseInterval(ok); err != nil {
			t.Errorf("ParseInterval(%q): unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"", "2m", "1D", "daily", "1mo"} {
		if _, err := ParseInterval(bad); err == nil {
			t.Errorf("ParseInterval(%q): want error", bad)
		}
	}
}

func TestIntervalIntraday(t *testing.T) {
	intraday := map[Interval]bool{
		Interval1m: true, Interval5m: true, Interval15m: true, Interval1h: true,
		Interval1d: false, Interval1wk: false,
	}
	for iv, want := range intraday {
		if got := iv.Intraday(); got != want {
			t.Errorf("%s.Intraday() = %v, want %v", iv, got, want)
		}
	}
}

func TestSortCandles(t *testing.T) {
	at := func(s int64) time.Time { return time.Unix(s, 0).UTC() }

	t.Run("orders oldest first", func(t *testing.T) {
		got := SortCandles([]Candle{
			{Time: at(300), Close: 3},
			{Time: at(100), Close: 1},
			{Time: at(200), Close: 2},
		})
		want := []float64{1, 2, 3}
		if len(got) != len(want) {
			t.Fatalf("got %d candles, want %d", len(got), len(want))
		}
		for i, w := range want {
			if got[i].Close != w {
				t.Errorf("candle %d close = %v, want %v", i, got[i].Close, w)
			}
		}
	})

	t.Run("last duplicate wins", func(t *testing.T) {
		// Re-polling an in-progress bar must update it, not duplicate it.
		got := SortCandles([]Candle{
			{Time: at(100), Close: 1},
			{Time: at(100), Close: 9},
		})
		if len(got) != 1 {
			t.Fatalf("got %d candles, want 1", len(got))
		}
		if got[0].Close != 9 {
			t.Errorf("close = %v, want 9 (later value wins)", got[0].Close)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := SortCandles(nil); got != nil {
			t.Errorf("SortCandles(nil) = %v, want nil", got)
		}
	})
}

// TestParseSymbolRejectsNonTickers.
//
// The parser previously accepted any non-empty string, so a bulk watchlist
// paste containing a stray line of prose added that line as an instrument and
// the price stream then polled for it every few seconds.
func TestParseSymbolRejectsNonTickers(t *testing.T) {
	// Real listings, including the punctuation Indian tickers actually use.
	for _, good := range []string{
		"RELIANCE.NSE", "TCS.NSE", "AAPL", "M&M.NSE", "M&MFIN.NSE",
		"L&TFH.NSE", "BAJAJ-AUTO.NSE", "NIFTY50.NSE", "3MINDIA.NSE",
	} {
		if _, err := ParseSymbol(good); err != nil {
			t.Errorf("rejected a real ticker %q: %v", good, err)
		}
	}
	for _, bad := range []string{
		"@@@bad@@@", "", "   ", "hello world", "a b", "sym*bol",
		"WAYTOOLONGATICKERNAMEHERE", "123", "!!!", "semi;colon",
	} {
		if sym, err := ParseSymbol(bad); err == nil {
			t.Errorf("accepted %q as a ticker: %+v", bad, sym)
		}
	}
}
