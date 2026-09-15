package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/backtest"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

// maxBacktestBars caps how much history one run may pull per symbol.
//
// The evaluator is re-run at every bar, so the work is quadratic in the window.
// Two thousand daily bars is roughly eight years, which is more history than
// most of this universe has and well inside a request's patience.
const maxBacktestBars = 2000

// maxBacktestSymbols caps the breadth of one run.
//
// Not a performance limit so much as an interpretive one: a rule tested across
// two hundred instruments will always show a handful of spectacular results,
// and reading those as evidence is the most reliable way to lose money with a
// backtester.
const maxBacktestSymbols = 40

type backtestRequest struct {
	// Algorithm is an unsaved rule. Ignored when the route carries an id.
	Algorithm *algo.Algorithm `json:"algorithm,omitempty"`
	// Symbols and WatchlistIDs override the rule's own targets when present,
	// so a saved rule can be tested against something it does not watch.
	Symbols      []string        `json:"symbols,omitempty"`
	WatchlistIDs []int64         `json:"watchlist_ids,omitempty"`
	Bars         int             `json:"bars"`
	Config       backtest.Config `json:"config"`
}

// backtestResponse carries every symbol's run plus what they add up to.
type backtestResponse struct {
	Results []backtest.Result `json:"results"`
	Summary backtestSummary   `json:"summary"`
	// Skipped names instruments that could not be tested and why, rather than
	// letting them vanish and leave the operator reading a narrower result
	// than they asked for without knowing it.
	Skipped []skipped `json:"skipped,omitempty"`
	Elapsed string    `json:"elapsed"`
}

type skipped struct {
	Symbol string `json:"symbol"`
	Reason string `json:"reason"`
}

// backtestSummary aggregates across symbols.
//
// Equal-weighted across instruments rather than compounded into one equity
// curve: the runs are independent single-position tests, and stitching them
// together would imply a portfolio that was never simulated.
type backtestSummary struct {
	Symbols     int     `json:"symbols"`
	Trades      int     `json:"trades"`
	WinRate     float64 `json:"win_rate"`
	AvgTotalPct float64 `json:"avg_total_pct"`
	AvgBuyHold  float64 `json:"avg_buy_hold_pct"`
	// Beat is how many instruments the rule out-returned buy-and-hold on. The
	// honest headline for a multi-symbol run, because an average is carried by
	// its outliers and this is not.
	Beat            int     `json:"beat"`
	WorstDrawdown   float64 `json:"worst_drawdown"`
	MedianTotalPct  float64 `json:"median_total_pct"`
	MedianBuyHold   float64 `json:"median_buy_hold_pct"`
	TotalAmbiguous  int     `json:"total_ambiguous"`
	SymbolsNoTrades int     `json:"symbols_no_trades"`
}

func (s *Server) handleBacktest(w http.ResponseWriter, r *http.Request) {
	var body backtestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "That backtest request could not be read.")
		return
	}
	a := body.Algorithm
	if a == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "A rule is required.")
		return
	}
	s.runBacktest(w, r, a, body)
}

// handleBacktestSaved tests a stored rule.
func (s *Server) handleBacktestSaved(w http.ResponseWriter, r *http.Request) {
	id, ok := s.algorithmID(w, r)
	if !ok {
		return
	}
	var body backtestRequest
	// An empty body is a valid request: test this rule, as saved, on its own
	// targets with the defaults.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "That backtest request could not be read.")
			return
		}
	}
	a, err := s.deps.Store.GetAlgorithm(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such algorithm.")
		return
	}
	if err != nil {
		s.deps.Log.Error("could not read algorithm for backtest", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read that algorithm.")
		return
	}
	s.runBacktest(w, r, a, body)
}

