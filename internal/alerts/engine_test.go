package alerts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/marketdata"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

type memAlgorithms struct {
	mu   sync.Mutex
	list []*algo.Algorithm
	err  error
}

func (m *memAlgorithms) ListAlgorithms(context.Context) ([]*algo.Algorithm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return m.list, nil
}
func (m *memAlgorithms) GetAlgorithm(context.Context, int64) (*algo.Algorithm, error) {
	return nil, errors.New("not implemented")
}
func (m *memAlgorithms) CreateAlgorithm(context.Context, *algo.Algorithm) (int64, error) {
	return 0, errors.New("not implemented")
}
func (m *memAlgorithms) UpdateAlgorithm(context.Context, *algo.Algorithm) error {
	return errors.New("not implemented")
}
func (m *memAlgorithms) DeleteAlgorithm(context.Context, int64) error {
	return errors.New("not implemented")
}

type memAlerts struct {
	mu           sync.Mutex
	inserted     []*Alert
	lastFired    map[string]time.Time
	evaluations  []EvaluationRecord
	insertErr    error
	lastFiredErr error
	nextID       int64
}

func newMemAlerts() *memAlerts {
	return &memAlerts{lastFired: map[string]time.Time{}}
}

func key(id int64, symbol string) string { return symbol + "@" + strconv.FormatInt(id, 10) }

func (m *memAlerts) InsertAlert(_ context.Context, a *Alert) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.insertErr != nil {
		return 0, m.insertErr
	}
	m.nextID++
	a.ID = m.nextID
	// Copy so a later mutation by the caller cannot rewrite history.
	stored := *a
	m.inserted = append(m.inserted, &stored)
	m.lastFired[key(a.AlgorithmID, a.Symbol)] = a.FiredAt
	return a.ID, nil
}

func (m *memAlerts) ListAlerts(context.Context, Filter) ([]Alert, error) { return nil, nil }
func (m *memAlerts) MarkAlertRead(context.Context, int64) error          { return nil }
func (m *memAlerts) MarkAllAlertsRead(context.Context) error             { return nil }
func (m *memAlerts) UnreadAlertCount(context.Context) (int, error)       { return 0, nil }

func (m *memAlerts) LastFiredAt(_ context.Context, id int64, symbol string) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastFiredErr != nil {
		return time.Time{}, false, m.lastFiredErr
	}
	t, ok := m.lastFired[key(id, symbol)]
	return t, ok, nil
}

func (m *memAlerts) RecordEvaluation(_ context.Context, rec EvaluationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evaluations = append(m.evaluations, rec)
	return nil
}

func (m *memAlerts) LastEvaluations(context.Context, int64) ([]EvaluationRecord, error) {
	return nil, nil
}

func (m *memAlerts) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inserted)
}

func (m *memAlerts) at(i int) *Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inserted[i]
}

// stubCandles returns a fixed series, or an error.
type stubCandles struct {
	candles []marketdata.Candle
	err     error
	mu      sync.Mutex
	calls   int
}

func (s *stubCandles) Candles(_ context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Series, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.err != nil {
		return marketdata.Series{}, s.err
	}
	return marketdata.Series{Symbol: sym, Interval: iv, Candles: s.candles, Source: "test"}, nil
}

// recordingNotifier captures what was sent.
type recordingNotifier struct {
	channel    string
	configured bool
	err        error
	mu         sync.Mutex
	sent       []Message
	// block, when non-nil, holds Send until it is closed.
	block chan struct{}
}

func (n *recordingNotifier) Channel() string  { return n.channel }
func (n *recordingNotifier) Configured() bool { return n.configured }

func (n *recordingNotifier) Send(ctx context.Context, m Message) error {
	if n.block != nil {
		select {
		case <-n.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.sent = append(n.sent, m)
	return nil
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sent)
}

func (n *recordingNotifier) last() Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sent[len(n.sent)-1]
}

type stubAI struct {
	text   string
	status string
}

