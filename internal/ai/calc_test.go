package ai

import (
	"math"
	"strings"
	"testing"
)

// The arithmetic here decides how much money someone puts at risk. It is
// computed in Go precisely so it can be pinned down by tests rather than
// trusted to a model, and these are those tests.
func TestComputePosition(t *testing.T) {
	tests := []struct {
		name           string
		req            PositionRequest
		wantShares     float64
		wantRiskBudget float64
		wantActualRisk float64
		wantRR         float64
	}{
		{
			name: "long trade, round numbers",
			req: PositionRequest{
				AccountValue: 1_000_000, RiskPercent: 1,
				EntryPrice: 100, StopPrice: 95, Currency: "INR",
			},
			// 1% of 1,000,000 = 10,000 risk budget; 5 per share -> 2,000 shares.
			wantShares: 2000, wantRiskBudget: 10000, wantActualRisk: 10000,
		},
		{
			name: "share count rounds down, never up",
			req: PositionRequest{
				AccountValue: 100_000, RiskPercent: 1,
				EntryPrice: 100, StopPrice: 97, Currency: "USD",
			},
			// 1,000 budget / 3 per share = 333.33 -> 333 shares.
			wantShares: 333, wantRiskBudget: 1000, wantActualRisk: 999,
		},
		{
			name: "short trade, stop above entry",
			req: PositionRequest{
				AccountValue: 500_000, RiskPercent: 2,
				EntryPrice: 200, StopPrice: 210, Currency: "INR",
			},
			// 10,000 budget / 10 per share = 1,000 shares.
			wantShares: 1000, wantRiskBudget: 10000, wantActualRisk: 10000,
		},
		{
			name: "reward to risk with a target",
			req: PositionRequest{
				AccountValue: 1_000_000, RiskPercent: 1,
				EntryPrice: 100, StopPrice: 95, TargetPrice: 115, Currency: "INR",
			},
			wantShares: 2000, wantRiskBudget: 10000, wantActualRisk: 10000, wantRR: 3,
		},
		{
			name: "fractional risk percent",
			req: PositionRequest{
				AccountValue: 250_000, RiskPercent: 0.5,
				EntryPrice: 50, StopPrice: 48.75, Currency: "USD",
			},
			// 1,250 budget / 1.25 per share = 1,000 shares.
			wantShares: 1000, wantRiskBudget: 1250, wantActualRisk: 1250,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputePosition(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			check := func(label string, want float64) {
				t.Helper()
				for _, f := range append(append([]CalcField{}, got.Inputs...), got.Results...) {
					if f.Label == label {
						if math.Abs(f.Raw-want) > 0.005 {
							t.Errorf("%s = %v, want %v", label, f.Raw, want)
						}
						return
					}
				}
				t.Errorf("no field labelled %q", label)
			}
			check("Shares", tc.wantShares)
			check("Risk budget", tc.wantRiskBudget)
			check("Actual risk", tc.wantActualRisk)
			if tc.wantRR > 0 {
				check("Reward:risk", tc.wantRR)
			}
		})
	}
}

func TestComputePositionNeverExceedsTheRiskBudget(t *testing.T) {
	// The invariant that matters: rounding must never push actual risk above
	// what the operator authorised.
	for _, entry := range []float64{10, 99.99, 100, 1234.56, 2431} {
		for _, stopPct := range []float64{0.5, 1, 3.33, 7} {
			stop := entry * (1 - stopPct/100)
			req := PositionRequest{
				AccountValue: 1_000_000, RiskPercent: 1,
				EntryPrice: entry, StopPrice: stop,
			}
			got, err := ComputePosition(req)
			if err != nil {
				t.Fatal(err)
			}
			var budget, actual float64
			for _, f := range got.Results {
				switch f.Label {
				case "Risk budget":
					budget = f.Raw
				case "Actual risk":
					actual = f.Raw
				}
			}
			if actual > budget+0.001 {
				t.Errorf("entry %v stop %v: actual risk %v exceeds the budget %v", entry, stop, actual, budget)
			}
		}
	}
}

func TestComputePositionRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		req  PositionRequest
	}{
		{name: "zero account", req: PositionRequest{AccountValue: 0, RiskPercent: 1, EntryPrice: 10, StopPrice: 9}},
		{name: "negative account", req: PositionRequest{AccountValue: -1, RiskPercent: 1, EntryPrice: 10, StopPrice: 9}},
		{name: "zero risk", req: PositionRequest{AccountValue: 100, RiskPercent: 0, EntryPrice: 10, StopPrice: 9}},
		{name: "risk over 100pc", req: PositionRequest{AccountValue: 100, RiskPercent: 101, EntryPrice: 10, StopPrice: 9}},
		{name: "zero entry", req: PositionRequest{AccountValue: 100, RiskPercent: 1, EntryPrice: 0, StopPrice: 9}},
		{name: "zero stop", req: PositionRequest{AccountValue: 100, RiskPercent: 1, EntryPrice: 10, StopPrice: 0}},
		{
			// Division by zero would otherwise produce an infinite share count.
			name: "stop equals entry",
			req:  PositionRequest{AccountValue: 100, RiskPercent: 1, EntryPrice: 10, StopPrice: 10},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ComputePosition(tc.req); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestComputePositionWarnings(t *testing.T) {
	tests := []struct {
		name     string
		req      PositionRequest
		wantText string
	}{
		{
			name: "position needs leverage",
			req: PositionRequest{
				AccountValue: 10_000, RiskPercent: 50,
				EntryPrice: 100, StopPrice: 99,
			},
			wantText: "leverage",
		},
		{
			name: "concentrated position",
			req: PositionRequest{
				AccountValue: 100_000, RiskPercent: 2,
				EntryPrice: 100, StopPrice: 95,
			},
			wantText: "concentrated",
		},
		{
			name: "cannot afford one share",
			req: PositionRequest{
				AccountValue: 1000, RiskPercent: 0.1,
				EntryPrice: 5000, StopPrice: 2500,
			},
			wantText: "single share",
		},
		{
			name: "target closer than the stop",
			req: PositionRequest{
				AccountValue: 1_000_000, RiskPercent: 1,
				EntryPrice: 100, StopPrice: 90, TargetPrice: 105,
			},
			wantText: "Reward:risk",
		},
		{
			name: "target on the wrong side for a long",
			req: PositionRequest{
				AccountValue: 1_000_000, RiskPercent: 1,
				EntryPrice: 100, StopPrice: 95, TargetPrice: 90,
			},
			wantText: "same side",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputePosition(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(got.Warnings, " | ")
			if !strings.Contains(joined, tc.wantText) {
				t.Errorf("warnings %q do not mention %q", joined, tc.wantText)
			}
		})
	}
}

func TestComputePositionCurrencyFormatting(t *testing.T) {
	tests := []struct {
		currency string
		want     string
	}{
		{currency: "INR", want: "₹"},
		{currency: "USD", want: "$"},
		{currency: "", want: "1,000,000.00"},
	}
	for _, tc := range tests {
		t.Run(tc.currency, func(t *testing.T) {
			got, err := ComputePosition(PositionRequest{
				AccountValue: 1_000_000, RiskPercent: 1,
				EntryPrice: 100, StopPrice: 95, Currency: tc.currency,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Inputs[0].Value, tc.want) {
				t.Errorf("account value rendered as %q, want it to contain %q", got.Inputs[0].Value, tc.want)
			}
		})
	}
}

func TestHumanise(t *testing.T) {
	tests := map[float64]string{
		0:          "0.00",
		1:          "1.00",
		999.99:     "999.99",
		1000:       "1,000.00",
		1234567.89: "1,234,567.89",
		-2500:      "-2,500.00",
	}
	for in, want := range tests {
		if got := humanise(in); got != want {
			t.Errorf("humanise(%v) = %q, want %q", in, got, want)
		}
	}
}
