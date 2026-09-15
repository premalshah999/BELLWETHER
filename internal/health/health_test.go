package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
)

func quiet() Option { return WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) }

func newTracker(deps ...Dep) *Tracker {
	return New(nil, deps, quiet())
}

func statusOf(t *testing.T, tr *Tracker, provider string) storage.ProviderHealth {
	t.Helper()
	for _, h := range tr.Snapshot() {
		if h.Provider == provider {
			return h
		}
	}
	t.Fatalf("no health record for %q", provider)
	return storage.ProviderHealth{}
}

func TestInitialState(t *testing.T) {
	tr := newTracker(
		Dep{Provider: "yahoo", Kind: KindMarketData, Configured: true},
		Dep{Provider: "alphavantage", Kind: KindMarketData, Configured: false},
	)
	if got := statusOf(t, tr, "yahoo").Status; got != storage.HealthOK {
		t.Errorf("yahoo status = %q, want ok", got)
	}
	if got := statusOf(t, tr, "alphavantage").Status; got != storage.HealthUnconfigured {
		t.Errorf("alphavantage status = %q, want unconfigured — a missing key is not a fault", got)
	}
}

func TestObserveTransitions(t *testing.T) {
	tests := []struct {
		name       string
		outcomes   []marketdata.Outcome
		wantStatus storage.HealthStatus
	}{
		{
			name:       "success is ok",
			outcomes:   []marketdata.Outcome{{OK: true}},
			wantStatus: storage.HealthOK,
		},
		{
			name:       "one fault is degraded, not down",
			outcomes:   []marketdata.Outcome{{Err: errors.New("500")}},
			wantStatus: storage.HealthDegraded,
		},
		{
			name: "two faults are still degraded",
			outcomes: []marketdata.Outcome{
				{Err: errors.New("500")}, {Err: errors.New("500")},
			},
			wantStatus: storage.HealthDegraded,
		},
		{
			name: "three consecutive faults are down",
			outcomes: []marketdata.Outcome{
				{Err: errors.New("500")}, {Err: errors.New("500")}, {Err: errors.New("500")},
			},
			wantStatus: storage.HealthDown,
		},
		{
			name: "a success resets the fault streak",
			outcomes: []marketdata.Outcome{
				{Err: errors.New("500")}, {Err: errors.New("500")},
				{OK: true},
				{Err: errors.New("500")},
			},
			wantStatus: storage.HealthDegraded,
		},
		{
			name: "skips never escalate to down",
			outcomes: []marketdata.Outcome{
				{Skipped: true, Err: marketdata.ErrBudgetExhausted},
				{Skipped: true, Err: marketdata.ErrBudgetExhausted},
				{Skipped: true, Err: marketdata.ErrBudgetExhausted},
				{Skipped: true, Err: marketdata.ErrBudgetExhausted},
			},
			wantStatus: storage.HealthDegraded,
		},
		{
			name: "recovery after being down",
			outcomes: []marketdata.Outcome{
				{Err: errors.New("x")}, {Err: errors.New("x")}, {Err: errors.New("x")},
				{OK: true},
			},
			wantStatus: storage.HealthOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker(Dep{Provider: "yahoo", Kind: KindMarketData, Configured: true})
			sink := tr.MarketDataSink()
			for _, o := range tc.outcomes {
				o.Provider = "yahoo"
				sink(context.Background(), o)
			}
			if got := statusOf(t, tr, "yahoo").Status; got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
		})
	}
}

func TestUnconfiguredIgnoresOutcomes(t *testing.T) {
	tr := newTracker(Dep{Provider: "alphavantage", Kind: KindMarketData, Configured: false})
	sink := tr.MarketDataSink()
	for i := 0; i < 5; i++ {
		sink(context.Background(), marketdata.Outcome{Provider: "alphavantage", Err: errors.New("no key")})
	}
	if got := statusOf(t, tr, "alphavantage").Status; got != storage.HealthUnconfigured {
		t.Errorf("status = %q, want unconfigured to be sticky", got)
	}
}