func (s stubAI) AlertContext(context.Context, *Alert) (string, string) { return s.text, s.status }

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// risingCandles produces a series where "close > 0" is definitely true.
func risingCandles(n int) []marketdata.Candle {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]marketdata.Candle, n)
	for i := range out {
		p := 100 + float64(i)
		out[i] = marketdata.Candle{
			Time: base.Add(time.Duration(i) * 24 * time.Hour),
			Open: p, High: p + 1, Low: p - 1, Close: p, Volume: 1000,
		}
	}
	return out
}

// alwaysFires is an algorithm whose single condition is trivially true.
func alwaysFires(id int64, symbols ...string) *algo.Algorithm {
	one := 1.0
	if len(symbols) == 0 {
		symbols = []string{"XOM"}
	}
	a := &algo.Algorithm{
		ID: id, Name: "Always", Symbols: symbols, Interval: "1d",
		All:           []algo.Node{{Indicator: "close", Op: ">", Value: &one}},
		CooldownHours: 24,
		Notify:        algo.NotifyConfig{Telegram: true},
		Enabled:       true,
	}
	if err := a.Validate(); err != nil {
		panic(err)
	}
	return a
}

// neverFires is an algorithm whose condition is definitely false.
func neverFires(id int64) *algo.Algorithm {
	huge := 1e12
	a := &algo.Algorithm{
		ID: id, Name: "Never", Symbols: []string{"XOM"}, Interval: "1d",
		All:           []algo.Node{{Indicator: "close", Op: ">", Value: &huge}},
		CooldownHours: 24,
		Notify:        algo.NotifyConfig{Telegram: true},
		Enabled:       true,
	}
	if err := a.Validate(); err != nil {
		panic(err)
	}
	return a
}

type harness struct {
	engine     *Engine
	algorithms *memAlgorithms
	alerts     *memAlerts
	candles    *stubCandles
	telegram   *recordingNotifier
	now        time.Time
}

func newHarness(t *testing.T, algos ...*algo.Algorithm) *harness {
	t.Helper()
	h := &harness{
		algorithms: &memAlgorithms{list: algos},
		alerts:     newMemAlerts(),
		candles:    &stubCandles{candles: risingCandles(60)},
		telegram:   &recordingNotifier{channel: "telegram", configured: true},
		now:        time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}
	h.engine = NewEngine(
		h.algorithms, h.alerts, h.candles,
		algo.NewEvaluator(time.UTC),
		[]Notifier{h.telegram},
		WithLogger(quiet()),
		WithClock(func() time.Time { return h.now }),
	)
	return h
}

// ---------------------------------------------------------------------------
// Firing
// ---------------------------------------------------------------------------

func TestFiresAndDelivers(t *testing.T) {
	h := newHarness(t, alwaysFires(1))

	summary, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Triggered != 1 {
		t.Errorf("triggered = %d, want 1", summary.Triggered)
	}
	if h.alerts.count() != 1 {
		t.Fatalf("stored %d alerts, want 1", h.alerts.count())
	}
	if h.telegram.count() != 1 {
		t.Fatalf("sent %d telegram messages, want 1", h.telegram.count())
	}

	a := h.alerts.at(0)
	if a.Symbol != "XOM" {
		t.Errorf("symbol = %q", a.Symbol)
	}
	if a.Summary == "" {
		t.Error("alert has no summary")
	}
	if len(a.Conditions) == 0 {
		t.Error("alert stored no evaluation snapshot")
	}
	if a.Price == 0 || a.BarTime.IsZero() {
		t.Error("alert did not capture the price and bar it fired on")
	}
	if len(a.Delivery) != 1 || !a.Delivery[0].OK {
		t.Errorf("delivery record = %+v, want one successful telegram entry", a.Delivery)
	}
}

