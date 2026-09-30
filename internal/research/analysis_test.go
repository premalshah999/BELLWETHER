package research

import (
	"context"
	"log/slog"
	"math"
	"testing"
	"time"
)

type fakePrices map[string][]Bar

func (f fakePrices) DailyBars(_ context.Context, symbol string, _ int) ([]Bar, error) {
	return f[symbol], nil
}

func TestAnalysisBetaMovesAndReactions(t *testing.T) {
	start := time.Date(2025, 10, 1, 21, 0, 0, 0, time.UTC)
	var bench, stock []Bar
	bp, sp := 5000.0, 100.0
	jump := 150 // the index of a news-driven jump
	for i := 0; i < 200; i++ {
		day := start.AddDate(0, 0, i)
		// A market that alternates, and a stock that moves twice as much.
		mr := 0.004
		if i%2 == 1 {
			mr = -0.003
		}
		if i > 0 {
			bp *= 1 + mr
			r := 2 * mr
			if i == jump {
				r += 0.10
			}
			sp *= 1 + r
		}
		bench = append(bench, Bar{Time: day, Close: bp, Volume: 1})
		stock = append(stock, Bar{Time: day, Close: sp, Volume: 1000})
	}
	newsDay := start.AddDate(0, 0, jump-1).Add(2 * time.Hour) // found the evening before
	e := NewEngine(nil, WithLogger(slog.Default()), WithPrices(fakePrices{Benchmark: bench, "ACME": stock}),
		WithAnalysis(AnalysisDeps{Events: func(context.Context, string, time.Time) ([]EventRef, error) {
			return []EventRef{{ID: 1, Headline: "Acme wins record contract", Type: "ORDER_WIN", Importance: 8, DiscoveredAt: newsDay}}, nil
		}}))
	out := e.analyse(context.Background(), []string{"ACME"})
	if len(out) != 1 {
		t.Fatalf("expected one analysis, got %d", len(out))
	}
	a := out[0]
	if math.Abs(a.Beta-2) > 0.3 {
		t.Fatalf("beta = %.2f, want about 2", a.Beta)
	}
	jumpDay := dayKey(start.AddDate(0, 0, jump))
	var found *BigMove
	for i := range a.BigMoves {
		if a.BigMoves[i].Date == jumpDay {
			found = &a.BigMoves[i]
		}
	}
	if found == nil || found.AbnormalPct < 8 || len(found.Events) == 0 {
		t.Fatalf("the jump was not found with its news: %+v", a.BigMoves)
	}
	// The event was discovered the evening before the jump, so its next-day
	// reaction must be measured on the jump session.
	if len(a.Notable) != 1 || a.Notable[0].Session != jumpDay || a.Notable[0].Day1Pct < 8 {
		t.Fatalf("reaction not measured from the next session: %+v", a.Notable)
	}
	if len(a.Series) == 0 || a.Series[0].Stock != 100 || a.Series[0].Bench != 100 {
		t.Fatalf("series not rebased: %+v", a.Series[:1])
	}
	if len(a.Notes) == 0 {
		t.Fatal("no notes")
	}
}
