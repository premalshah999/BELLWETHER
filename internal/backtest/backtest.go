// Package backtest runs an algorithm over history and reports what it would
// have done.
//
// The whole value of a backtest is that it is honest about a strategy the
// operator has not yet risked money on, so every design decision here is made
// against one question: could this number have been known at the time?
//
// Three rules follow from that, and they are the reason to read this file
// before trusting a result:
//
//  1. Conditions are evaluated by the same evaluator the live alert engine
//     uses, handed a slice that ends at the bar being evaluated. Look-ahead is
//     therefore structurally impossible rather than merely avoided — the
//     evaluator cannot read a bar that is not in the slice it was given.
//
//  2. A signal on a bar is acted on at the *next* bar's open. The close of a
//     bar is not knowable until that bar has closed, so a backtest that buys
//     at the signal bar's close is buying at a price it learned after the
//     opportunity passed. That single mistake is worth several percent a year
//     on most strategies and always in the flattering direction.
//
//  3. Costs are charged on both sides and default to a real number. A
//     backtest without costs is not an optimistic estimate, it is a different
//     strategy — one that trades for free.
package backtest

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// Direction is which way a signal is taken.
type Direction string

const (
	// Long buys the signal.
	Long Direction = "long"
	// Short sells it. The same rule, read as "this is about to fall".
	Short Direction = "short"
)

// SizeMode is how much of the account one position uses.
type SizeMode string

const (
	// SizeFull commits the whole account to each trade. Simple, and the
	// reason most naive backtests report drawdowns nobody could have sat
	// through.
	SizeFull SizeMode = "full"
	// SizeFraction commits a fixed share of the account.
	SizeFraction SizeMode = "fraction"
	// SizeRisk sizes so that being stopped out costs a fixed share of the
	// account. This is how position size is actually decided by anyone doing
	// it deliberately, and it makes the stop a risk decision rather than a
	// decoration.
	SizeRisk SizeMode = "risk"
)

// Config is everything about a run that is not the rule itself.
type Config struct {
	// Direction reads the rule as an entry to buy or to sell. Defaults to
	// long.
	Direction Direction `json:"direction"`

	// ExitRule optionally ends a position when its own conditions hold,
	// evaluated only while holding. Without it a position can only end on a
	// stop, a target, or the hold limit — which describes a great many
	// strategies but not the ones whose exit is a signal in its own right.
	ExitRule *algo.Algorithm `json:"exit_rule,omitempty"`

	// SizeMode and its parameters. Defaults to full.
	SizeMode SizeMode `json:"size_mode"`
	// SizePct is the share of the account per trade under SizeFraction.
	SizePct float64 `json:"size_pct"`
	// RiskPct is the share of the account lost if the stop is hit, under
	// SizeRisk. Requires a stop: without one there is no defined loss to size
	// against.
	RiskPct float64 `json:"risk_pct"`

	// StopLossPct exits when price falls this far below entry. Zero disables.
	StopLossPct float64 `json:"stop_loss_pct"`
	// TakeProfitPct exits when price rises this far above entry. Zero disables.
	TakeProfitPct float64 `json:"take_profit_pct"`
	// MaxHoldBars exits after this many bars regardless, counted from the
	// entry bar inclusive: a limit of one opens at that bar's open and closes
	// at its own close. Zero disables, which
	// means a position with no stop and no target is held to the end of the
	// data — usually a sign the operator has not finished describing the
	// strategy, so it is reported rather than silently allowed.
	MaxHoldBars int `json:"max_hold_bars"`
	// CostBps is charged on entry and again on exit, in basis points of
	// notional. It stands in for brokerage, STT, exchange fees and slippage
	// together, because separating them implies a precision this does not
	// have.
	CostBps float64 `json:"cost_bps"`
}

// DefaultCostBps is the round-trip-per-side assumption for Indian cash
// equity when none is given.
//
// Ten basis points a side is a deliberately unkind number — discount
// brokerage is near zero, but STT, exchange and SEBI fees, stamp duty and
// GST land around 5-6bps a side on delivery, and slippage on a market order
// in anything outside the most liquid names covers the rest. A strategy that
// only works below this was never going to work.
const DefaultCostBps = 10

