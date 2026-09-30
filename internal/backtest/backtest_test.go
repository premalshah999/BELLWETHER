package backtest

import (
	"math"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/marketdata"
)

func sym(t *testing.T) marketdata.Symbol {
	t.Helper()
	s, err := marketdata.ParseSymbol("TEST")
	if err != nil {
		t.Fatalf("parse symbol: %v", err)
	}
	return s
}

// bars builds a daily series from (open, high, low, close) tuples.
func bars(ohlc ...[4]float64) []marketdata.Candle {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]marketdata.Candle, len(ohlc))
	for i, v := range ohlc {
		out[i] = marketdata.Candle{
			Time:   start.AddDate(0, 0, i),
			Open:   v[0],
			High:   v[1],
			Low:    v[2],
			Close:  v[3],
			Volume: 1000,
		}
	}
	return out
}

// flat builds a bar whose four prices are all the same, for series where only
// the close matters.
func flat(closes ...float64) []marketdata.Candle {
	ohlc := make([][4]float64, len(closes))
	for i, c := range closes {
		ohlc[i] = [4]float64{c, c, c, c}
	}
	return bars(ohlc...)
}

func aboveHundred() *algo.Algorithm {
	return &algo.Algorithm{
		Name:     "above 100",
		Interval: "1d",
		All:      []algo.Node{{Indicator: "close", Op: ">", Value: ptr(100.0)}},
	}
}

func ptr(f float64) *float64 { return &f }

// The signal is on the bar that closes above 100; the fill is the *next* bar's
// open. Buying at the signal bar's own close would mean transacting at a price
// that was not known until after the chance to act had passed, and it is the
// single most common way a backtest flatters itself.
func TestEntersAtNextBarOpenNotSignalClose(t *testing.T) {
	// close crosses above 100 on index 2. Index 3 opens at 150, a price
	// deliberately far from index 2's close so the two are distinguishable.
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101}, // signal here
		[4]float64{150, 160, 149, 155}, // fill should be this open: 150
		[4]float64{155, 156, 154, 155},
	)
	res, err := Run(aboveHundred(), sym(t), cs, Config{MaxHoldBars: 1, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) == 0 {
		t.Fatal("expected a trade")
	}
	got := res.Trades[0].EntryPrice
	if got != 150 {
		t.Errorf("entry price = %v, want 150 (the next bar's open); 101 would mean it bought at the signal bar's close", got)
	}
}

// Truncating the data must not change any decision that was already made.
//
// This is the load-bearing test of the package. If the evaluator could see
// past the bar under evaluation, adding future bars would alter earlier
// trades, and the whole result would be a description of hindsight.
func TestFutureBarsCannotChangeEarlierTrades(t *testing.T) {
	cs := flat(
		90, 95, 101, 104, 99, 97, 102, 108, 96, 94,
		103, 107, 111, 98, 92, 105, 109, 113, 99, 95,
	)
	cfg := Config{StopLossPct: 3, TakeProfitPct: 5, MaxHoldBars: 3, CostBps: 5}

	full, err := Run(aboveHundred(), sym(t), append([]marketdata.Candle(nil), cs...), cfg, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	cut := 12
	short, err := Run(aboveHundred(), sym(t), append([]marketdata.Candle(nil), cs[:cut]...), cfg, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("short run: %v", err)
	}

	// Every trade that closed before the truncation point must be identical.
	limit := cs[cut-1].Time
	for i, want := range short.Trades {
		if want.ExitTime.After(limit) || want.Reason == "still open at the end of the data" {
			continue
		}
		if i >= len(full.Trades) {
			t.Fatalf("trade %d present in the short run but missing from the full run", i)
		}
		got := full.Trades[i]
		if !got.EntryTime.Equal(want.EntryTime) || got.EntryPrice != want.EntryPrice ||
			!got.ExitTime.Equal(want.ExitTime) || got.ExitPrice != want.ExitPrice {
			t.Errorf("trade %d changed when later bars were added:\n short %+v\n full  %+v", i, want, got)
		}
	}
}

func TestStopLossFillsAtTheStopNotTheClose(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101}, // signal (index 2, the first evaluable bar)
		[4]float64{100, 100, 100, 100}, // entry at this open: 100
		[4]float64{100, 100, 80, 85},   // low pierces a 5% stop at 95
	)
	res, err := Run(aboveHundred(), sym(t), cs, Config{StopLossPct: 5, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("got %d trades, want 1", len(res.Trades))
	}
	tr := res.Trades[0]
	if tr.Reason != "stop loss" {
		t.Errorf("reason = %q, want stop loss", tr.Reason)
	}
	if math.Abs(tr.ExitPrice-95) > 1e-9 {
		t.Errorf("exit = %v, want 95 (the stop); 85 would mean it waited for the close", tr.ExitPrice)
	}
}