func TestDoesNotFireWhenConditionsAreNotMet(t *testing.T) {
	h := newHarness(t, neverFires(1))
	summary, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Triggered != 0 {
		t.Errorf("triggered = %d, want 0", summary.Triggered)
	}
	if h.alerts.count() != 0 || h.telegram.count() != 0 {
		t.Error("an alert was raised for conditions that were not met")
	}
	// The non-firing evaluation is still recorded, so "why didn't this
	// alert?" has an answer.
	if len(h.alerts.evaluations) != 1 {
		t.Errorf("recorded %d evaluations, want 1", len(h.alerts.evaluations))
	}
	if h.alerts.evaluations[0].Status != algo.StatusNotMet {
		t.Errorf("status = %q, want not_met", h.alerts.evaluations[0].Status)
	}
}

func TestDisabledAlgorithmsAreSkipped(t *testing.T) {
	a := alwaysFires(1)
	a.Enabled = false
	h := newHarness(t, a)

	summary, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Algorithms != 0 || h.alerts.count() != 0 {
		t.Error("a disabled algorithm was evaluated")
	}
}

func TestOnlyMatchingIntervalRuns(t *testing.T) {
	a := alwaysFires(1)
	a.Interval = "1h"
	h := newHarness(t, a)

	if _, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d); err != nil {
		t.Fatal(err)
	}
	if h.alerts.count() != 0 {
		t.Error("a 1h algorithm ran on the 1d pass")
	}
	if _, err := h.engine.RunInterval(context.Background(), marketdata.Interval1h); err != nil {
		t.Fatal(err)
	}
	if h.alerts.count() != 1 {
		t.Error("the 1h algorithm did not run on its own pass")
	}
}

// ---------------------------------------------------------------------------
// Cooldown and deduplication
// ---------------------------------------------------------------------------

func TestCooldownSuppressesRepeats(t *testing.T) {
	h := newHarness(t, alwaysFires(1)) // 24h cooldown
	ctx := context.Background()

	// First pass fires.
	if _, err := h.engine.RunInterval(ctx, marketdata.Interval1d); err != nil {
		t.Fatal(err)
	}
	if h.alerts.count() != 1 {
		t.Fatalf("first pass stored %d alerts, want 1", h.alerts.count())
	}

	// Several more passes inside the window must stay silent.
	for i := 0; i < 5; i++ {
		h.now = h.now.Add(time.Hour)
		summary, err := h.engine.RunInterval(ctx, marketdata.Interval1d)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Suppressed != 1 {
			t.Errorf("pass %d: suppressed = %d, want 1", i, summary.Suppressed)
		}
	}
	if h.alerts.count() != 1 {
		t.Errorf("stored %d alerts during the cooldown, want 1", h.alerts.count())
	}
	if h.telegram.count() != 1 {
		t.Errorf("sent %d messages during the cooldown, want 1", h.telegram.count())
	}
}

