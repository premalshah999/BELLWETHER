package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/marketdata"
)

func sampleAlgorithm(name string) *algo.Algorithm {
	threshold := 35.0
	return &algo.Algorithm{
		Name:     name,
		Symbols:  []string{"RELIANCE.BSE", "AAPL"},
		Interval: "1d",
		All: []algo.Node{
			{Indicator: "rsi", Period: 14, Op: "<", Value: &threshold},
			{Indicator: "close", Op: ">", Compare: &algo.Operand{Indicator: "sma", Period: 200}},
		},
		CooldownHours: 24,
		Notify:        algo.NotifyConfig{Telegram: true, AIContext: true},
		Enabled:       true,
	}
}

func TestAlgorithmCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	a := sampleAlgorithm("Momentum watch")
	id, err := db.CreateAlgorithm(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 || a.ID != id {
		t.Fatalf("id = %d, algorithm.ID = %d", id, a.ID)
	}

	got, err := db.GetAlgorithm(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Momentum watch" || len(got.All) != 2 || !got.Enabled {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got.CooldownHours != 24 {
		t.Errorf("cooldown = %v, want 24", got.CooldownHours)
	}
	if !got.Notify.Telegram || !got.Notify.AIContext {
		t.Errorf("notify = %+v", got.Notify)
	}
	// The nested comparison operand must survive.
	if got.All[1].Compare == nil || got.All[1].Compare.Period != 200 {
		t.Errorf("compare operand lost: %+v", got.All[1].Compare)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps were not set")
	}

	got.Name = "Renamed"
	got.Enabled = false
	if err := db.UpdateAlgorithm(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetAlgorithm(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "Renamed" || after.Enabled {
		t.Errorf("update did not apply: %+v", after)
	}

	if err := db.DeleteAlgorithm(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAlgorithm(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete, error = %v, want ErrNotFound", err)
	}
}

func TestCreateRejectsInvalidAlgorithm(t *testing.T) {
	db := newTestDB(t)
	// Nothing unevaluable may reach storage, and therefore the scheduler.
	bad := &algo.Algorithm{Name: "", Symbols: nil, Interval: "nope"}
	if _, err := db.CreateAlgorithm(context.Background(), bad); err == nil {
		t.Error("want a validation error")
	}
}

func TestUpdateMissingAlgorithm(t *testing.T) {
	db := newTestDB(t)
	a := sampleAlgorithm("ghost")
	a.ID = 999
	if err := db.UpdateAlgorithm(context.Background(), a); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestDeleteMissingAlgorithm(t *testing.T) {
	db := newTestDB(t)
	if err := db.DeleteAlgorithm(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestListAlgorithmsNewestFirst(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, n := range []string{"first", "second", "third"} {
		if _, err := db.CreateAlgorithm(ctx, sampleAlgorithm(n)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListAlgorithms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d algorithms, want 3", len(got))
	}
	if got[0].Name != "third" {
		t.Errorf("first entry = %q, want the newest", got[0].Name)
	}
}

func TestTemplatesRoundTripThroughStorage(t *testing.T) {
	// Seeding uses this path, so every shipped template must survive it.
	db := newTestDB(t)
	ctx := context.Background()

	for _, tpl := range algo.Templates() {
		t.Run(tpl.Key, func(t *testing.T) {
			id, err := db.CreateAlgorithm(ctx, tpl.Algorithm)
			if err != nil {
				t.Fatalf("storing template: %v", err)
			}
			got, err := db.GetAlgorithm(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tpl.Algorithm.Name {
				t.Errorf("name = %q, want %q", got.Name, tpl.Algorithm.Name)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("the reloaded template no longer validates: %v", err)
			}
			if got.RequiredBars() != tpl.Algorithm.RequiredBars() {
				t.Errorf("required bars = %d, want %d", got.RequiredBars(), tpl.Algorithm.RequiredBars())
			}
		})
	}
}

func TestAlertLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	algoID, err := db.CreateAlgorithm(ctx, sampleAlgorithm("watcher"))
	if err != nil {
		t.Fatal(err)
	}

	firedAt := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)
	a := &alerts.Alert{
		AlgorithmID:   algoID,
		AlgorithmName: "watcher",
		Symbol:        "RELIANCE.BSE",
		Interval:      "1d",
		FiredAt:       firedAt,
		BarTime:       firedAt.Add(-time.Hour),
		Price:         2431.5,
		Summary:       "RSI(14)=32.10 < 35",
		Conditions: []algo.ConditionResult{
			{Label: "RSI(14)", Op: "<", LeftValue: 32.1, RightLabel: "35", RightValue: 35, Result: "true", Text: "RSI(14)=32.10 < 35"},
		},
		AIContext: "Some context.",
		AIStatus:  "ok",
		Delivery:  []alerts.DeliveryRecord{{Channel: "telegram", OK: true, At: firedAt}},
	}

	id, err := db.InsertAlert(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("no id assigned")
	}

	list, err := db.ListAlerts(ctx, alerts.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d alerts, want 1", len(list))
	}
	got := list[0]
	if got.Symbol != "RELIANCE.BSE" || got.Price != 2431.5 {
		t.Errorf("alert = %+v", got)
	}
	if !got.FiredAt.Equal(firedAt) {
		t.Errorf("firedAt = %v, want %v", got.FiredAt, firedAt)
	}
	// The full snapshot must survive: it is the audit trail.
	if len(got.Conditions) != 1 || got.Conditions[0].Label != "RSI(14)" {
		t.Errorf("conditions lost in storage: %+v", got.Conditions)
	}
	if got.Conditions[0].LeftValue != 32.1 {
		t.Errorf("snapshot value = %v, want 32.1", got.Conditions[0].LeftValue)
	}
	if got.AIContext != "Some context." || got.AIStatus != "ok" {
		t.Errorf("ai fields lost: %q/%q", got.AIContext, got.AIStatus)
	}
	if len(got.Delivery) != 1 || !got.Delivery[0].OK {
		t.Errorf("delivery record lost: %+v", got.Delivery)
	}
	if got.Read() {
		t.Error("a new alert should be unread")
	}
}

func TestInsertAlertStampsCooldown(t *testing.T) {
	// The alert row and the cooldown stamp must be written together: a crash
	// between them would either lose the alert or re-notify next tick.
	db := newTestDB(t)
	ctx := context.Background()
	firedAt := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)

	if _, ok, err := db.LastFiredAt(ctx, 1, "AAPL"); err != nil || ok {
		t.Fatalf("before firing: ok = %v, err = %v", ok, err)
	}

	if _, err := db.InsertAlert(ctx, &alerts.Alert{
		AlgorithmID: 1, AlgorithmName: "x", Symbol: "AAPL", Interval: "1d",
		FiredAt: firedAt, BarTime: firedAt, Price: 1,
	}); err != nil {
		t.Fatal(err)
	}

	at, ok, err := db.LastFiredAt(ctx, 1, "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the cooldown stamp was not written")
	}
	if !at.Equal(firedAt) {
		t.Errorf("stamp = %v, want %v", at, firedAt)
	}
	// Another symbol is unaffected.
	if _, ok, _ := db.LastFiredAt(ctx, 1, "RELIANCE.BSE"); ok {
		t.Error("the cooldown leaked across symbols")
	}
}

func TestCooldownSurvivesReopen(t *testing.T) {
	// Restarting the process must not reset a cooldown and re-spam.
	dir := t.TempDir() + "/cooldown.db"
	firedAt := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertAlert(context.Background(), &alerts.Alert{
		AlgorithmID: 7, AlgorithmName: "x", Symbol: "AAPL", Interval: "1d",
		FiredAt: firedAt, BarTime: firedAt, Price: 1,
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	at, ok, err := reopened.LastFiredAt(context.Background(), 7, "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !at.Equal(firedAt) {
		t.Errorf("after reopening: ok = %v, at = %v, want the stamp preserved", ok, at)
	}
}

func TestAlertFilters(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	seed := []struct {
		algoID int64
		symbol string
		offset time.Duration
	}{
		{1, "AAPL", 0},
		{1, "RELIANCE.BSE", time.Hour},
		{2, "AAPL", 2 * time.Hour},
		{2, "TCS.BSE", 3 * time.Hour},
	}
	for _, s := range seed {
		if _, err := db.InsertAlert(ctx, &alerts.Alert{
			AlgorithmID: s.algoID, AlgorithmName: "x", Symbol: s.symbol, Interval: "1d",
			FiredAt: base.Add(s.offset), BarTime: base, Price: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name   string
		filter alerts.Filter
		want   int
	}{
		{name: "everything", filter: alerts.Filter{}, want: 4},
		{name: "by algorithm", filter: alerts.Filter{AlgorithmID: 1}, want: 2},
		{name: "by symbol", filter: alerts.Filter{Symbol: "AAPL"}, want: 2},
		{name: "by both", filter: alerts.Filter{AlgorithmID: 2, Symbol: "AAPL"}, want: 1},
		{name: "unread only", filter: alerts.Filter{UnreadOnly: true}, want: 4},
		{name: "limit", filter: alerts.Filter{Limit: 2}, want: 2},
		{name: "before a time", filter: alerts.Filter{Before: base.Add(90 * time.Minute)}, want: 2},
		{name: "no matches", filter: alerts.Filter{Symbol: "NOSUCH"}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.ListAlerts(ctx, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Errorf("got %d alerts, want %d", len(got), tc.want)
			}
		})
	}

	t.Run("newest first", func(t *testing.T) {
		got, err := db.ListAlerts(ctx, alerts.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(got); i++ {
			if got[i].FiredAt.After(got[i-1].FiredAt) {
				t.Fatalf("alert %d is newer than the one before it", i)
			}
		}
	})
}

func TestReadState(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := db.InsertAlert(ctx, &alerts.Alert{
			AlgorithmID: 1, AlgorithmName: "x", Symbol: "AAPL", Interval: "1d",
			FiredAt: base.Add(time.Duration(i) * time.Hour), BarTime: base, Price: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	if n, _ := db.UnreadAlertCount(ctx); n != 3 {
		t.Errorf("unread = %d, want 3", n)
	}

	if err := db.MarkAlertRead(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.UnreadAlertCount(ctx); n != 2 {
		t.Errorf("unread after one read = %d, want 2", n)
	}

	// Marking an already-read alert again is not an error.
	if err := db.MarkAlertRead(ctx, ids[0]); err != nil {
		t.Errorf("re-marking should be a no-op, got %v", err)
	}
	if err := db.MarkAlertRead(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("marking a missing alert: error = %v, want ErrNotFound", err)
	}

	if err := db.MarkAllAlertsRead(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.UnreadAlertCount(ctx); n != 0 {
		t.Errorf("unread after mark-all = %d, want 0", n)
	}

	got, err := db.ListAlerts(ctx, alerts.Filter{UnreadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("unread listing returned %d alerts", len(got))
	}
}

func TestEvaluationRecords(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	for _, rec := range []alerts.EvaluationRecord{
		{AlgorithmID: 1, Symbol: "AAPL", At: at, Status: algo.StatusNotMet, Reason: "conditions not met", Summary: "close=1 < 5"},
		{AlgorithmID: 1, Symbol: "RELIANCE.BSE", At: at, Status: algo.StatusInsufficientData, Reason: "SMA(200) needs 200 bars, have 60"},
	} {
		if err := db.RecordEvaluation(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.LastEvaluations(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	byName := map[string]alerts.EvaluationRecord{}
	for _, r := range got {
		byName[r.Symbol] = r
	}
	if byName["RELIANCE.BSE"].Status != algo.StatusInsufficientData {
		t.Errorf("status = %q", byName["RELIANCE.BSE"].Status)
	}
	if byName["RELIANCE.BSE"].Reason == "" {
		t.Error("the reason a rule could not be decided was not stored")
	}
}

func TestEvaluationRecordDoesNotClobberCooldown(t *testing.T) {
	// Both live in the same row. Recording an evaluation must not erase the
	// last_fired_at stamp that suppresses duplicate alerts.
	db := newTestDB(t)
	ctx := context.Background()
	firedAt := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	if _, err := db.InsertAlert(ctx, &alerts.Alert{
		AlgorithmID: 1, AlgorithmName: "x", Symbol: "AAPL", Interval: "1d",
		FiredAt: firedAt, BarTime: firedAt, Price: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordEvaluation(ctx, alerts.EvaluationRecord{
		AlgorithmID: 1, Symbol: "AAPL", At: firedAt.Add(time.Hour), Status: algo.StatusNotMet,
	}); err != nil {
		t.Fatal(err)
	}

	at, ok, err := db.LastFiredAt(ctx, 1, "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !at.Equal(firedAt) {
		t.Errorf("cooldown stamp = %v (ok=%v), want it preserved at %v", at, ok, firedAt)
	}
}

func TestDeletingAnAlgorithmKeepsItsAlertHistory(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	id, err := db.CreateAlgorithm(ctx, sampleAlgorithm("doomed"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	if _, err := db.InsertAlert(ctx, &alerts.Alert{
		AlgorithmID: id, AlgorithmName: "doomed", Symbol: "AAPL", Interval: "1d",
		FiredAt: at, BarTime: at, Price: 1, Summary: "fired",
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteAlgorithm(ctx, id); err != nil {
		t.Fatal(err)
	}

	got, err := db.ListAlerts(ctx, alerts.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d alerts after deleting the algorithm, want the history kept", len(got))
	}
	if got[0].AlgorithmName != "doomed" {
		t.Errorf("algorithm name = %q, want it denormalised onto the alert", got[0].AlgorithmName)
	}

	// The per-symbol state, however, is gone: a recreated algorithm should
	// not inherit a stale cooldown.
	if _, ok, _ := db.LastFiredAt(ctx, id, "AAPL"); ok {
		t.Error("per-symbol state survived the delete")
	}
}

// mustSymbol parses a canonical symbol for tests.
func mustSymbol(t *testing.T, s string) marketdata.Symbol {
	t.Helper()
	sym, err := marketdata.ParseSymbol(s)
	if err != nil {
		t.Fatal(err)
	}
	return sym
}

// newAlert builds a minimal stored alert for tests.
func newAlert(algorithmID int64, symbol string, at time.Time) *alerts.Alert {
	return &alerts.Alert{
		AlgorithmID: algorithmID, AlgorithmName: "watcher", Symbol: symbol,
		Interval: "1d", FiredAt: at, BarTime: at, Price: 100,
		Summary: "RSI(14)=32.10 < 35",
	}
}
