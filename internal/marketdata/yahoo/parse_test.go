package yahoo

import (
	"errors"
	"os"
	"path/filepath"
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

func TestParseChart(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantCandles int
		wantErr     error
		errContains string
		check       func(t *testing.T, candles []marketdata.Candle, meta ChartMeta)
	}{
		{
			name:        "aapl daily skips the null holiday bar",
			fixture:     "chart_aapl_1d.json",
			wantCandles: 4, // 5 timestamps, one all-null bar dropped
			check: func(t *testing.T, c []marketdata.Candle, m ChartMeta) {
				if m.Currency != "USD" {
					t.Errorf("currency = %q, want USD", m.Currency)
				}
				if !m.HasPrice || m.Price != 226.79 {
					t.Errorf("price = %v (has=%v), want 226.79", m.Price, m.HasPrice)
				}
				if m.PrevClose != 220.15 {
					t.Errorf("prevClose = %v, want 220.15 (chartPreviousClose preferred)", m.PrevClose)
				}
				// A dropped null bar must not leave a zero-priced candle.
				for i, cd := range c {
					if cd.Open == 0 || cd.Close == 0 {
						t.Errorf("candle %d has a zero price: %+v", i, cd)
					}
				}
				last := c[len(c)-1]
				if last.Close != 226.79 {
					t.Errorf("last close = %v, want 226.79", last.Close)
				}
				// Bars must be strictly increasing in time.
				for i := 1; i < len(c); i++ {
					if !c[i].Time.After(c[i-1].Time) {
						t.Errorf("candle %d time %v not after %v", i, c[i].Time, c[i-1].Time)
					}
				}
			},
		},
		{
			name:        "reliance daily parses INR series",
			fixture:     "chart_reliance_1d.json",
			wantCandles: 5,
			check: func(t *testing.T, c []marketdata.Candle, m ChartMeta) {
				if m.Currency != "INR" {
					t.Errorf("currency = %q, want INR", m.Currency)
				}
				if c[len(c)-1].Close != 2431.4 {
					t.Errorf("last close = %v, want 2431.4", c[len(c)-1].Close)
				}
			},
		},
		{
			name:        "upstream error is surfaced",
			fixture:     "chart_error_notfound.json",
			errContains: "No data found",
		},
		{
			name:        "ragged arrays truncate instead of panicking",
			fixture:     "chart_ragged.json",
			wantCandles: 2, // high[] is the shortest at 2 entries
		},
		{
			name:        "missing price still yields bars",
			fixture:     "chart_no_price.json",
			wantCandles: 2,
			check: func(t *testing.T, c []marketdata.Candle, m ChartMeta) {
				if m.HasPrice {
					t.Error("HasPrice = true, want false when meta omits regularMarketPrice")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candles, meta, err := ParseChart(load(t, tc.fixture))
			if tc.errContains != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.errContains)
				}
				if !contains(err.Error(), tc.errContains) {
					t.Fatalf("error %q does not contain %q", err, tc.errContains)
				}
				return
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(candles) != tc.wantCandles {
				t.Fatalf("got %d candles, want %d", len(candles), tc.wantCandles)
			}
			if tc.check != nil {
				tc.check(t, candles, meta)
			}
		})
	}
}

func TestParseChartMalformed(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not json", "<html>429 Too Many Requests</html>"},
		{"empty body", ""},
		{"empty result array", `{"chart":{"result":[],"error":null}}`},
		{"null everything", `{"chart":{"result":null,"error":null}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The contract is: return an error, never panic, never hand back
			// candles the caller might chart.
			candles, _, err := ParseChart([]byte(tc.body))
			if err == nil {
				t.Fatalf("want error, got %d candles", len(candles))
			}
			if len(candles) != 0 {
				t.Errorf("got %d candles alongside an error, want 0", len(candles))
			}
		})
	}
}

func TestQuoteFromMeta(t *testing.T) {
	aapl := marketdata.MustParseSymbol("AAPL")

	t.Run("derives change from meta", func(t *testing.T) {
		candles, meta, err := ParseChart(load(t, "chart_aapl_1d.json"))
		if err != nil {
			t.Fatal(err)
		}
		q, err := quoteFromMeta(aapl, meta, candles)
		if err != nil {
			t.Fatal(err)
		}
		if q.Price != 226.79 {
			t.Errorf("price = %v, want 226.79", q.Price)
		}
		wantChange := 226.79 - 220.15
		if !nearly(q.Change, wantChange) {
			t.Errorf("change = %v, want %v", q.Change, wantChange)
		}
		if !nearly(q.ChangePercent, wantChange/220.15*100) {
			t.Errorf("changePercent = %v, want %v", q.ChangePercent, wantChange/220.15*100)
		}
	})

	t.Run("falls back to last bar when meta has no price", func(t *testing.T) {
		candles, meta, err := ParseChart(load(t, "chart_no_price.json"))
		if err != nil {
			t.Fatal(err)
		}
		q, err := quoteFromMeta(aapl, meta, candles)
		if err != nil {
			t.Fatal(err)
		}
		if q.Price != 11.2 {
			t.Errorf("price = %v, want 11.2 (last close)", q.Price)
		}
		if q.PrevClose != 10.2 {
			t.Errorf("prevClose = %v, want 10.2 (second-to-last close)", q.PrevClose)
		}
	})

	t.Run("no price and no bars is an error", func(t *testing.T) {
		_, err := quoteFromMeta(aapl, ChartMeta{}, nil)
		if !errors.Is(err, marketdata.ErrNoData) {
			t.Errorf("error = %v, want ErrNoData", err)
		}
	})

	t.Run("currency defaults from the symbol", func(t *testing.T) {
		rel := marketdata.MustParseSymbol("RELIANCE.BSE")
		q, err := quoteFromMeta(rel, ChartMeta{Price: 100, HasPrice: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if q.Currency != "INR" {
			t.Errorf("currency = %q, want INR", q.Currency)
		}
	})
}

func TestVendorSymbol(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"AAPL", "AAPL"},
		{"RELIANCE.BSE", "RELIANCE.BO"},
		{"TCS.NSE", "TCS.NS"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := vendorSymbol(marketdata.MustParseSymbol(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("vendorSymbol(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestVendorInterval(t *testing.T) {
	// 1h is the one that differs: Yahoo spells it 60m.
	got, err := vendorInterval(marketdata.Interval1h)
	if err != nil {
		t.Fatal(err)
	}
	if got != "60m" {
		t.Errorf("vendorInterval(1h) = %q, want 60m", got)
	}
	if _, err := vendorInterval(marketdata.Interval("3mo")); !errors.Is(err, marketdata.ErrNotSupported) {
		t.Errorf("error = %v, want ErrNotSupported", err)
	}
}

func TestVendorRange(t *testing.T) {
	tests := []struct {
		name     string
		interval marketdata.Interval
		limit    int
		want     string
	}{
		{"200 daily bars needs about a year", marketdata.Interval1d, 200, "1y"},
		{"30 daily bars fits in three months", marketdata.Interval1d, 30, "3mo"},
		{"1m is capped at Yahoo's 7-day ceiling", marketdata.Interval1m, 10000, "5d"},
		{"5m is capped at Yahoo's 60-day ceiling", marketdata.Interval5m, 10000, "1mo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := vendorRange(tc.interval, tc.limit); got != tc.want {
				t.Errorf("vendorRange(%s, %d) = %q, want %q", tc.interval, tc.limit, got, tc.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func nearly(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
