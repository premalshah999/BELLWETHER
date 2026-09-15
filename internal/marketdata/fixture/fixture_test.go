package fixture

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

var frozen = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

func newProvider() *Provider {
	return New(WithClock(func() time.Time { return frozen }))
}

func TestDeterministic(t *testing.T) {
	// The same symbol must produce the same series every time, or a chart
	// would reshuffle itself on every poll.
	sym := marketdata.MustParseSymbol("RELIANCE.BSE")
	a, err := newProvider().Candles(context.Background(), sym, marketdata.Interval1d, 50)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newProvider().Candles(context.Background(), sym, marketdata.Interval1d, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Candles) != len(b.Candles) {
		t.Fatalf("lengths differ: %d vs %d", len(a.Candles), len(b.Candles))
	}
	for i := range a.Candles {
		if a.Candles[i] != b.Candles[i] {
			t.Fatalf("candle %d differs: %+v vs %+v", i, a.Candles[i], b.Candles[i])
		}
	}
}

func TestCandlesAreCoherent(t *testing.T) {
	for _, s := range []string{"RELIANCE.BSE", "AAPL", "TCS.NSE"} {
		t.Run(s, func(t *testing.T) {
			sym := marketdata.MustParseSymbol(s)
			bars, err := newProvider().Candles(context.Background(), sym, marketdata.Interval1d, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(bars.Candles) != 100 {
				t.Fatalf("got %d candles, want 100", len(bars.Candles))
			}
			for i, c := range bars.Candles {
				if c.High < c.Low {
					t.Errorf("candle %d: high %v below low %v", i, c.High, c.Low)
				}
				if c.High < c.Open || c.High < c.Close {
					t.Errorf("candle %d: high %v below open/close", i, c.High)
				}
				if c.Low > c.Open || c.Low > c.Close {
					t.Errorf("candle %d: low %v above open/close", i, c.Low)
				}
				if c.Close <= 0 || c.Volume <= 0 {
					t.Errorf("candle %d: non-positive close/volume: %+v", i, c)
				}
				if i > 0 && !c.Time.After(bars.Candles[i-1].Time) {
					t.Errorf("candle %d time %v not after previous", i, c.Time)
				}
			}
		})
	}
}

func TestPriceRangeMatchesVenue(t *testing.T) {
	// Rupee names should look like rupee names so a glance at a chart is not
	// immediately absurd.
	inr, err := newProvider().Candles(context.Background(),
		marketdata.MustParseSymbol("RELIANCE.BSE"), marketdata.Interval1d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if p := inr.Candles[0].Close; p < 100 || p > 20000 {
		t.Errorf("INR price %v is outside a plausible range", p)
	}
}

func TestQuoteAgreesWithLastCandle(t *testing.T) {
	p := newProvider()
	sym := marketdata.MustParseSymbol("AAPL")

	bars, err := p.Candles(context.Background(), sym, marketdata.Interval1d, 2)
	if err != nil {
		t.Fatal(err)
	}
	q, err := p.Quote(context.Background(), sym)
	if err != nil {
		t.Fatal(err)
	}
	last := bars.Candles[len(bars.Candles)-1]
	if q.Price != last.Close {
		t.Errorf("quote price %v disagrees with last close %v", q.Price, last.Close)
	}
	if q.Currency != "USD" {
		t.Errorf("currency = %q, want USD", q.Currency)
	}
}

func TestScriptedFailures(t *testing.T) {
	p := newProvider()
	sym := marketdata.MustParseSymbol("AAPL")
	boom := errors.New("scripted outage")

	p.FailNext(sym, boom)
	if _, err := p.Candles(context.Background(), sym, marketdata.Interval1d, 10); !errors.Is(err, boom) {
		t.Errorf("first call error = %v, want the scripted error", err)
	}
	if _, err := p.Candles(context.Background(), sym, marketdata.Interval1d, 10); err != nil {
		t.Errorf("second call should succeed, got %v", err)
	}
	if got := p.Calls(sym); got != 2 {
		t.Errorf("Calls = %d, want 2", got)
	}
}

func TestFailAlways(t *testing.T) {
	p := newProvider()
	sym := marketdata.MustParseSymbol("AAPL")
	boom := errors.New("permanent")
	p.FailAlways(sym, boom)

	for i := 0; i < 5; i++ {
		if _, err := p.Candles(context.Background(), sym, marketdata.Interval1d, 10); !errors.Is(err, boom) {
			t.Fatalf("call %d error = %v, want the scripted error", i, err)
		}
	}
}

func TestRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newProvider().Candles(ctx, marketdata.MustParseSymbol("AAPL"), marketdata.Interval1d, 10)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestName(t *testing.T) {
	if got := New().Name(); got != ProviderName {
		t.Errorf("Name = %q, want %q", got, ProviderName)
	}
	if got := New(WithName("alt")).Name(); got != "alt" {
		t.Errorf("Name = %q, want alt", got)
	}
}