// DefaultCostBpsUS is the same assumption for US cash equity, which has none
// of the India-specific taxes above: no STT, no stamp duty, no GST. What is
// left is commission (at most brokers, zero) and slippage, which even a
// deliberately unkind number puts in the low single digits rather than
// double.
const DefaultCostBpsUS = 3

// DefaultCostBpsFor picks the default by the symbol's own venue, so a
// backtest never charges an Indian tax regime against a US fill or the
// reverse.
func DefaultCostBpsFor(sym marketdata.Symbol) float64 {
	if sym.IsIndian() {
		return DefaultCostBps
	}
	return DefaultCostBpsUS
}

// Trade is one completed round trip.
type Trade struct {
	Direction Direction `json:"direction"`
	// Size is the share of the account committed, 0-1. Reported because a 40%
	// gain on a tenth of the account and the same gain on all of it are not
	// the same trade, and only this distinguishes them.
	Size       float64   `json:"size"`
	EntryTime  time.Time `json:"entry_time"`
	EntryPrice float64   `json:"entry_price"`
	ExitTime   time.Time `json:"exit_time"`
	ExitPrice  float64   `json:"exit_price"`
	Bars       int       `json:"bars"`
	// ReturnPct is the return on the notional committed, net of costs on both
	// sides. It is not the effect on the account: that is this scaled by Size.
	ReturnPct float64 `json:"return_pct"`
	// AccountPct is what the trade did to the account, which is ReturnPct
	// times Size. With fractional sizing the two diverge sharply and reading
	// the wrong one is how a backtest gets believed.
	AccountPct float64 `json:"account_pct"`
	// Reason is what closed the position: stop, target, time, or end of data.
	Reason string `json:"reason"`
	// Ambiguous marks a bar whose high reached the target and whose low
	// reached the stop. Daily OHLC cannot say which came first, so the stop is
	// assumed and the trade is flagged rather than quietly counted as a win.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// Stats is the performance summary.
type Stats struct {
	Trades   int     `json:"trades"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	WinRate  float64 `json:"win_rate"`
	TotalPct float64 `json:"total_pct"`
	// CAGR is annualised from the actual elapsed calendar time of the window.
	CAGR       float64 `json:"cagr"`
	AvgWinPct  float64 `json:"avg_win_pct"`
	AvgLossPct float64 `json:"avg_loss_pct"`
	// ProfitFactor is gross gains over gross losses. Infinite when there are
	// no losses, which is reported as zero and should be read alongside the
	// trade count rather than as a triumph.
	ProfitFactor float64 `json:"profit_factor"`
	MaxDrawdown  float64 `json:"max_drawdown"`
	// Sharpe is annualised from per-bar equity returns at a zero risk-free
	// rate. With few trades it is close to meaningless and the trade count is
	// the number to look at first.
	Sharpe float64 `json:"sharpe"`
	// ExposurePct is the share of bars spent holding a position. A strategy
	// that beats buy-and-hold while in the market a tenth of the time is a
	// different proposition from one that is always in.
	ExposurePct float64 `json:"exposure_pct"`
	AvgBars     float64 `json:"avg_bars"`
	// BuyHoldPct is the same window, bought at the first tradeable open and
	// held. Without it a return figure means nothing.
	BuyHoldPct float64 `json:"buy_hold_pct"`
	Ambiguous  int     `json:"ambiguous"`
}

// EquityPoint is one step of the equity curve, indexed by bar time.
type EquityPoint struct {
	Time   time.Time `json:"t"`
	Equity float64   `json:"e"`
	// Hold is buy-and-hold rebased to the same starting capital, so the two
	// curves can be read on one axis.
	Hold float64 `json:"h"`
}

// Result is one symbol backtested.
type Result struct {
	Symbol   string        `json:"symbol"`
	Interval string        `json:"interval"`
	From     time.Time     `json:"from"`
	To       time.Time     `json:"to"`
	Bars     int           `json:"bars"`
	Stats    Stats         `json:"stats"`
	Trades   []Trade       `json:"trades"`
	Equity   []EquityPoint `json:"equity"`
	// Warning names something that should temper how the result is read: too
	// few bars, too few trades, no exit rule. Empty when there is nothing to
	// say.
	Warnings []string `json:"warnings,omitempty"`
}

// startEquity is nominal. Every figure reported is a ratio, so the number
// itself never reaches the operator.
const startEquity = 100.0

// minBarsForResult is the shortest window worth reporting on.
//
// Not a technical limit. Below this the statistics are arithmetic performed on
// noise, and presenting them next to the same labels a five-year run uses
// invites them to be read the same way.
const minBarsForResult = 60

// Run evaluates one algorithm across one symbol's history.
//
// The evaluator is handed a growing prefix of the candles rather than an index
// into the whole series, which is what makes look-ahead impossible instead of
// merely unintended. It costs a recomputation of the indicators at every bar;
// on a few thousand bars that is milliseconds, and it removes the entire class
// of bug where a backtest quietly reads tomorrow.
// The evaluator is passed in rather than constructed here, so a backtest is
// guaranteed to use the very instance the live alert engine uses — same
// exchange timezone, same session handling. A lookalike built locally could
// drift from it silently, and a backtest that evaluates differently from the
// engine is measuring a strategy nobody is running.
func Run(
	a *algo.Algorithm,
	sym marketdata.Symbol,
	candles []marketdata.Candle,
	cfg Config,
	ev *algo.Evaluator,
) (Result, error) {
	res := Result{
		Symbol:   sym.String(),
		Interval: a.Interval,
		Bars:     len(candles),
	}
	if len(candles) == 0 {
		return res, fmt.Errorf("backtest: no candles for %s", sym)
	}

	// Sorted ascending, because everything below indexes forward through time
	// and a provider returning newest-first would silently invert the run.
	sort.Slice(candles, func(i, j int) bool { return candles[i].Time.Before(candles[j].Time) })
	res.From = candles[0].Time
	res.To = candles[len(candles)-1].Time

	if cfg.CostBps < 0 {
		cfg.CostBps = 0
	}
	cost := cfg.CostBps / 10_000
	dir := cfg.Direction
	if dir != Short {
		dir = Long
	}
	size := sizeFor(cfg)

	// The first bar index at which every indicator in the rule is defined.
	//
	// RequiredBars is a count of bars, not an index: a rule needing three bars
	// is first evaluable at index 2. Using the count directly started the run
	// one bar late, which on a short series meant no trades at all.
	warmup := a.RequiredBars() - 1
	// An exit rule has its own warm-up, and a position that opens before its
	// exit can be evaluated would be held on a condition nobody could read.
	if cfg.ExitRule != nil {
		if w := cfg.ExitRule.RequiredBars() - 1; w > warmup {
			warmup = w
		}
	}
	if warmup < 0 {
		warmup = 0
	}
	if warmup >= len(candles) {
		res.Warnings = []string{fmt.Sprintf(
			"This rule needs %d bars before it can be evaluated and only %d were available.",
			a.RequiredBars(), len(candles))}
		return res, nil
	}

	var (
		trades   []Trade
		equity   = startEquity
		curve    []EquityPoint
		holding  bool
		entryPx  float64
		entryAt  time.Time
		heldBars int
	)

	// Buy and hold, filled the same way the strategy is filled.
	//
	// The earliest bar the strategy can be *in* the market is the one after
	// the first evaluable bar, and the benchmark buys at that same open. Any
	// other convention hands one side a head start: buying at the first bar of
	// data gives the benchmark moves the strategy could never have caught,
	// and buying at a close gives it a price the strategy could not transact
	// at.
	firstFill := warmup + 1
	holdEntry := 0.0
	if firstFill < len(candles) {
		holdEntry = candles[firstFill].Open
		if holdEntry <= 0 {
			holdEntry = candles[firstFill].Close
		}
	}

	// book closes a position and records it.
	book := func(exitPx float64, at time.Time, reason string, ambiguous bool) {
		r := tradeReturn(dir, entryPx, exitPx, cost)
		equity *= 1 + size*r
		trades = append(trades, Trade{
			Direction:  dir,
			Size:       size,
			EntryTime:  entryAt,
			EntryPrice: entryPx,
			ExitTime:   at,
			ExitPrice:  exitPx,
			Bars:       heldBars,
			ReturnPct:  r * 100,
			AccountPct: size * r * 100,
			Reason:     reason,
			Ambiguous:  ambiguous,
		})
		holding = false
		heldBars = 0
	}

	for i := warmup; i < len(candles); i++ {
		bar := candles[i]

		if holding {
			heldBars++
			// Price-level exits first: a stop is an order resting in the
			// market and fills during the bar, whereas a rule can only be read
			// once the bar has closed.
			if exitPx, reason, ambiguous := exitFor(bar, dir, entryPx, heldBars, cfg); reason != "" {
				book(exitPx, bar.Time, reason, ambiguous)
			} else if cfg.ExitRule != nil {
				// An exit signal on this bar is acted on at the next open, for
				// exactly the reason an entry is: the close is not knowable
				// until the bar has closed.
				out := ev.Evaluate(cfg.ExitRule, sym, candles[:i+1])
				if out.Triggered() && i+1 < len(candles) {
					next := candles[i+1]
					px := next.Open
					if px <= 0 {
						px = next.Close
					}
					if px > 0 {
						heldBars++
						book(px, next.Time, "exit rule", false)
					}
				}
			}
		}

		// Only look for an entry when flat. One position at a time: pyramiding
		// changes what the result means, and a strategy that has not been
		// tested flat-to-flat cannot be reasoned about at all.
		if !holding {
			// The evaluator sees candles[0..i] and nothing after.
			out := ev.Evaluate(a, sym, candles[:i+1])
			// Enter at the NEXT bar's open. The close of bar i is only known
			// once bar i has closed, so bar i's own prices are not available
			// to act on.
			if out.Triggered() && i+1 < len(candles) {
				next := candles[i+1]
				px := next.Open
				if px <= 0 {
					px = next.Close
				}
				if px > 0 {
					holding = true
					entryPx = px
					entryAt = next.Time
					heldBars = 0
				}
			}
		}

		// Uninvested until the benchmark's own fill, so it never shows a move
		// over a period it was not in the market for.
		hold := startEquity
		if holdEntry > 0 && i >= firstFill {
			hold = startEquity * (bar.Close / holdEntry)
		}
		// While holding, equity marks to market so the drawdown reflects what
		// the operator would actually have watched, not just closed trades.
		shown := equity
		if holding && entryPx > 0 {
			shown = equity * (1 + size*tradeReturn(dir, entryPx, bar.Close, cost))
		}
		curve = append(curve, EquityPoint{Time: bar.Time, Equity: shown, Hold: hold})
	}

	// An open position at the end of the data is closed at the last close and
	// labelled, rather than being dropped. Dropping it hides losing trades
	// that had not yet been cut, which is exactly the population a reader
	// needs to see.
	if holding {
		last := candles[len(candles)-1]
		book(last.Close, last.Time, "still open at the end of the data", false)
	}

	res.Trades = trades
	res.Equity = curve
	res.Stats = summarise(trades, curve, candles, warmup, holdEntry)
	res.Warnings = warn(res, cfg, len(candles))
	return res, nil
}

// tradeReturn is the return on committed notional, net of costs on both legs.
//
// Costs are charged against each transaction's own value rather than as a flat
// haircut, which is what makes the two directions exactly symmetric:
//
//	long  N at P, out at X:  N·(X/P) − N − N·c − N·(X/P)·c
//	short N at P, back at X: N − N·(X/P) − N·c − N·(X/P)·c
//
// Divided through by N those give the two expressions below. Both return
// −2c on a flat round trip, as they must.
func tradeReturn(dir Direction, entry, exit, c float64) float64 {
	if entry <= 0 {
		return 0
	}
	ratio := exit / entry
	if dir == Short {
		return (1 - c) - ratio*(1+c)
	}
	return ratio*(1-c) - (1 + c)
}

// exitFor decides whether this bar closes an open position, and at what price.
//
// Stops and targets are checked against the bar's low and high rather than its
// close, because a stop is an order resting in the market and does not wait
// for the close to be filled.
func exitFor(bar marketdata.Candle, dir Direction, entry float64, held int, cfg Config) (px float64, reason string, ambiguous bool) {
	// A stop protects against the direction the position loses in, so it sits
	// below entry for a long and above it for a short. Getting this backwards
	// produces a short book that appears to stop out into profit, which reads
	// as a brilliant strategy.
	stop, target := 0.0, 0.0
	if dir == Short {
		if cfg.StopLossPct > 0 {
			stop = entry * (1 + cfg.StopLossPct/100)
		}
		if cfg.TakeProfitPct > 0 {
			target = entry * (1 - cfg.TakeProfitPct/100)
		}
	} else {
		if cfg.StopLossPct > 0 {
			stop = entry * (1 - cfg.StopLossPct/100)
		}
		if cfg.TakeProfitPct > 0 {
			target = entry * (1 + cfg.TakeProfitPct/100)
		}
	}

	var hitStop, hitTarget bool
	if dir == Short {
		hitStop = stop > 0 && bar.High >= stop
		hitTarget = target > 0 && bar.Low <= target
	} else {
		hitStop = stop > 0 && bar.Low <= stop
		hitTarget = target > 0 && bar.High >= target
	}

	switch {
	case hitStop && hitTarget:
		// Both levels traded inside one bar. OHLC does not record the order
		// they were reached, and assuming the target would turn every volatile
		// bar into a win. The stop is assumed and the trade is flagged so the
		// count of such bars is visible.
		return stop, "stop loss", true
	case hitStop:
		return stop, "stop loss", false
	case hitTarget:
		return target, "take profit", false
	case cfg.MaxHoldBars > 0 && held >= cfg.MaxHoldBars:
		return bar.Close, "held to limit", false
	}
	return 0, "", false
}

// sizeFor is the share of the account one position commits.
//
// Capped at 1 throughout: this models a cash account, and a risk setting that
// implies leverage is clamped rather than silently honoured, because a
// backtest quietly running at 3x is not describing the account the operator
// has.
func sizeFor(cfg Config) float64 {
	switch cfg.SizeMode {
	case SizeFraction:
		f := cfg.SizePct / 100
		if f <= 0 {
			return 1
		}
		return math.Min(f, 1)
	case SizeRisk:
		// Risking r% of the account with a stop s% away means committing r/s
		// of the account: the stop converts a position size into a known loss,
		// which is the entire point of sizing this way.
		if cfg.RiskPct <= 0 || cfg.StopLossPct <= 0 {
			return 1
		}
		return math.Min(cfg.RiskPct/cfg.StopLossPct, 1)
	default:
		return 1
	}
}

func summarise(
	trades []Trade,
	curve []EquityPoint,
	candles []marketdata.Candle,
	warmup int,
	holdEntry float64,
) Stats {
	var s Stats
	s.Trades = len(trades)

	var grossWin, grossLoss, sumWin, sumLoss, sumBars float64
	for _, t := range trades {
		sumBars += float64(t.Bars)
		if t.Ambiguous {
			s.Ambiguous++
		}
		if t.ReturnPct > 0 {
			s.Wins++
			sumWin += t.ReturnPct
			grossWin += t.ReturnPct
		} else {
			s.Losses++
			sumLoss += t.ReturnPct
			grossLoss += -t.ReturnPct
		}
	}
	if s.Trades > 0 {
		s.WinRate = float64(s.Wins) / float64(s.Trades) * 100
		s.AvgBars = sumBars / float64(s.Trades)
	}
	if s.Wins > 0 {
		s.AvgWinPct = sumWin / float64(s.Wins)
	}
	if s.Losses > 0 {
		s.AvgLossPct = sumLoss / float64(s.Losses)
	}
	if grossLoss > 0 {
		s.ProfitFactor = grossWin / grossLoss
	}

	if len(curve) > 0 {
		last := curve[len(curve)-1]
		s.TotalPct = (last.Equity/startEquity - 1) * 100
		if holdEntry > 0 {
			s.BuyHoldPct = (last.Hold/startEquity - 1) * 100
		}

		peak := curve[0].Equity
		for _, p := range curve {
			if p.Equity > peak {
				peak = p.Equity
			}
			if peak > 0 {
				if dd := (p.Equity/peak - 1) * 100; dd < s.MaxDrawdown {
					s.MaxDrawdown = dd
				}
			}
		}

		// Annualised from elapsed calendar time, not bar count: 250 hourly
		// bars and 250 daily bars cover very different periods and must not
		// annualise the same way.
		years := curve[len(curve)-1].Time.Sub(curve[0].Time).Hours() / (24 * 365.25)
		if years > 0.08 && last.Equity > 0 {
			s.CAGR = (math.Pow(last.Equity/startEquity, 1/years) - 1) * 100
		}
		s.Sharpe = sharpe(curve, years)
	}

	// Exposure counts bars spent holding against bars the strategy was able to
	// trade at all.
	tradeable := len(candles) - warmup
	if tradeable > 0 {
		var held float64
		for _, t := range trades {
			held += float64(t.Bars)
		}
		s.ExposurePct = held / float64(tradeable) * 100
	}
	return s
}

// sharpe is the annualised ratio of mean per-bar equity return to its standard
// deviation, at a zero risk-free rate.
func sharpe(curve []EquityPoint, years float64) float64 {
	if len(curve) < 3 || years <= 0 {
		return 0
	}
	rets := make([]float64, 0, len(curve)-1)
	for i := 1; i < len(curve); i++ {
		prev := curve[i-1].Equity
		if prev <= 0 {
			continue
		}
		rets = append(rets, curve[i].Equity/prev-1)
	}
	if len(rets) < 2 {
		return 0
	}
	var mean float64
	for _, r := range rets {
		mean += r
	}
	mean /= float64(len(rets))

	var variance float64
	for _, r := range rets {
		variance += (r - mean) * (r - mean)
	}
	variance /= float64(len(rets) - 1)
	sd := math.Sqrt(variance)
	if sd <= 0 {
		return 0
	}
	// Bars per year, derived from the window rather than assumed, so the same
	// code annualises daily and hourly runs correctly.
	perYear := float64(len(rets)) / years
	return (mean / sd) * math.Sqrt(perYear)
}

// warn names the reasons a result should not be read at face value.
//
// These are the things a reader would otherwise have to notice for themselves,
// and the whole failure mode of backtesting is not noticing them.
func warn(res Result, cfg Config, bars int) []string {
	var out []string
	if bars < minBarsForResult {
		out = append(out, fmt.Sprintf(
			"Only %d bars. Every figure below is arithmetic on a sample too short to describe a strategy.", bars))
	}
	switch n := res.Stats.Trades; {
	case n == 0:
		out = append(out, "The rule never triggered in this window, so there is nothing to measure.")
	case n < 10:
		out = append(out, fmt.Sprintf(
			"%d trades. Win rate and profit factor need far more than this before they mean anything.", n))
	}
	if cfg.StopLossPct <= 0 && cfg.TakeProfitPct <= 0 && cfg.MaxHoldBars <= 0 && cfg.ExitRule == nil {
		out = append(out, "No exit rule was set, so each position was held to the end of the data. That measures the entry signal, not a strategy.")
	}
	if cfg.SizeMode == SizeRisk && cfg.StopLossPct <= 0 {
		out = append(out, "Risk-based sizing needs a stop to size against — without one there is no defined loss, so the whole account was committed instead.")
	}
	if cfg.SizeMode == SizeRisk && cfg.RiskPct > 0 && cfg.StopLossPct > 0 &&
		cfg.RiskPct/cfg.StopLossPct > 1 {
		out = append(out, fmt.Sprintf(
			"Risking %.1f%% with a %.1f%% stop implies %.0f%% of the account per trade. This is a cash account, so it was capped at 100%%.",
			cfg.RiskPct, cfg.StopLossPct, cfg.RiskPct/cfg.StopLossPct*100))
	}
	if cfg.Direction == Short {
		out = append(out, "Short results assume the borrow was available and free. Neither is guaranteed, and in this market most of this universe cannot be shorted overnight at all.")
	}
	if res.Stats.Ambiguous > 0 {
		out = append(out, fmt.Sprintf(
			"%d trade(s) hit both the stop and the target inside one bar. OHLC cannot say which came first; the stop was assumed.",
			res.Stats.Ambiguous))
	}
	if cfg.CostBps <= 0 {
		out = append(out, "Costs were set to zero. This is not an optimistic estimate — it is a strategy that trades for free.")
	}
	if res.Stats.Trades > 0 && res.Stats.TotalPct < res.Stats.BuyHoldPct {
		out = append(out, fmt.Sprintf(
			"Buy and hold returned %.1f%% over the same window against the strategy's %.1f%%.",
			res.Stats.BuyHoldPct, res.Stats.TotalPct))
	}
	return out
}
