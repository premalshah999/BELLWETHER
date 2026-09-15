package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// aiUnavailable is the exact text an alert carries when no AI brief could be
// produced. It is spelled out rather than omitted so the operator can tell
// "the model had nothing to add" from "the model was never asked".
const aiUnavailable = "AI context unavailable"

// maxConcurrentSymbols bounds how many symbols one run evaluates at once.
// Evaluations read through the market data cache, but a cold cache turns into
// upstream requests, and a burst of those looks like abuse.
const maxConcurrentSymbols = 4

// Engine runs algorithms and turns triggers into notifications.
type Engine struct {
	algorithms AlgorithmStore
	alerts     AlertStore
	candles    CandleSource
	evaluator  *algo.Evaluator
	notifiers  []Notifier
	ai         AIContextProvider
	// watchlists expands attached lists into instruments. Optional: without
	// it a rule evaluates only the symbols named on it directly.
	watchlists WatchlistResolver
	log        *slog.Logger
	now        func() time.Time

	// mu serialises runs so two overlapping schedules cannot evaluate the
	// same algorithm concurrently and both slip past the cooldown check.
	mu sync.Mutex
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) EngineOption { return func(e *Engine) { e.log = l } }

// WithClock replaces the time source, used by tests to age cooldowns.
func WithClock(now func() time.Time) EngineOption { return func(e *Engine) { e.now = now } }

// WithAIContext attaches the optional brief provider.
func WithAIContext(p AIContextProvider) EngineOption { return func(e *Engine) { e.ai = p } }

// WithWatchlists lets algorithms point at named lists instead of fixed symbols.
func WithWatchlists(r WatchlistResolver) EngineOption {
	return func(e *Engine) { e.watchlists = r }
}

// NewEngine builds the alert engine.
func NewEngine(
	algorithms AlgorithmStore,
	alertStore AlertStore,
	candles CandleSource,
	evaluator *algo.Evaluator,
	notifiers []Notifier,
	opts ...EngineOption,
) *Engine {
	e := &Engine{
		algorithms: algorithms,
		alerts:     alertStore,
		candles:    candles,
		evaluator:  evaluator,
		notifiers:  notifiers,
		log:        slog.Default(),
		now:        func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// RunSummary reports what one scheduled pass did.
type RunSummary struct {
	Interval   string        `json:"interval"`
	Algorithms int           `json:"algorithms"`
	Evaluated  int           `json:"evaluated"`
	Triggered  int           `json:"triggered"`
	Suppressed int           `json:"suppressed"`
	Skipped    int           `json:"skipped"`
	Errors     int           `json:"errors"`
	Duration   time.Duration `json:"duration_ns"`
}

// RunInterval evaluates every enabled algorithm on the given interval.
func (e *Engine) RunInterval(ctx context.Context, interval marketdata.Interval) (RunSummary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	start := e.now()
	summary := RunSummary{Interval: string(interval)}

	all, err := e.algorithms.ListAlgorithms(ctx)
	if err != nil {
		return summary, fmt.Errorf("list algorithms: %w", err)
	}

	for _, a := range all {
		if !a.Enabled || a.Interval != string(interval) {
			continue
		}
		summary.Algorithms++
		s := e.runAlgorithm(ctx, a)
		summary.Evaluated += s.Evaluated
		summary.Triggered += s.Triggered
		summary.Suppressed += s.Suppressed
		summary.Skipped += s.Skipped
		summary.Errors += s.Errors
	}

	summary.Duration = e.now().Sub(start)
	if summary.Algorithms > 0 {
		e.log.Info("evaluation pass complete",
			"interval", interval,
			"algorithms", summary.Algorithms,
			"evaluated", summary.Evaluated,
			"triggered", summary.Triggered,
			"suppressed", summary.Suppressed,
			"skipped", summary.Skipped,
			"errors", summary.Errors,
			"duration", summary.Duration.Round(time.Millisecond))
	}
	return summary, nil
}

// RunAlgorithm evaluates one algorithm now, regardless of its interval or
// enabled flag. This is what the "run now" button in the builder calls.
func (e *Engine) RunAlgorithm(ctx context.Context, a *algo.Algorithm) RunSummary {
	e.mu.Lock()
	defer e.mu.Unlock()

	start := e.now()
	summary := e.runAlgorithm(ctx, a)
	summary.Duration = e.now().Sub(start)
	return summary
}

func (e *Engine) runAlgorithm(ctx context.Context, a *algo.Algorithm) RunSummary {
	summary := RunSummary{Interval: a.Interval, Algorithms: 1}

	interval, err := marketdata.ParseInterval(a.Interval)
	if err != nil {
		e.log.Error("algorithm has an invalid interval", "algorithm", a.Name, "interval", a.Interval, "err", err)
		summary.Errors++
		return summary
	}

	// Fetch a little more than the deepest indicator needs so the newest bars
	// are comfortably inside every indicator's defined range.
	limit := a.RequiredBars() + 20

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	sem := make(chan struct{}, maxConcurrentSymbols)

	for _, raw := range e.symbolsFor(ctx, a) {
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			// Validation should have caught this, so it means the stored
			// document was edited outside the API.
			e.log.Error("algorithm has an unparseable symbol", "algorithm", a.Name, "symbol", raw, "err", err)
			mu.Lock()
			summary.Errors++
			mu.Unlock()
			continue
		}

		wg.Add(1)
		go func(sym marketdata.Symbol) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			outcome := e.evaluateSymbol(ctx, a, sym, interval, limit)

			mu.Lock()
			defer mu.Unlock()
			summary.Evaluated++
			switch outcome {
			case outcomeTriggered:
				summary.Triggered++
			case outcomeSuppressed:
				summary.Suppressed++
			case outcomeSkipped:
				summary.Skipped++
			case outcomeError:
				summary.Errors++
			}
		}(sym)
	}
	wg.Wait()
	return summary
}