func TestLastOKIsRemembered(t *testing.T) {
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	tr := New(nil, []Dep{{Provider: "yahoo", Kind: KindMarketData, Configured: true}},
		quiet(), WithClock(func() time.Time { return at }))

	tr.Observe(context.Background(), "yahoo", true, false, nil)
	at = at.Add(time.Hour)
	tr.Observe(context.Background(), "yahoo", false, false, errors.New("429"))

	h := statusOf(t, tr, "yahoo")
	if h.LastOKAt == nil {
		t.Fatal("lastOKAt is nil; a failure must not erase when it last worked")
	}
	if h.LastOKAt.Hour() != 9 {
		t.Errorf("lastOKAt = %v, want 09:00", h.LastOKAt)
	}
	if h.LastErrorAt == nil || h.LastErrorAt.Hour() != 10 {
		t.Errorf("lastErrorAt = %v, want 10:00", h.LastErrorAt)
	}
	if h.Message != "429" {
		t.Errorf("message = %q, want %q", h.Message, "429")
	}
}

func TestMarketDataDegraded(t *testing.T) {
	fail := errors.New("down")
	tests := []struct {
		name     string
		deps     []Dep
		observe  func(*Tracker)
		wantFlag bool
	}{
		{
			name:     "all healthy",
			deps:     []Dep{{Provider: "yahoo", Kind: KindMarketData, Configured: true}},
			wantFlag: false,
		},
		{
			name: "one of two failing is not degraded — that is what fallback is for",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "alphavantage", Kind: KindMarketData, Configured: true},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "yahoo", false, false, fail)
				tr.Observe(context.Background(), "alphavantage", true, false, nil)
			},
			wantFlag: false,
		},
		{
			name: "every configured provider failing is degraded",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "alphavantage", Kind: KindMarketData, Configured: true},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "yahoo", false, false, fail)
				tr.Observe(context.Background(), "alphavantage", false, false, fail)
			},
			wantFlag: true,
		},
		{
			name: "an unconfigured provider does not count against health",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "alphavantage", Kind: KindMarketData, Configured: false},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "yahoo", true, false, nil)
			},
			wantFlag: false,
		},
		{
			name: "a failing LLM does not raise the market data banner",
			deps: []Dep{
				{Provider: "yahoo", Kind: KindMarketData, Configured: true},
				{Provider: "llm", Kind: KindLLM, Configured: true},
			},
			observe: func(tr *Tracker) {
				tr.Observe(context.Background(), "yahoo", true, false, nil)
				tr.Observe(context.Background(), "llm", false, false, fail)
			},
			wantFlag: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker(tc.deps...)
			if tc.observe != nil {
				tc.observe(tr)
			}
			if got := tr.MarketDataDegraded(); got != tc.wantFlag {
				t.Errorf("MarketDataDegraded() = %v, want %v", got, tc.wantFlag)
			}
		})
	}
}

func TestSetConfigured(t *testing.T) {
	tr := newTracker(Dep{Provider: "alphavantage", Kind: KindMarketData, Configured: false})
	if got := statusOf(t, tr, "alphavantage").Status; got != storage.HealthUnconfigured {
		t.Fatalf("status = %q, want unconfigured", got)
	}
	tr.SetConfigured("alphavantage", true)
	if got := statusOf(t, tr, "alphavantage").Status; got != storage.HealthOK {
		t.Errorf("after configuring, status = %q, want ok", got)
	}
	tr.Observe(context.Background(), "alphavantage", false, false, errors.New("bad key"))
	if got := statusOf(t, tr, "alphavantage").Status; got != storage.HealthDegraded {
		t.Errorf("a configured provider must now register faults, got %q", got)
	}
}

func TestSnapshotOrderIsStable(t *testing.T) {
	tr := newTracker(
		Dep{Provider: "yahoo", Kind: KindMarketData, Configured: true},
		Dep{Provider: "alphavantage", Kind: KindMarketData, Configured: true},
		Dep{Provider: "telegram", Kind: KindNotify, Configured: true},
		Dep{Provider: "llm", Kind: KindLLM, Configured: true},
	)
	want := []string{"llm", "alphavantage", "yahoo", "telegram"}
	for i := 0; i < 5; i++ {
		got := tr.Snapshot()
		if len(got) != len(want) {
			t.Fatalf("got %d records, want %d", len(got), len(want))
		}
		for j, w := range want {
			if got[j].Provider != w {
				t.Fatalf("iteration %d position %d = %q, want %q", i, j, got[j].Provider, w)
			}
		}
	}
}