func TestTakeProfitFillsAtTheTarget(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 100, 100, 100}, // entry at 100
		[4]float64{100, 130, 100, 105}, // high clears a 10% target at 110
	)
	res, err := Run(aboveHundred(), sym(t), cs, Config{TakeProfitPct: 10, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 || res.Trades[0].Reason != "take profit" {
		t.Fatalf("got %+v, want one take-profit trade", res.Trades)
	}
	if math.Abs(res.Trades[0].ExitPrice-110) > 1e-9 {
		t.Errorf("exit = %v, want 110", res.Trades[0].ExitPrice)
	}
}

// A bar that reaches both levels is genuinely undecidable from OHLC. Assuming
// the target would turn every volatile bar into a win, so the stop is taken
// and the trade is flagged.
func TestAmbiguousBarTakesTheStopAndIsFlagged(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 100, 100, 100}, // entry at 100
		[4]float64{100, 130, 80, 100},  // reaches +10% target and -5% stop
	)
	res, err := Run(aboveHundred(), sym(t), cs, Config{StopLossPct: 5, TakeProfitPct: 10}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("got %d trades, want 1", len(res.Trades))
	}
	if !res.Trades[0].Ambiguous {
		t.Error("trade was not flagged ambiguous")
	}
	if res.Trades[0].Reason != "stop loss" {
		t.Errorf("reason = %q, want the conservative stop", res.Trades[0].Reason)
	}
	if res.Stats.Ambiguous != 1 {
		t.Errorf("stats.Ambiguous = %d, want 1", res.Stats.Ambiguous)
	}
	found := false
	for _, w := range res.Warnings {
		if len(w) > 0 && w[0] == '1' {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning mentioned the ambiguous bar: %v", res.Warnings)
	}
}

// Costs are charged on both sides. A flat round trip must lose exactly two
// sides' worth.
func TestCostsAreChargedOnEntryAndExit(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 100, 100, 100}, // entry at 100
		[4]float64{100, 100, 100, 100}, // exit at 100 on the hold limit
	)
	res, err := Run(aboveHundred(), sym(t), cs, Config{MaxHoldBars: 1, CostBps: 50}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("got %d trades, want 1", len(res.Trades))
	}
	// Costs are charged against each leg's own value, so a flat round trip
	// costs exactly two sides: (1)(1−c) − (1+c) = −2c. At 50bps that is
	// −1.00%. The old formulation, (1−c)² − 1, gave −0.9975% — close, but an
	// approximation where an exact answer is available.
	want := -2 * 0.005 * 100
	if math.Abs(res.Trades[0].ReturnPct-want) > 1e-9 {
		t.Errorf("return = %.6f%%, want %.6f%% (two sides of 50bps)", res.Trades[0].ReturnPct, want)
	}
}

func TestBuyHoldUsesTheSameWindowAsTheStrategy(t *testing.T) {
	// Never triggers, so the strategy does nothing and only the benchmark
	// moves. The earliest the strategy could have been in the market is index
	// 3, so the benchmark buys that open (100) and holds to the final close
	// (200): +100%. Measuring it from the first bar of data would credit the
	// benchmark with a move the strategy could never have caught.
	never := &algo.Algorithm{
		Name:     "never",
		Interval: "1d",
		All:      []algo.Node{{Indicator: "close", Op: ">", Value: ptr(1e9)}},
	}
	cs := bars(
		[4]float64{10, 10, 10, 10},
		[4]float64{20, 20, 20, 20},
		[4]float64{50, 50, 50, 50},
		[4]float64{100, 100, 100, 100},
		[4]float64{200, 200, 200, 200},
	)
	res, err := Run(never, sym(t), cs, Config{}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("expected no trades, got %d", len(res.Trades))
	}
	if math.Abs(res.Stats.BuyHoldPct-100) > 1e-6 {
		t.Errorf("buy-and-hold = %.2f%%, want 100", res.Stats.BuyHoldPct)
	}
	if res.Stats.TotalPct != 0 {
		t.Errorf("strategy return = %v, want 0", res.Stats.TotalPct)
	}
}