func TestCooldownExpires(t *testing.T) {
	h := newHarness(t, alwaysFires(1))
	ctx := context.Background()

	if _, err := h.engine.RunInterval(ctx, marketdata.Interval1d); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		advance time.Duration
		want    int
	}{
		{name: "one hour later, still quiet", advance: time.Hour, want: 1},
		{name: "23 hours later, still quiet", advance: 23 * time.Hour, want: 1},
		{name: "just past 24 hours, fires again", advance: 24*time.Hour + time.Minute, want: 2},
	}
	base := h.now
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h.now = base.Add(tc.advance)
			if _, err := h.engine.RunInterval(ctx, marketdata.Interval1d); err != nil {
				t.Fatal(err)
			}
			if got := h.alerts.count(); got != tc.want {
				t.Errorf("alerts = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCooldownIsPerSymbol(t *testing.T) {
	h := newHarness(t, alwaysFires(1, "XOM", "AAPL", "IBM"))
	ctx := context.Background()

	if _, err := h.engine.RunInterval(ctx, marketdata.Interval1d); err != nil {
		t.Fatal(err)
	}
	if h.alerts.count() != 3 {
		t.Fatalf("stored %d alerts, want one per symbol", h.alerts.count())
	}

	// Every symbol is now independently in cooldown.
	h.now = h.now.Add(time.Hour)
	summary, err := h.engine.RunInterval(ctx, marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Suppressed != 3 {
		t.Errorf("suppressed = %d, want 3", summary.Suppressed)
	}
}

func TestZeroCooldownFiresEveryPass(t *testing.T) {
	a := alwaysFires(1)
	a.CooldownHours = 0
	h := newHarness(t, a)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		h.now = h.now.Add(time.Minute)
		if _, err := h.engine.RunInterval(ctx, marketdata.Interval1d); err != nil {
			t.Fatal(err)
		}
	}
	if h.alerts.count() != 3 {
		t.Errorf("alerts = %d, want 3 with no cooldown set", h.alerts.count())
	}
}

func TestCooldownFailsClosed(t *testing.T) {
	// If we cannot read the cooldown state we cannot know whether the
	// operator was already told. Staying quiet is the safer error: alert
	// spam destroys trust in every future alert.
	h := newHarness(t, alwaysFires(1))
	h.alerts.lastFiredErr = errors.New("database unavailable")

	summary, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Suppressed != 1 {
		t.Errorf("suppressed = %d, want 1", summary.Suppressed)
	}
	if h.telegram.count() != 0 {
		t.Error("an alert was sent while the cooldown state was unreadable")
	}
}

// ---------------------------------------------------------------------------
// Degradation
// ---------------------------------------------------------------------------

func TestAlertIsRecordedEvenWhenDeliveryFails(t *testing.T) {
	// A Telegram outage must not cost the operators the record that their
	// rule fired.
	h := newHarness(t, alwaysFires(1))
	h.telegram.err = errors.New("telegram: 502 bad gateway")

	summary, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Triggered != 1 {
		t.Errorf("triggered = %d, want 1", summary.Triggered)
	}
	if h.alerts.count() != 1 {
		t.Fatalf("stored %d alerts, want the alert recorded despite the failure", h.alerts.count())
	}
	rec := h.alerts.at(0).Delivery
	if len(rec) != 1 || rec[0].OK {
		t.Fatalf("delivery = %+v, want one failed record", rec)
	}
	if !strings.Contains(rec[0].Error, "502") {
		t.Errorf("delivery error = %q, want the upstream message preserved", rec[0].Error)
	}
}

func TestUnconfiguredNotifierIsSkippedNotFailed(t *testing.T) {
	h := newHarness(t, alwaysFires(1))
	h.telegram.configured = false

	if _, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d); err != nil {
		t.Fatal(err)
	}
	if h.alerts.count() != 1 {
		t.Fatal("the alert should still be recorded in the in-app feed")
	}
	if len(h.alerts.at(0).Delivery) != 0 {
		t.Errorf("delivery = %+v, want no records: an unset channel is not a failed delivery",
			h.alerts.at(0).Delivery)
	}
}

func TestTelegramSkippedWhenAlgorithmOptsOut(t *testing.T) {
	a := alwaysFires(1)
	a.Notify.Telegram = false
	h := newHarness(t, a)

	if _, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d); err != nil {
		t.Fatal(err)
	}
	if h.telegram.count() != 0 {
		t.Error("telegram was used by an algorithm that opted out")
	}
	if h.alerts.count() != 1 {
		t.Error("the alert should still reach the in-app feed")
	}
}

func TestMarketDataFailureDoesNotFire(t *testing.T) {
	h := newHarness(t, alwaysFires(1))
	h.candles.err = errors.New("all providers down")

	summary, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Errors != 1 {
		t.Errorf("errors = %d, want 1", summary.Errors)
	}
	if h.alerts.count() != 0 {
		t.Error("an alert fired without any market data")
	}
	if len(h.alerts.evaluations) != 1 || h.alerts.evaluations[0].Status != algo.StatusError {
		t.Errorf("evaluations = %+v, want one error record", h.alerts.evaluations)
	}
}

