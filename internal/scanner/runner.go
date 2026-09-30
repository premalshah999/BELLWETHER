package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Store is the persistence the runner needs.
type Store interface {
	SaveScan(ctx context.Context, res Result) (int64, error)
	MarkExplained(ctx context.Context, findingID int64, explained bool) error
	// SaveCandles persists one symbol's daily bars -- the same method the
	// market-data router uses for a chart's cache, reused here for the
	// event study engine's price-history prerequisite rather than adding a
	// second write path for what is, once decoded, the same data shape.
	SaveCandles(ctx context.Context, sym marketdata.Symbol, interval marketdata.Interval, source string, bars marketdata.Bars, requestedLimit int) error
}

// Explainer reports whether the archive already accounts for a move.
type Explainer interface {
	// ExplainsMove reports whether anything known about a symbol since a
	// given time would account for unusual trading.
	ExplainsMove(ctx context.Context, symbol string, since time.Time) (bool, error)
}

// Observer raises attention for a symbol by an explicit weight. This is
// news.Tracker.ObserveSignal.
type Observer func(symbols, sectors []string, weight float64, reason string)

// Runner performs scans and routes what they find.
//
// The routing is the point. A scan that only produces a list is a screener;
// what makes this an intelligence component is that an unexplained anomaly
// raises that instrument's polling priority, so the system starts looking
// harder at a name *because the market did something*, not because an article
// happened to arrive.
type Runner struct {
	runMu     sync.Mutex
	mu        sync.RWMutex
	attention []string

	Client    *Client
	Store     Store
	Explainer Explainer
	Observe   Observer
	Universe  func() []marketdata.Symbol
	Log       *slog.Logger

	// OnFinding is called for each anomaly as the scan resolves it, so a
	// screen can show findings arriving rather than waiting for the whole
	// universe to finish. A full pass takes around a hundred seconds.
	OnFinding func(Finding)

	// OnAttention is called once a scan has decided which instruments need
	// searching. Without it those names wait for the next periodic source
	// sync, which is up to five minutes of doing nothing about the one
	// signal the whole scanner exists to produce.
	OnAttention func()

	// MaxFindings caps how many anomalies one scan may promote. A scanner
	// that escalates eighty names has escalated nothing.
	MaxFindings int

	// MaxSearchTargets caps how many instruments a scan may put under
	// targeted search. Each one becomes a recurring query against a third
	// party for as long as it stays on the list, so this is a courtesy limit
	// as much as a relevance one.
	MaxSearchTargets int
}

// AttentionSymbols returns the instruments the last scan could not explain,
// most extreme first.
//
// These are the names worth searching for a reason, which is the second half
// of the loop: the scanner establishes that something happened, and targeted
// search goes looking for what.
func (r *Runner) AttentionSymbols() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.attention...)
}

// Attention weights, in the units the tracker uses: 2.0 is its warm
// threshold and 6.0 its hot one.
//
// An unexplained move earns more than an explained one, which is the
// inversion that matters: once the archive accounts for a move there is
// nothing left to find, while a move nobody has explained is exactly where
// searching pays.
//
// weightUnexplained clears warm on its own, so a single scan is enough to
// make the scheduler start looking harder. It stops short of hot, which is
// reserved for the extremes that the score bonus below can reach.
const (
	weightUnexplained = 2.5
	weightExplained   = 0.8
	// weightScoreBonus scales with how extreme the finding is, so a 7 sigma
	// volume event outranks a marginal one instead of both arriving as the
	// same nudge. Capped so a single outlier cannot pin an instrument hot
	// for the rest of the day.
	weightScoreBonus = 0.25
	weightBonusMax   = 3.5
	// explainWindow is how far back the archive is consulted. A move today
	// is rarely explained by something from last week, and widening the
	// window mostly finds coincidences.
	explainWindow = 36 * time.Hour
)