// An unclosed position must be reported, not dropped: dropping it hides
// exactly the losing trades that had not yet been cut.
func TestOpenPositionAtTheEndIsReported(t *testing.T) {
	cs := flat(90, 95, 101, 100, 99, 98, 97)
	res, err := Run(aboveHundred(), sym(t), cs, Config{}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("got %d trades, want 1", len(res.Trades))
	}
	if res.Trades[0].Reason != "still open at the end of the data" {
		t.Errorf("reason = %q", res.Trades[0].Reason)
	}
}

func TestWarnsWhenThereIsNoExitRule(t *testing.T) {
	res, err := Run(aboveHundred(), sym(t), flat(90, 95, 101, 102, 103), Config{CostBps: 10}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var found bool
	for _, w := range res.Warnings {
		if w == "No exit rule was set, so each position was held to the end of the data. That measures the entry signal, not a strategy." {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning about the missing exit rule: %v", res.Warnings)
	}
}

func TestZeroCostsAreCalledOut(t *testing.T) {
	res, err := Run(aboveHundred(), sym(t), flat(90, 95, 101, 102, 103), Config{MaxHoldBars: 1, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var found bool
	for _, w := range res.Warnings {
		if w == "Costs were set to zero. This is not an optimistic estimate — it is a strategy that trades for free." {
			found = true
		}
	}
	if !found {
		t.Errorf("zero costs were not called out: %v", res.Warnings)
	}
}

// Candles arriving newest-first must not silently invert the run.
func TestUnsortedCandlesAreOrdered(t *testing.T) {
	cs := flat(90, 95, 101, 100, 105)
	rev := make([]marketdata.Candle, len(cs))
	for i := range cs {
		rev[i] = cs[len(cs)-1-i]
	}
	res, err := Run(aboveHundred(), sym(t), rev, Config{MaxHoldBars: 1}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.From.Before(res.To) {
		t.Errorf("window runs backwards: %s to %s", res.From, res.To)
	}
}

func TestNoCandlesIsAnError(t *testing.T) {
	if _, err := Run(aboveHundred(), sym(t), nil, Config{}, algo.NewEvaluator()); err == nil {
		t.Error("expected an error for an empty series")
	}
}

// ---------------------------------------------------------------- shorts

// A short profits when price falls, and its stop sits *above* entry. Getting
// that backwards produces a short book that appears to stop out into profit,
// which reads as a brilliant strategy and is the most dangerous bug this
// package could have.
func TestShortProfitsWhenPriceFalls(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101}, // signal
		[4]float64{100, 100, 90, 90},   // short at this open (100), covered at its close (90)
	)
	res, err := Run(aboveHundred(), sym(t), cs,
		Config{Direction: Short, MaxHoldBars: 1, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("got %d trades, want 1", len(res.Trades))
	}
	tr := res.Trades[0]
	if tr.Direction != Short {
		t.Errorf("direction = %q", tr.Direction)
	}
	// Sold at 100, bought back at 90: +10%.
	if math.Abs(tr.ReturnPct-10) > 1e-9 {
		t.Errorf("return = %.4f%%, want +10 (a short into a fall); -10 would mean the sign is inverted", tr.ReturnPct)
	}
}

func TestShortStopIsAboveEntry(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 100, 100, 100}, // short at 100
		[4]float64{100, 120, 100, 118}, // high pierces a 5% stop at 105
	)
	res, err := Run(aboveHundred(), sym(t), cs,
		Config{Direction: Short, StopLossPct: 5, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 || res.Trades[0].Reason != "stop loss" {
		t.Fatalf("got %+v, want one stopped short", res.Trades)
	}
	if math.Abs(res.Trades[0].ExitPrice-105) > 1e-9 {
		t.Errorf("exit = %v, want 105 (the stop above entry)", res.Trades[0].ExitPrice)
	}
	// Sold at 100, covered at 105: -5%.
	if math.Abs(res.Trades[0].ReturnPct+5) > 1e-9 {
		t.Errorf("return = %.4f%%, want -5", res.Trades[0].ReturnPct)
	}
}

func TestShortTargetIsBelowEntry(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 100, 100, 100}, // short at 100
		[4]float64{100, 100, 80, 95},   // low reaches a 10% target at 90
	)
	res, err := Run(aboveHundred(), sym(t), cs,
		Config{Direction: Short, TakeProfitPct: 10, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 || res.Trades[0].Reason != "take profit" {
		t.Fatalf("got %+v, want one short at target", res.Trades)
	}
	if math.Abs(res.Trades[0].ExitPrice-90) > 1e-9 {
		t.Errorf("exit = %v, want 90 (the target below entry)", res.Trades[0].ExitPrice)
	}
}

// Costs must bite identically in both directions.
func TestCostsAreSymmetricAcrossDirections(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 100, 100, 100},
		[4]float64{100, 100, 100, 100}, // flat round trip either way
	)
	long, err := Run(aboveHundred(), sym(t), append([]marketdata.Candle(nil), cs...),
		Config{Direction: Long, MaxHoldBars: 1, CostBps: 25}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("long: %v", err)
	}
	short, err := Run(aboveHundred(), sym(t), append([]marketdata.Candle(nil), cs...),
		Config{Direction: Short, MaxHoldBars: 1, CostBps: 25}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("short: %v", err)
	}
	if len(long.Trades) != 1 || len(short.Trades) != 1 {
		t.Fatalf("want one trade each, got %d and %d", len(long.Trades), len(short.Trades))
	}
	want := -2 * 0.0025 * 100
	for name, got := range map[string]float64{
		"long":  long.Trades[0].ReturnPct,
		"short": short.Trades[0].ReturnPct,
	} {
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s flat round trip = %.6f%%, want %.6f%%", name, got, want)
		}
	}
}

func TestShortResultsCarryABorrowWarning(t *testing.T) {
	res, err := Run(aboveHundred(), sym(t), flat(90, 95, 101, 102, 103),
		Config{Direction: Short, MaxHoldBars: 1, CostBps: 10}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var found bool
	for _, w := range res.Warnings {
		if len(w) > 5 && w[:5] == "Short" {
			found = true
		}
	}
	if !found {
		t.Errorf("no borrow warning on a short run: %v", res.Warnings)
	}
}

// ---------------------------------------------------------------- sizing

// The return on the position and the effect on the account are different
// numbers the moment sizing is not 100%, and confusing them is how a backtest
// gets believed.
func TestFractionalSizingScalesTheAccountNotTheTrade(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 110, 100, 110}, // entry at this open (100), out at its close (110): +10%
	)
	res, err := Run(aboveHundred(), sym(t), cs,
		Config{SizeMode: SizeFraction, SizePct: 25, MaxHoldBars: 1, CostBps: 0},
		algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("got %d trades, want 1", len(res.Trades))
	}
	tr := res.Trades[0]
	if math.Abs(tr.Size-0.25) > 1e-9 {
		t.Errorf("size = %v, want 0.25", tr.Size)
	}
	if math.Abs(tr.ReturnPct-10) > 1e-9 {
		t.Errorf("position return = %.4f%%, want +10", tr.ReturnPct)
	}
	if math.Abs(tr.AccountPct-2.5) > 1e-9 {
		t.Errorf("account effect = %.4f%%, want +2.5 (a quarter of the position's move)", tr.AccountPct)
	}
	if math.Abs(res.Stats.TotalPct-2.5) > 1e-9 {
		t.Errorf("total = %.4f%%, want +2.5", res.Stats.TotalPct)
	}
}

