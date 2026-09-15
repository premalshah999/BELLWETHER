package marketdata

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestRoutineDeclinesDoNotCountAsFaults(t *testing.T) {
	// A provider that does not carry a symbol is working correctly and saying
	// so. Only a genuine failure should darken its health dot.
	tests := []struct {
		name        string
		err         error
		wantSkipped bool
	}{
		{name: "unsupported symbol or interval", err: ErrNotSupported, wantSkipped: true},
		{name: "budget spent", err: ErrBudgetExhausted, wantSkipped: true},
		{
			// Twelve Data's free tier returns this for every Indian listing.
			name: "provider does not carry this symbol", err: ErrNoData, wantSkipped: true,
		},
		{
			name:        "wrapped no-data is still a decline",
			err:         fmt.Errorf("twelvedata: %w: available on the Grow plan", ErrNoData),
			wantSkipped: true,
		},
		{name: "connection refused is a fault", err: errors.New("connection refused"), wantSkipped: false},
		{name: "http 500 is a fault", err: errors.New("http 500"), wantSkipped: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRoutineDecline(tc.err); got != tc.wantSkipped {
				t.Errorf("isRoutineDecline(%v) = %v, want %v", tc.err, got, tc.wantSkipped)
			}

			// And end to end through the router's outcome reporting.
			var got []Outcome
			var mu sync.Mutex
			p := &stubProvider{name: "twelvedata", err: tc.err}
			backup := &stubProvider{name: "alphavantage", candles: makeCandles(1, 2)}
			r := NewRouter(newMemCache(), []Provider{p, backup},
				WithLogger(quietLogger()),
				WithOutcomeSink(func(_ context.Context, o Outcome) {
					mu.Lock()
					got = append(got, o)
					mu.Unlock()
				}))

			if _, err := r.Candles(context.Background(), testSym, Interval1d, 10); err != nil {
				t.Fatalf("the fallback provider should have covered this: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("got %d outcomes, want 2", len(got))
			}
			if got[0].Skipped != tc.wantSkipped {
				t.Errorf("outcome.Skipped = %v, want %v", got[0].Skipped, tc.wantSkipped)
			}
			if !got[1].OK {
				t.Error("the fallback provider was not recorded as healthy")
			}
		})
	}
}
