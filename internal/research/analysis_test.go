package research

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
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
	jumpDay := marketDay(start.AddDate(0, 0, jump))
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

// marketBars builds n weekday sessions stamped at midnight New York, as the
// router stores them, with a flat market and a stock that jumps on the given
// sessions.
func marketBars(n int, jumps map[int]float64) (bench, stock []Bar, days []time.Time) {
	d := time.Date(2025, 1, 6, 0, 0, 0, 0, marketdata.Market)
	bp, sp := 5000.0, 100.0
	for len(days) < n {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			i := len(days)
			mr := 0.002
			if i%2 == 1 {
				mr = -0.002
			}
			if i > 0 {
				bp *= 1 + mr
				sp *= 1 + mr + jumps[i]
			}
			bench = append(bench, Bar{Time: d, Close: bp, Volume: 1})
			stock = append(stock, Bar{Time: d, Close: sp, Volume: 1000})
			days = append(days, d)
		}
		d = d.AddDate(0, 0, 1)
	}
	return
}

// TestReactionsCountEachSessionOnce: forty stories about one launch are one
// event for the stock. Counting them forty times is how every kind of news
// once showed the same reaction, all of it from a single session.
func TestReactionsCountEachSessionOnce(t *testing.T) {
	bench, stock, days := marketBars(300, map[int]float64{200: 0.05, 250: -0.04, 280: 0.03})
	var events []EventRef
	for k, i := range []int{200, 250, 280} {
		for j := 0; j < 40; j++ {
			events = append(events, EventRef{ID: int64(k*100 + j), Headline: "launch", Type: "NEW_PRODUCT", Importance: 5,
				DiscoveredAt: days[i].Add(8 * time.Hour)}) // 8am, before the open
		}
	}
	e := NewEngine(nil, WithPrices(fakePrices{Benchmark: bench, "ACME": stock}),
		WithAnalysis(AnalysisDeps{Events: func(context.Context, string, time.Time) ([]EventRef, error) { return events, nil }}))
	out := e.analyse(context.Background(), []string{"ACME"})
	if len(out) != 1 {
		t.Fatal("no analysis")
	}
	var got *Reaction
	for i := range out[0].Reactions {
		if out[0].Reactions[i].Type == "NEW_PRODUCT" {
			got = &out[0].Reactions[i]
		}
	}
	if got == nil || got.Count != 3 {
		t.Fatalf("product news = %+v, want 3 sessions", got)
	}
	// Found before the open, so the reaction is that same session's.
	if want := (5.0 - 4 + 3) / 3; math.Abs(got.Day1Mean-want) > 0.3 {
		t.Fatalf("day-one mean = %.2f, want about %.2f", got.Day1Mean, want)
	}
	if got.Label != "Product news" || got.Since != marketDay(days[200]) {
		t.Fatalf("label/since = %q/%q", got.Label, got.Since)
	}
}

// TestEarningsExplainBigDaysBeforeNewsCoverage: a results day is explained by
// the earnings history even where the news archive does not reach, and a
// big day before coverage is reported as not covered, not as unexplained.
func TestEarningsExplainBigDaysBeforeNewsCoverage(t *testing.T) {
	bench, stock, days := marketBars(300, map[int]float64{120: -0.08, 200: 0.06, 290: 0.04})
	surprise := 6.7
	// After the close on session 119, so session 120 is the first to trade on it.
	announced := days[119].Add(16 * time.Hour)
	covered := days[285].Add(10 * time.Hour)
	e := NewEngine(nil, WithPrices(fakePrices{Benchmark: bench, "ACME": stock}),
		WithAnalysis(AnalysisDeps{
			Earnings: func(context.Context, string) ([]EarningsRef, error) {
				return []EarningsRef{{AnnouncedAt: announced, SurprisePct: &surprise}}, nil
			},
			Events: func(context.Context, string, time.Time) ([]EventRef, error) {
				return []EventRef{
					{ID: 1, Headline: "Acme wins a contract", Type: "CONTRACT", Importance: 7, DiscoveredAt: covered},
					{ID: 2, Headline: "Acme raises outlook", Type: "GUIDANCE", Importance: 8, DiscoveredAt: days[290].Add(7 * time.Hour)},
				}, nil
			},
		}))
	out := e.analyse(context.Background(), []string{"ACME"})
	if len(out) != 1 {
		t.Fatal("no analysis")
	}
	a := out[0]
	byDay := map[string]BigMove{}
	for _, m := range a.BigMoves {
		byDay[m.Date] = m
	}
	if m := byDay[marketDay(days[120])]; m.Earnings == "" || !strings.Contains(m.Earnings, "beat estimates by 6.7%") || !strings.Contains(m.Earnings, "after the close") {
		t.Fatalf("results day = %+v", m)
	}
	if m := byDay[marketDay(days[200])]; m.Explained() || m.Covered {
		t.Fatalf("a day before coverage must be uncovered, not explained: %+v", m)
	}
	if m := byDay[marketDay(days[290])]; len(m.Events) == 0 || !m.Covered {
		t.Fatalf("covered news day = %+v", m)
	}
	if a.NewsSince != marketDay(days[285]) {
		t.Fatalf("news since = %q", a.NewsSince)
	}
	joined := strings.Join(a.Notes, " ")
	if !strings.Contains(joined, "first session after earnings") || !strings.Contains(joined, "fell before the news archive") {
		t.Fatalf("notes do not account for the big days: %s", joined)
	}
}