func (s *Server) runBacktest(w http.ResponseWriter, r *http.Request, a *algo.Algorithm, body backtestRequest) {
	if s.deps.Evaluator == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The evaluator is not available.")
		return
	}

	// The request's targets are adopted onto a copy of the rule before it is
	// validated.
	//
	// Validation requires a rule to name somewhere to look, which is right for
	// a saved rule and wrong here: testing an ad-hoc rule against a watchlist
	// chosen in the request is the normal way this endpoint is used, and
	// validating the bare rule rejected exactly that.
	if len(body.Symbols) > 0 || len(body.WatchlistIDs) > 0 {
		clone := *a
		clone.Symbols = body.Symbols
		clone.WatchlistIDs = body.WatchlistIDs
		a = &clone
	}

	if err := a.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid_algorithm",
			"message": "That rule is not valid, so there is nothing to test.",
			"detail":  err.Error(),
		})
		return
	}

	// The exit rule is a rule in its own right and gets the same scrutiny. An
	// unvalidated one would evaluate to "insufficient data" on every bar and
	// silently never fire, which looks exactly like a strategy that simply
	// never wanted to exit.
	if body.Config.ExitRule != nil {
		ex := body.Config.ExitRule
		// It inherits the entry rule's interval and instruments; it is a
		// condition, not a separate schedule, and validation requires both.
		ex.Interval = a.Interval
		ex.Symbols = a.Symbols
		ex.WatchlistIDs = a.WatchlistIDs
		if ex.Name == "" {
			ex.Name = a.Name + " exit"
		}
		if err := ex.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "invalid_exit_rule",
				"message": "That exit rule is not valid.",
				"detail":  err.Error(),
			})
			return
		}
	}

	interval, err := marketdata.ParseInterval(a.Interval)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "That rule has an interval this system does not serve.")
		return
	}

	// Targets: what the request named, else what the rule watches.
	targets := body.Symbols
	if len(body.WatchlistIDs) > 0 {
		if store, ok := s.watchlistStore(); ok {
			for _, id := range body.WatchlistIDs {
				syms, err := store.WatchlistSymbols(r.Context(), id)
				if err != nil {
					s.deps.Log.Warn("could not read a watchlist for a backtest", "watchlist", id, "err", err)
					continue
				}
				for _, sym := range syms {
					targets = append(targets, sym.String())
				}
			}
		}
	}
	if len(targets) == 0 {
		targets = s.algorithmTargets(r.Context(), a)
	}
	targets = dedupe(targets)
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request",
			"This rule names no instruments and no watchlist, so there is nothing to test it on.")
		return
	}
	if len(targets) > maxBacktestSymbols {
		targets = targets[:maxBacktestSymbols]
	}

	bars := body.Bars
	if bars <= 0 {
		bars = 500
	}
	if bars > maxBacktestBars {
		bars = maxBacktestBars
	}
	// Enough extra history for the slowest indicator in either rule to be
	// defined before the first bar anyone is asked to read a result from.
	warm := a.RequiredBars()
	if body.Config.ExitRule != nil {
		if w := body.Config.ExitRule.RequiredBars(); w > warm {
			warm = w
		}
	}
	bars += warm

	cfg := body.Config
	// A request that never mentions costs gets real ones. Defaulting to zero
	// would make the friendliest possible assumption silently, which is
	// exactly the assumption a backtester must not make on its own.
	if cfg.CostBps == 0 && !body.mentionsCost() {
		cfg.CostBps = backtest.DefaultCostBps
	}

	start := time.Now()
	var (
		mu      sync.Mutex
		results []backtest.Result
		skips   []skipped
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 6)
	)

	for _, raw := range targets {
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			skips = append(skips, skipped{Symbol: raw, Reason: "not a symbol this system recognises"})
			continue
		}
		wg.Add(1)
		go func(sym marketdata.Symbol) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			series, err := s.deps.Router.Candles(r.Context(), sym, interval, bars)
			if err != nil {
				mu.Lock()
				skips = append(skips, skipped{Symbol: sym.String(), Reason: "no price history available"})
				mu.Unlock()
				return
			}
			res, err := backtest.Run(a, sym, series.Candles, cfg, s.deps.Evaluator)
			if err != nil {
				mu.Lock()
				skips = append(skips, skipped{Symbol: sym.String(), Reason: err.Error()})
				mu.Unlock()
				return
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}(sym)
	}
	wg.Wait()

	// Worst first: a reader scanning a multi-symbol run should meet the
	// failures before the successes, not have to scroll to them.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Stats.TotalPct < results[j].Stats.TotalPct
	})

	writeJSON(w, http.StatusOK, backtestResponse{
		Results: results,
		Summary: summariseBacktests(results),
		Skipped: skips,
		Elapsed: time.Since(start).Round(time.Millisecond).String(),
	})
}

// mentionsCost reports whether the caller said anything about costs at all.
//
// Zero is a meaningful choice ("show me the frictionless version") and also
// the zero value of an omitted field. Telling them apart is what lets an
// omitted config get realistic costs while an explicit zero is honoured.
func (b backtestRequest) mentionsCost() bool {
	return b.Config.CostBps != 0 || b.Config.StopLossPct != 0 ||
		b.Config.TakeProfitPct != 0 || b.Config.MaxHoldBars != 0 ||
		b.Config.Direction != "" || b.Config.SizeMode != "" || b.Config.ExitRule != nil
}

func summariseBacktests(results []backtest.Result) backtestSummary {
	var s backtestSummary
	s.Symbols = len(results)
	if len(results) == 0 {
		return s
	}

	var wins, totals, holds float64
	totalList := make([]float64, 0, len(results))
	holdList := make([]float64, 0, len(results))

	for _, r := range results {
		s.Trades += r.Stats.Trades
		wins += float64(r.Stats.Wins)
		totals += r.Stats.TotalPct
		holds += r.Stats.BuyHoldPct
		totalList = append(totalList, r.Stats.TotalPct)
		holdList = append(holdList, r.Stats.BuyHoldPct)
		s.TotalAmbiguous += r.Stats.Ambiguous
		if r.Stats.Trades == 0 {
			s.SymbolsNoTrades++
		}
		if r.Stats.TotalPct > r.Stats.BuyHoldPct {
			s.Beat++
		}
		if r.Stats.MaxDrawdown < s.WorstDrawdown {
			s.WorstDrawdown = r.Stats.MaxDrawdown
		}
	}

	if s.Trades > 0 {
		s.WinRate = wins / float64(s.Trades) * 100
	}
	n := float64(len(results))
	s.AvgTotalPct = totals / n
	s.AvgBuyHold = holds / n
	s.MedianTotalPct = median(totalList)
	s.MedianBuyHold = median(holdList)
	return s
}

// median is reported alongside the mean because a multi-symbol run's mean is
// routinely carried by one instrument, and the gap between the two is itself
// the most useful thing on the summary.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
