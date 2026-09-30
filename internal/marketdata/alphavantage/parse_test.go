package alphavantage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestParseTimeSeries(t *testing.T) {
	tests := []struct {
		name      string
		fixture   string
		seriesKey string
		wantErr   error
		errSubstr string
		check     func(t *testing.T, c []marketdata.Candle)
	}{
		{
			name:      "real IBM daily payload",
			fixture:   "daily_ibm.json",
			seriesKey: "Time Series (Daily)",
			check: func(t *testing.T, c []marketdata.Candle) {
				if len(c) != 100 {
					t.Errorf("got %d candles, want 100 (compact output size)", len(c))
				}
				// Oldest-first ordering is the contract every indicator relies on.
				for i := 1; i < len(c); i++ {
					if !c[i].Time.After(c[i-1].Time) {
						t.Fatalf("candle %d time %v not after %v", i, c[i].Time, c[i-1].Time)
					}
				}
				last := c[len(c)-1]
				if last.Close <= 0 || last.High < last.Low {
					t.Errorf("last candle is incoherent: %+v", last)
				}
				if last.High < last.Close || last.Low > last.Close {
					t.Errorf("close %v outside high/low %v/%v", last.Close, last.High, last.Low)
				}
			},
		},
		{
			name:      "unparseable rows are dropped, not zero-filled",
			fixture:   "daily_dirty.json",
			seriesKey: "Time Series (Daily)",
			check: func(t *testing.T, c []marketdata.Candle) {
				// 4 entries: one has an empty high, one has a bad date key.
				// The empty-volume row survives because volume may be absent.
				if len(c) != 2 {
					t.Fatalf("got %d candles, want 2", len(c))
				}
				for i, cd := range c {
					if cd.Open == 0 || cd.High == 0 || cd.Low == 0 || cd.Close == 0 {
						t.Errorf("candle %d has a zero price: %+v", i, cd)
					}
				}
				if c[0].Volume != 0 {
					t.Errorf("candle with empty volume: got %v, want 0", c[0].Volume)
				}
				if c[1].Close != 10.5 {
					t.Errorf("newest close = %v, want 10.5", c[1].Close)
				}
			},
		},
		{
			name:      "rate-limit Information degrades as budget exhaustion",
			fixture:   "information_ratelimit.json",
			seriesKey: "Time Series (Daily)",
			wantErr:   marketdata.ErrBudgetExhausted,
		},
		{
			name:      "throttle Note degrades as budget exhaustion",
			fixture:   "note_throttle.json",
			seriesKey: "Time Series (Daily)",
			wantErr:   marketdata.ErrBudgetExhausted,
		},
		{
			name:      "Error Message means no data for this symbol",
			fixture:   "error_message.json",
			seriesKey: "Time Series (Daily)",
			wantErr:   marketdata.ErrNoData,
		},
		{
			name:      "missing series block names the keys it did see",
			fixture:   "daily_ibm.json",
			seriesKey: "Time Series (Weekly)",
			errSubstr: "Meta Data",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candles, err := ParseTimeSeries(load(t, tc.fixture), tc.seriesKey)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			case tc.errSubstr != "":
				if err == nil || !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error = %v, want it to mention %q", err, tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, candles)
		})
	}
}

func TestParseTimeSeriesMalformed(t *testing.T) {
	for _, body := range []string{"", "not json at all", "[]", `{"Time Series (Daily)": "a string"}`} {
		c, err := ParseTimeSeries([]byte(body), "Time Series (Daily)")
		if err == nil {
			t.Errorf("body %q: want error, got %d candles", body, len(c))
		}
	}
}

func TestParseGlobalQuote(t *testing.T) {
	ibm := marketdata.MustParseSymbol("IBM")

	t.Run("real payload", func(t *testing.T) {
		q, err := ParseGlobalQuote(load(t, "quote_ibm.json"), ibm)
		if err != nil {
			t.Fatal(err)
		}
		if q.Price != 235.68 {
			t.Errorf("price = %v, want 235.68", q.Price)
		}
		if q.PrevClose != 233.69 {
			t.Errorf("prevClose = %v, want 233.69", q.PrevClose)
		}
		// Change is recomputed from prices, not taken from the pre-formatted
		// "0.8516%" string.
		wantChange := 235.68 - 233.69
		if diff := q.Change - wantChange; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("change = %v, want %v", q.Change, wantChange)
		}
		if q.Currency != "USD" {
			t.Errorf("currency = %q, want USD", q.Currency)
		}
		if q.AsOf.Format("2006-01-02") != "2026-08-21" {
			t.Errorf("asOf = %v, want 2026-08-21", q.AsOf)
		}
	})

	t.Run("rate limit degrades as budget exhaustion", func(t *testing.T) {
		_, err := ParseGlobalQuote(load(t, "information_ratelimit.json"), ibm)
		if !errors.Is(err, marketdata.ErrBudgetExhausted) {
			t.Errorf("error = %v, want ErrBudgetExhausted", err)
		}
	})

	t.Run("empty quote block is no data", func(t *testing.T) {
		_, err := ParseGlobalQuote([]byte(`{"Global Quote": {}}`), ibm)
		if !errors.Is(err, marketdata.ErrNoData) {
			t.Errorf("error = %v, want ErrNoData", err)
		}
	})
}

func TestCandleParams(t *testing.T) {
	rel := marketdata.MustParseSymbol("AAPL")
	tests := []struct {
		name         string
		interval     marketdata.Interval
		limit        int
		wantFunction string
		wantKey      string
		wantSize     string
	}{
		{"daily compact", marketdata.Interval1d, 50, "TIME_SERIES_DAILY", "Time Series (Daily)", "compact"},
		{"daily full when over 100 bars", marketdata.Interval1d, 300, "TIME_SERIES_DAILY", "Time Series (Daily)", "full"},
		{"weekly", marketdata.Interval1wk, 50, "TIME_SERIES_WEEKLY", "Weekly Time Series", ""},
		{"hourly maps to 60min", marketdata.Interval1h, 50, "TIME_SERIES_INTRADAY", "Time Series (60min)", "compact"},
		{"five minute", marketdata.Interval5m, 50, "TIME_SERIES_INTRADAY", "Time Series (5min)", "compact"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, key, err := candleParams(rel, tc.interval, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if got := v.Get("function"); got != tc.wantFunction {
				t.Errorf("function = %q, want %q", got, tc.wantFunction)
			}
			if key != tc.wantKey {
				t.Errorf("series key = %q, want %q", key, tc.wantKey)
			}
			if got := v.Get("outputsize"); got != tc.wantSize {
				t.Errorf("outputsize = %q, want %q", got, tc.wantSize)
			}
			if got := v.Get("symbol"); got != "AAPL" {
				t.Errorf("symbol = %q, want AAPL", got)
			}
		})
	}

	t.Run("unsupported interval", func(t *testing.T) {
		if _, _, err := candleParams(rel, marketdata.Interval("1mo"), 10); !errors.Is(err, marketdata.ErrNotSupported) {
			t.Errorf("error = %v, want ErrNotSupported", err)
		}
	})
}