type datedScraper struct{ query string }

func (s *datedScraper) Name() string { return "google_news" }
func (s *datedScraper) Trust() int   { return 80 }
func (s *datedScraper) Search(_ context.Context, q string, _ int) ([]Finding, error) {
	s.query = q
	at := func(d, hm string) time.Time {
		t, _ := time.ParseInLocation("2006-01-02 15:04", d+" "+hm, marketdata.Market)
		return t
	}
	return []Finding{
		{Title: "Apple stock: a familiar ride - Trefis", URL: "https://t.example/1", Publisher: "Trefis", PublishedAt: at("2026-07-02", "18:00")},
		{Title: "Apple shares pop 5% as Wall Street weighs iPhone roadmap - CNBC", URL: "https://c.example/2", Publisher: "CNBC", PublishedAt: at("2026-07-02", "13:00")},
		{Title: "Why Apple Stock Rallied Today - Yahoo Finance", URL: "https://y.example/3", Publisher: "Yahoo Finance", PublishedAt: at("2026-07-02", "15:30")},
		{Title: "Microsoft rallies on cloud deal", URL: "https://m.example/4", Publisher: "Reuters", PublishedAt: at("2026-07-02", "11:00")},
		{Title: "Why Apple fell last month", URL: "https://old.example/5", Publisher: "Old", PublishedAt: at("2026-06-03", "11:00")},
		{Title: "Apple stock rises after iPhone news", URL: "https://www.livemint.com/x", Publisher: "Mint", PublishedAt: at("2026-07-02", "12:00")},
	}, nil
}

// TestDatedHeadlinesExplainADay: a day's coverage is searched by date, kept
// only when it names the company and falls on that day, and the headlines
// that give a reason come first.
func TestDatedHeadlinesExplainADay(t *testing.T) {
	sc := &datedScraper{}
	day, _ := time.ParseInLocation("2006-01-02", "2026-07-02", marketdata.Market)
	got := datedHeadlines(context.Background(), sc, "Apple", "AAPL", day)
	if !strings.Contains(sc.query, "after:2026-07-01") || !strings.Contains(sc.query, "before:2026-07-03") {
		t.Fatalf("query = %q", sc.query)
	}
	if len(got) != 2 {
		t.Fatalf("got %d headlines: %+v", len(got), got)
	}
	for _, g := range got {
		if strings.Contains(g.Title, "Trefis") || strings.Contains(g.URL, "m.example") || strings.Contains(g.URL, "old.example") || strings.Contains(g.URL, "livemint") {
			t.Fatalf("kept %+v", g)
		}
	}
	if got[0].Publisher != "CNBC" && got[0].Publisher != "Yahoo Finance" {
		t.Fatalf("explanations not first: %+v", got)
	}
	if strings.HasSuffix(got[0].Title, "- CNBC") {
		t.Fatalf("publisher suffix not trimmed: %q", got[0].Title)
	}
}