// Risking r% behind an s% stop commits r/s of the account, so being stopped
// out costs exactly r%. That identity is the whole reason to size this way.
func TestRiskSizingLosesExactlyTheRiskBudget(t *testing.T) {
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 100, 100, 100}, // entry at 100
		[4]float64{100, 100, 70, 75},   // stopped at 95
	)
	res, err := Run(aboveHundred(), sym(t), cs,
		Config{SizeMode: SizeRisk, RiskPct: 1, StopLossPct: 5, CostBps: 0},
		algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("got %d trades, want 1", len(res.Trades))
	}
	tr := res.Trades[0]
	if math.Abs(tr.Size-0.2) > 1e-9 {
		t.Errorf("size = %v, want 0.2 (1%% risk over a 5%% stop)", tr.Size)
	}
	if math.Abs(tr.AccountPct+1) > 1e-9 {
		t.Errorf("account effect = %.4f%%, want exactly -1 (the risk budget)", tr.AccountPct)
	}
}

// A cash account cannot take a position larger than itself, so an aggressive
// risk setting is clamped and said out loud rather than silently levered.
func TestRiskSizingIsCappedAtTheWholeAccount(t *testing.T) {
	res, err := Run(aboveHundred(), sym(t), flat(90, 95, 101, 100, 95),
		Config{SizeMode: SizeRisk, RiskPct: 10, StopLossPct: 2, CostBps: 0},
		algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) == 0 {
		t.Fatal("expected a trade")
	}
	if res.Trades[0].Size != 1 {
		t.Errorf("size = %v, want 1 (capped)", res.Trades[0].Size)
	}
	var warned bool
	for _, w := range res.Warnings {
		if len(w) > 7 && w[:7] == "Risking" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the cap was applied without saying so: %v", res.Warnings)
	}
}