func TestInsufficientDataIsSkippedNotFired(t *testing.T) {
	deep := &algo.Algorithm{
		ID: 1, Name: "Deep", Symbols: []string{"XOM"}, Interval: "1d",
		All: []algo.Node{{
			Indicator: "close", Op: ">",
			Compare: &algo.Operand{Indicator: "sma", Period: 200},
		}},
		CooldownHours: 24, Notify: algo.NotifyConfig{Telegram: true}, Enabled: true,
	}
	if err := deep.Validate(); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, deep)
	h.candles.candles = risingCandles(50) // not enough for SMA(200)

	summary, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", summary.Skipped)
	}
	if h.alerts.count() != 0 {
		t.Error("an alert fired on an indicator with no value")
	}
}

// ---------------------------------------------------------------------------
// AI context
// ---------------------------------------------------------------------------

func TestAIContextAttached(t *testing.T) {
	a := alwaysFires(1)
	a.Notify.AIContext = true
	h := newHarness(t, a)
	h.engine = NewEngine(h.algorithms, h.alerts, h.candles,
		algo.NewEvaluator(time.UTC), []Notifier{h.telegram},
		WithLogger(quiet()), WithClock(func() time.Time { return h.now }),
		WithAIContext(stubAI{text: "Three sentences of context.", status: "ok"}))

	if _, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d); err != nil {
		t.Fatal(err)
	}
	got := h.alerts.at(0)
	if got.AIContext != "Three sentences of context." {
		t.Errorf("ai context = %q", got.AIContext)
	}
	if got.AIStatus != "ok" {
		t.Errorf("ai status = %q, want ok", got.AIStatus)
	}
	if !strings.Contains(h.telegram.last().Body, "AI: Three sentences") {
		t.Errorf("telegram body does not carry the labelled AI line:\n%s", h.telegram.last().Body)
	}
}

func TestAIContextDegradesButStillFires(t *testing.T) {
	tests := []struct {
		name       string
		provider   AIContextProvider
		wantText   string
		wantStatus string
	}{
		{
			name:       "no provider configured",
			provider:   nil,
			wantText:   aiUnavailable,
			wantStatus: "unavailable",
		},
		{
			name:       "provider returns nothing",
			provider:   stubAI{text: "", status: "unavailable"},
			wantText:   aiUnavailable,
			wantStatus: "unavailable",
		},
		{
			name:       "budget exhausted",
			provider:   stubAI{text: "", status: "budget_reached"},
			wantText:   aiUnavailable,
			wantStatus: "budget_reached",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := alwaysFires(1)
			a.Notify.AIContext = true
			h := newHarness(t, a)

			opts := []EngineOption{
				WithLogger(quiet()), WithClock(func() time.Time { return h.now }),
			}
			if tc.provider != nil {
				opts = append(opts, WithAIContext(tc.provider))
			}
			h.engine = NewEngine(h.algorithms, h.alerts, h.candles,
				algo.NewEvaluator(time.UTC), []Notifier{h.telegram}, opts...)

			if _, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d); err != nil {
				t.Fatal(err)
			}
			// The alert must still fire — that is the whole point.
			if h.alerts.count() != 1 {
				t.Fatalf("stored %d alerts, want 1: alerts must fire without AI", h.alerts.count())
			}
			if h.telegram.count() != 1 {
				t.Fatal("the alert was not delivered")
			}
			got := h.alerts.at(0)
			if got.AIContext != tc.wantText {
				t.Errorf("ai context = %q, want %q", got.AIContext, tc.wantText)
			}
			if got.AIStatus != tc.wantStatus {
				t.Errorf("ai status = %q, want %q", got.AIStatus, tc.wantStatus)
			}
		})
	}
}

func TestNoAIContextWhenNotRequested(t *testing.T) {
	h := newHarness(t, alwaysFires(1)) // AIContext false
	h.engine = NewEngine(h.algorithms, h.alerts, h.candles,
		algo.NewEvaluator(time.UTC), []Notifier{h.telegram},
		WithLogger(quiet()), WithClock(func() time.Time { return h.now }),
		WithAIContext(stubAI{text: "should not appear", status: "ok"}))

	if _, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d); err != nil {
		t.Fatal(err)
	}
	if got := h.alerts.at(0).AIContext; got != "" {
		t.Errorf("ai context = %q, want empty when the algorithm did not ask for it", got)
	}
}