// Run scans the universe once and routes its findings.
func (r *Runner) Run(ctx context.Context) (Result, error) {
	if !r.runMu.TryLock() {
		return Result{}, fmt.Errorf("scanner: a scan is already running")
	}
	defer r.runMu.Unlock()
	universe := r.Universe()
	max := r.MaxFindings
	if max <= 0 {
		max = 40
	}

	res, err := r.Client.Scan(ctx, universe, max)
	if err != nil {
		return Result{}, err
	}

	r.persistSeries(ctx, res.Series)

	// Resolved before the scan is stored rather than by a second pass, so a
	// stored scan is never missing the one field that makes its findings
	// actionable.
	var unexplained, explained int
	for i := range res.Findings {
		if r.Explainer == nil {
			continue
		}
		since := res.AsOf.Add(-explainWindow)
		known, err := r.Explainer.ExplainsMove(ctx, res.Findings[i].Symbol, since)
		if err != nil {
			r.log().Warn("could not check archive", "symbol", res.Findings[i].Symbol, "err", err)
			continue
		}
		res.Findings[i].Explained = &known
		if known {
			explained++
		} else {
			unexplained++
		}
	}

	scanID, err := r.Store.SaveScan(ctx, res)
	if err != nil {
		// A scan that cannot be stored is still worth acting on: the
		// attention it raises matters more than the history it leaves.
		r.log().Error("could not store scan", "err", err)
	}

	for _, f := range res.Findings {
		known := f.Explained != nil && *f.Explained

		weight := weightUnexplained
		if known {
			weight = weightExplained
		}
		if bonus := f.Score * weightScoreBonus; bonus > 0 {
			weight += math.Min(bonus, weightBonusMax)
		}

		if r.OnFinding != nil {
			r.OnFinding(f)
		}

		if r.Observe != nil {
			// No sector heat. A single company's unusual volume says little
			// about its industry, and promoting the sector would elevate a
			// hundred companies on one data point.
			r.Observe([]string{f.Symbol}, nil, weight, r.reason(f, known))
		}
	}

	targets := r.MaxSearchTargets
	if targets <= 0 {
		targets = 10
	}
	var attention []string
	for _, f := range res.Findings {
		if len(attention) >= targets {
			break
		}
		// Only the unexplained ones, and only those whose signals are of a
		// kind that tends to have a findable reason. Searching for the cause
		// of a slow drift toward a 52-week high produces nothing but noise.
		if (f.Explained == nil || !*f.Explained) && f.HasDiscoverableCause() {
			attention = append(attention, f.Symbol)
		}
	}
	r.mu.Lock()
	r.attention = attention
	r.mu.Unlock()

	if r.OnAttention != nil && len(attention) > 0 {
		r.OnAttention()
	}

	r.log().Info("scan complete",
		"universe", res.Universe, "scanned", res.Scanned, "failed", res.Failed,
		"findings", len(res.Findings), "unexplained", unexplained,
		"explained", explained, "searching", len(attention),
		"elapsed", res.Elapsed, "scan_id", scanID)

	return res, nil
}

// persistSeries writes each symbol's freshly-fetched daily bars to the
// candle store -- see Result.Series's doc for why this exists. Best-effort:
// a symbol whose write fails is logged and skipped rather than failing the
// scan, since the findings this scan exists to produce do not depend on it.
func (r *Runner) persistSeries(ctx context.Context, series map[marketdata.Symbol][]marketdata.Candle) {
	if len(series) == 0 || r.Store == nil {
		return
	}
	var saved, failed int
	for sym, candles := range series {
		bars := marketdata.Bars{Candles: candles}
		if err := r.Store.SaveCandles(ctx, sym, marketdata.Interval1d, "yfinance-scan", bars, len(candles)); err != nil {
			failed++
			r.log().Warn("could not persist scan series", "symbol", sym.String(), "err", err)
			continue
		}
		saved++
	}
	r.log().Info("scan series persisted", "symbols", saved, "failed", failed)
}

func (r *Runner) reason(f Finding, known bool) string {
	names := make([]string, 0, len(f.Signals))
	for _, s := range f.Signals {
		names = append(names, string(s))
	}
	prefix := "scanner: "
	if !known {
		prefix = "scanner (unexplained): "
	}
	return prefix + strings.Join(names, ", ")
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