func TestRiskSizingWithoutAStopIsCalledOut(t *testing.T) {
	res, err := Run(aboveHundred(), sym(t), flat(90, 95, 101, 102, 103),
		Config{SizeMode: SizeRisk, RiskPct: 1, MaxHoldBars: 1, CostBps: 10},
		algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var found bool
	for _, w := range res.Warnings {
		if len(w) > 10 && w[:10] == "Risk-based" {
			found = true
		}
	}
	if !found {
		t.Errorf("risk sizing without a stop was not called out: %v", res.Warnings)
	}
}

// ------------------------------------------------------------- exit rule

// An exit signal is acted on at the next open, for the same reason an entry
// is: the bar's close is not knowable until it has closed.
func TestExitRuleFillsAtTheNextOpen(t *testing.T) {
	below := &algo.Algorithm{
		Name:     "below 100",
		Interval: "1d",
		All:      []algo.Node{{Indicator: "close", Op: "<", Value: ptr(100.0)}},
	}
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101}, // entry signal
		[4]float64{100, 100, 100, 100}, // entry at 100; close is not < 100
		[4]float64{100, 100, 100, 99},  // exit signal on this close
		[4]float64{88, 88, 88, 88},     // exit fills at this open: 88
	)
	res, err := Run(aboveHundred(), sym(t), cs,
		Config{ExitRule: below, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("got %d trades, want 1: %+v", len(res.Trades), res.Trades)
	}
	if res.Trades[0].Reason != "exit rule" {
		t.Errorf("reason = %q, want exit rule", res.Trades[0].Reason)
	}
	if math.Abs(res.Trades[0].ExitPrice-88) > 1e-9 {
		t.Errorf("exit = %v, want 88 (the next open); 99 would mean it exited at the signal bar's close", res.Trades[0].ExitPrice)
	}
}

// A stop fills during the bar; a rule can only be read once the bar closes.
// When both would fire, the stop must win.
func TestStopBeatsTheExitRuleOnTheSameBar(t *testing.T) {
	below := &algo.Algorithm{
		Name:     "below 100",
		Interval: "1d",
		All:      []algo.Node{{Indicator: "close", Op: "<", Value: ptr(100.0)}},
	}
	cs := bars(
		[4]float64{90, 90, 90, 90},
		[4]float64{95, 95, 95, 95},
		[4]float64{101, 101, 101, 101},
		[4]float64{100, 100, 100, 100}, // entry at 100
		[4]float64{100, 100, 80, 85},   // stop at 95 fills intrabar; close 85 also trips the rule
		[4]float64{70, 70, 70, 70},
	)
	res, err := Run(aboveHundred(), sym(t), cs,
		Config{ExitRule: below, StopLossPct: 5, CostBps: 0}, algo.NewEvaluator())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Trades) == 0 {
		t.Fatal("expected a trade")
	}
	if res.Trades[0].Reason != "stop loss" {
		t.Errorf("reason = %q, want stop loss to win the bar", res.Trades[0].Reason)
	}
	if math.Abs(res.Trades[0].ExitPrice-95) > 1e-9 {
		t.Errorf("exit = %v, want 95", res.Trades[0].ExitPrice)
	}
}
