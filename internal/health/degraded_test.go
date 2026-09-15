package health

import (
	"context"
	"errors"
	"testing"
)

func TestDegradedProviders(t *testing.T) {
	fail := errors.New("down")

	tests := []struct {
		name    string
		deps    []Dep
		observe func(*Tracker)
		want    []string
		wantAll bool
	}{
		{
			name: "everything healthy",
			deps: []Dep{{Provider: "yahoo", Kind: KindMarketData, Configured: true}},
			want: nil,
		},
		{
			// The case the acceptance walkthrough exercises: one source dies
			// and the fallback holds. Worth saying, but not worth alarming.
			name: "one failing, one covering",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "alphavantage", Kind: KindMarketData, Configured: true},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "alphavantage", false, false, fail)
				tr.Observe(context.Background(), "yahoo", true, false, nil)
			},
			want: []string{"alphavantage"},
		},
		{
			name: "everything failing",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "alphavantage", Kind: KindMarketData, Configured: true},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "alphavantage", false, false, fail)
				tr.Observe(context.Background(), "yahoo", false, false, fail)
			},
			want:    []string{"alphavantage", "yahoo"},
			wantAll: true,
		},
		{
			name: "an unconfigured provider is not degraded",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "alphavantage", Kind: KindMarketData, Configured: false},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "yahoo", true, false, nil)
			},
			want: nil,
		},
		{
			name: "a spent budget counts as degraded but not as down",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "alphavantage", Kind: KindMarketData, Configured: true},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "alphavantage", false, true, errors.New("budget"))
				tr.Observe(context.Background(), "yahoo", true, false, nil)
			},
			want: []string{"alphavantage"},
		},
		{
			name: "a failing LLM is not a market data problem",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "llm", Kind: KindLLM, Configured: true},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "yahoo", true, false, nil)
				tr.Observe(context.Background(), "llm", false, false, fail)
			},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker(tc.deps...)
			if tc.observe != nil {
				tc.observe(tr)
			}
			got := tr.DegradedProviders()
			if len(got) != len(tc.want) {
				t.Fatalf("DegradedProviders = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("DegradedProviders[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
			if gotAll := tr.MarketDataDegraded(); gotAll != tc.wantAll {
				t.Errorf("MarketDataDegraded = %v, want %v", gotAll, tc.wantAll)
			}
		})
	}
}

func TestDegradedProvidersOrderIsStable(t *testing.T) {
	// The banner names them; the text must not shuffle between polls.
	tr := newTracker(
		Dep{Provider: "yahoo", Kind: KindMarketData, Configured: true},
		Dep{Provider: "alphavantage", Kind: KindMarketData, Configured: true},
		Dep{Provider: "synthetic", Kind: KindMarketData, Configured: true},
	)
	boom := errors.New("x")
	for _, p := range []string{"yahoo", "alphavantage", "synthetic"} {
		tr.Observe(context.Background(), p, false, false, boom)
	}
	want := []string{"alphavantage", "synthetic", "yahoo"}
	for i := 0; i < 5; i++ {
		got := tr.DegradedProviders()
		for j, w := range want {
			if got[j] != w {
				t.Fatalf("iteration %d: got %v, want %v", i, got, want)
			}
		}
	}
}