type outcome int

const (
	outcomeNotMet outcome = iota
	outcomeTriggered
	outcomeSuppressed
	outcomeSkipped
	outcomeError
)

// evaluateSymbol runs one algorithm against one symbol and delivers an alert
// if it fired and is not inside its cooldown.
func (e *Engine) evaluateSymbol(
	ctx context.Context,
	a *algo.Algorithm,
	sym marketdata.Symbol,
	interval marketdata.Interval,
	limit int,
) outcome {
	series, err := e.candles.Candles(ctx, sym, interval, limit)
	if err != nil {
		e.log.Warn("no data to evaluate against",
			"algorithm", a.Name, "symbol", sym, "err", err)
		e.record(ctx, a, sym, algo.Result{
			Status: algo.StatusError,
			Reason: "market data unavailable: " + err.Error(),
		})
		return outcomeError
	}

	result := e.evaluator.Evaluate(a, sym, series.Candles)
	e.record(ctx, a, sym, result)

	if !result.Triggered() {
		if result.Status == algo.StatusInsufficientData {
			e.log.Debug("evaluation skipped",
				"algorithm", a.Name, "symbol", sym, "reason", result.Reason)
			return outcomeSkipped
		}
		return outcomeNotMet
	}

	// Cooldown: the operator asked not to hear about this symbol again for a
	// while, and a restart must not reset that.
	if suppressed, until := e.inCooldown(ctx, a, sym); suppressed {
		e.log.Debug("alert suppressed by cooldown",
			"algorithm", a.Name, "symbol", sym, "until", until)
		return outcomeSuppressed
	}

	alert := &Alert{
		AlgorithmID:   a.ID,
		AlgorithmName: a.Name,
		Symbol:        sym.String(),
		Interval:      a.Interval,
		FiredAt:       e.now(),
		BarTime:       result.BarTime,
		Price:         result.Price,
		Summary:       result.Summary,
		Conditions:    result.Conditions,
	}

	e.attachAIContext(ctx, a, alert)
	alert.Delivery = e.deliver(ctx, a, alert)

	// Persist last and unconditionally. A Telegram outage must not cost the
	// operators the record that their rule fired.
	if _, err := e.alerts.InsertAlert(ctx, alert); err != nil {
		e.log.Error("could not record alert", "algorithm", a.Name, "symbol", sym, "err", err)
		return outcomeError
	}

	e.log.Info("alert fired",
		"algorithm", a.Name, "symbol", sym, "price", result.Price, "summary", result.Summary)
	return outcomeTriggered
}

// inCooldown reports whether this algorithm alerted on this symbol recently
// enough to stay quiet.
func (e *Engine) inCooldown(ctx context.Context, a *algo.Algorithm, sym marketdata.Symbol) (bool, time.Time) {
	if a.CooldownHours <= 0 {
		return false, time.Time{}
	}
	last, ok, err := e.alerts.LastFiredAt(ctx, a.ID, sym.String())
	if err != nil {
		// Fail closed: if we cannot tell whether we already notified, staying
		// quiet is the safer error. Alert spam erodes trust in every alert.
		e.log.Error("could not read cooldown state; suppressing to avoid duplicates",
			"algorithm", a.Name, "symbol", sym, "err", err)
		return true, time.Time{}
	}
	if !ok {
		return false, time.Time{}
	}
	until := last.Add(time.Duration(a.CooldownHours * float64(time.Hour)))
	return e.now().Before(until), until
}