// ---------------------------------------------------------------------------
// Message rendering
// ---------------------------------------------------------------------------

func TestRenderMessageMatchesTheSpecShape(t *testing.T) {
	a := &Alert{
		AlgorithmName: "Momentum watch",
		Symbol:        "XOM",
		Summary:       "RSI(14)=32.10 < 35, close=2431.00 > SMA(200)=2398.00",
		AIContext:     "Reliance fell with the broader index today.",
	}
	msg := RenderMessage(a)

	if msg.Title != "[ALGO] Momentum watch — XOM" {
		t.Errorf("title = %q", msg.Title)
	}
	if !strings.Contains(msg.Body, "RSI(14)=32.10") {
		t.Errorf("body is missing the indicator snapshot:\n%s", msg.Body)
	}
	if !strings.HasPrefix(strings.Split(msg.Body, "\n")[1], "AI: ") {
		t.Errorf("the AI line must be labelled:\n%s", msg.Body)
	}
}

func TestRenderMessageStripsTheSuffixFromTheTitle(t *testing.T) {
	for _, tc := range []struct{ symbol, want string }{
		{"GSPC.INDEX", "GSPC"},
		{"AAPL", "AAPL"},
	} {
		msg := RenderMessage(&Alert{AlgorithmName: "X", Symbol: tc.symbol})
		if !strings.HasSuffix(msg.Title, "— "+tc.want) {
			t.Errorf("symbol %q produced title %q", tc.symbol, msg.Title)
		}
	}
}

func TestRenderMessageWithoutAI(t *testing.T) {
	msg := RenderMessage(&Alert{AlgorithmName: "X", Symbol: "AAPL", Summary: "close=1 > 0"})
	if strings.Contains(msg.Body, "AI:") {
		t.Errorf("an AI line appeared where none was set:\n%s", msg.Body)
	}
}

// ---------------------------------------------------------------------------
// Robustness
// ---------------------------------------------------------------------------

func TestRunAlgorithmIgnoresEnabledAndInterval(t *testing.T) {
	// "Run now" in the builder must work on a draft that is neither enabled
	// nor due.
	a := alwaysFires(1)
	a.Enabled = false
	a.Interval = "1wk"
	h := newHarness(t, a)

	summary := h.engine.RunAlgorithm(context.Background(), a)
	if summary.Triggered != 1 {
		t.Errorf("triggered = %d, want 1", summary.Triggered)
	}
}

func TestConcurrentRunsDoNotDoubleFire(t *testing.T) {
	// Two overlapping schedules must not both slip past the cooldown check.
	h := newHarness(t, alwaysFires(1))
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.engine.RunInterval(ctx, marketdata.Interval1d)
		}()
	}
	wg.Wait()

	if got := h.alerts.count(); got != 1 {
		t.Errorf("stored %d alerts from concurrent passes, want 1", got)
	}
}

func TestStoreFailureIsCountedNotPanicked(t *testing.T) {
	h := newHarness(t, alwaysFires(1))
	h.alerts.insertErr = errors.New("disk full")

	summary, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Errors != 1 {
		t.Errorf("errors = %d, want 1", summary.Errors)
	}
}

func TestListFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.algorithms.err = errors.New("database gone")

	if _, err := h.engine.RunInterval(context.Background(), marketdata.Interval1d); err == nil {
		t.Error("want an error when algorithms cannot be listed")
	}
}

func TestCancelledContextStopsCleanly(t *testing.T) {
	h := newHarness(t, alwaysFires(1, "A", "B", "C", "D", "E", "F"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Must return rather than hang or panic.
	done := make(chan struct{})
	go func() {
		h.engine.RunInterval(ctx, marketdata.Interval1d)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunInterval did not return on a cancelled context")
	}
}