// attachAIContext asks the AI layer for a brief, degrading to an explicit
// "unavailable" note. An alert never waits on, or fails because of, the model.
func (e *Engine) attachAIContext(ctx context.Context, a *algo.Algorithm, alert *Alert) {
	if !a.Notify.AIContext {
		return
	}
	if e.ai == nil {
		alert.AIStatus = "unavailable"
		alert.AIContext = aiUnavailable
		return
	}
	text, status := e.ai.AlertContext(ctx, alert)
	alert.AIStatus = status
	if strings.TrimSpace(text) == "" {
		alert.AIContext = aiUnavailable
		if alert.AIStatus == "" {
			alert.AIStatus = "unavailable"
		}
		return
	}
	alert.AIContext = text
}

// deliver sends the alert down every configured channel the algorithm asked
// for, recording each attempt.
func (e *Engine) deliver(ctx context.Context, a *algo.Algorithm, alert *Alert) []DeliveryRecord {
	records := []DeliveryRecord{}
	msg := RenderMessage(alert)

	for _, n := range e.notifiers {
		if n.Channel() == "telegram" && !a.Notify.Telegram {
			continue
		}
		if !n.Configured() {
			// Not a failure: the operator simply has not set this channel up.
			continue
		}

		// Bound each delivery so one hanging channel cannot stall the pass.
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := n.Send(sendCtx, msg)
		cancel()

		rec := DeliveryRecord{Channel: n.Channel(), OK: err == nil, At: e.now()}
		if err != nil {
			rec.Error = err.Error()
			e.log.Warn("alert delivery failed",
				"channel", n.Channel(), "algorithm", a.Name, "symbol", alert.Symbol, "err", err)
		}
		records = append(records, rec)
	}
	return records
}

// record stores the evaluation outcome whether or not it fired.
func (e *Engine) record(ctx context.Context, a *algo.Algorithm, sym marketdata.Symbol, result algo.Result) {
	err := e.alerts.RecordEvaluation(ctx, EvaluationRecord{
		AlgorithmID: a.ID,
		Symbol:      sym.String(),
		At:          e.now(),
		Status:      result.Status,
		Reason:      result.Reason,
		Summary:     result.Summary,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		e.log.Warn("could not record evaluation", "algorithm", a.Name, "symbol", sym, "err", err)
	}
}

// RenderMessage formats an alert for delivery.
//
// The shape follows the product specification exactly:
//
//	[ALGO] Momentum watch — RELIANCE
//	RSI(14)=32.1 (<35), close 2,431 > SMA200 2,398, vol 1.8x avg
//	AI: <brief, or "AI context unavailable">
func RenderMessage(a *Alert) Message {
	ticker := a.Symbol
	if idx := strings.Index(ticker, "."); idx > 0 {
		ticker = ticker[:idx]
	}
	title := fmt.Sprintf("[ALGO] %s — %s", a.AlgorithmName, ticker)

	var body strings.Builder
	body.WriteString(a.Summary)
	if a.AIContext != "" {
		body.WriteString("\nAI: ")
		body.WriteString(a.AIContext)
	}

	return Message{
		Title:         title,
		Body:          body.String(),
		Symbol:        a.Symbol,
		AlgorithmName: a.AlgorithmName,
	}
}

// WatchlistResolver expands a watchlist id into its instruments.
type WatchlistResolver interface {
	WatchlistSymbols(ctx context.Context, id int64) ([]marketdata.Symbol, error)
}

// symbolsFor is the set an algorithm should evaluate.
//
// The union of the symbols named on the rule and the members of every list
// attached to it, deduplicated. Resolved at evaluation rather than stored, so
// adding an instrument to a list immediately brings every rule watching that
// list along with it — which is the entire reason for attaching one.
//
// A list that cannot be read is logged and skipped rather than failing the
// run: a rule that also names symbols directly should still evaluate those.
func (e *Engine) symbolsFor(ctx context.Context, a *algo.Algorithm) []string {
	seen := make(map[string]bool, len(a.Symbols))
	out := make([]string, 0, len(a.Symbols))
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range a.Symbols {
		add(s)
	}

	if e.watchlists == nil {
		return out
	}
	for _, id := range a.WatchlistIDs {
		syms, err := e.watchlists.WatchlistSymbols(ctx, id)
		if err != nil {
			e.log.Warn("could not read a watchlist for an algorithm",
				"algorithm", a.Name, "watchlist", id, "err", err)
			continue
		}
		for _, s := range syms {
			add(s.String())
		}
	}
	return out
}
