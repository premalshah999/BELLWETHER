package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/search"
)

func TestLLMUsageAccounting(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	records := []ai.UsageRecord{
		{Period: "2026-08", Feature: "morning_brief", Model: "m", Usage: ai.Usage{TotalTokens: 1000}, At: at},
		{Period: "2026-08", Feature: "morning_brief", Model: "m", Usage: ai.Usage{TotalTokens: 500}, At: at},
		{Period: "2026-08", Feature: "news_digest", Model: "cheap", Usage: ai.Usage{TotalTokens: 200}, At: at},
		{Period: "2026-09", Feature: "morning_brief", Model: "m", Usage: ai.Usage{TotalTokens: 9999}, At: at},
	}
	for _, r := range records {
		if err := db.RecordLLMUsage(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	used, err := db.LLMTokensUsed(ctx, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if used != 1700 {
		t.Errorf("august usage = %d, want 1700", used)
	}

	// A different month has its own allowance.
	if used, _ := db.LLMTokensUsed(ctx, "2026-09"); used != 9999 {
		t.Errorf("september usage = %d, want 9999", used)
	}
	// An untouched month is zero, not an error.
	if used, err := db.LLMTokensUsed(ctx, "2026-01"); err != nil || used != 0 {
		t.Errorf("empty period = %d, err = %v", used, err)
	}

	byFeature, err := db.LLMUsageByFeature(ctx, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if byFeature["morning_brief"] != 1500 || byFeature["news_digest"] != 200 {
		t.Errorf("by feature = %v", byFeature)
	}
}

func TestAIOutputArchive(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 8, 30, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		out := &ai.Output{
			Kind: "morning_brief", CreatedAt: base.Add(time.Duration(i) * time.Hour),
			Model: "m", Tokens: 100, Content: []byte(`{"headline":"day ` + string(rune('a'+i)) + `"}`),
		}
		if _, err := db.SaveOutput(ctx, out); err != nil {
			t.Fatal(err)
		}
		if out.ID == 0 {
			t.Fatal("no id assigned")
		}
	}
	if _, err := db.SaveOutput(ctx, &ai.Output{
		Kind: "explain_move", Symbol: "AAPL", CreatedAt: base, Model: "m", Content: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	latest, ok, err := db.LatestOutput(ctx, "morning_brief", "")
	if err != nil || !ok {
		t.Fatalf("latest brief: ok=%v err=%v", ok, err)
	}
	if string(latest.Content) != `{"headline":"day c"}` {
		t.Errorf("latest = %s, want the newest", latest.Content)
	}

	list, err := db.ListOutputs(ctx, "morning_brief", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Errorf("got %d briefs, want 3", len(list))
	}

	// Symbol-scoped lookup.
	if _, ok, _ := db.LatestOutput(ctx, "explain_move", "AAPL"); !ok {
		t.Error("symbol-scoped lookup found nothing")
	}
	if _, ok, _ := db.LatestOutput(ctx, "explain_move", "MSFT"); ok {
		t.Error("symbol-scoped lookup matched the wrong symbol")
	}
	if _, ok, _ := db.LatestOutput(ctx, "nothing_here", ""); ok {
		t.Error("an absent kind reported a result")
	}
}

func TestOutlookLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	o := &ai.Outlook{
		Symbol: "RELIANCE.BSE", CreatedAt: created, HorizonDays: 10, PriceAtCreation: 1316,
		Base:    ai.Scenario{Probability: 0.6, MovePercent: 0.5, Reasoning: "Range-bound."},
		Bull:    ai.Scenario{Probability: 0.25, MovePercent: 7, Reasoning: "Margin recovery."},
		Bear:    ai.Scenario{Probability: 0.15, MovePercent: -6, Reasoning: "Crude spike."},
		KeyRisk: "Refining margins",
		Model:   "test-model",
	}
	id, err := db.SaveOutlook(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("no id assigned")
	}

	list, err := db.ListOutlooks(ctx, "RELIANCE.BSE", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d outlooks, want 1", len(list))
	}
	got := list[0]
	if got.Base.Probability != 0.6 || got.Bull.MovePercent != 7 || got.Bear.Reasoning != "Crude spike." {
		t.Errorf("scenarios lost in storage: %+v", got)
	}
	if got.KeyRisk != "Refining margins" {
		t.Errorf("key risk = %q", got.KeyRisk)
	}
	if got.Resolved() {
		t.Error("a fresh outlook should not be resolved")
	}

	// Not due yet.
	if due, _ := db.DueOutlooks(ctx, created.AddDate(0, 0, 5)); len(due) != 0 {
		t.Errorf("got %d due outlooks before the horizon, want 0", len(due))
	}
	// Due after the horizon: 10 trading days is 14 calendar days.
	due, err := db.DueOutlooks(ctx, created.AddDate(0, 0, 20))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due outlooks, want 1", len(due))
	}

	resolvedAt := created.AddDate(0, 0, 15)
	price, move, brier := 1400.0, 6.38, 0.245
	due[0].ResolvedAt = &resolvedAt
	due[0].RealizedPrice = &price
	due[0].RealizedMove = &move
	due[0].ActualScenario = ai.ScenarioBull
	due[0].BrierScore = &brier
	if err := db.ResolveOutlook(ctx, &due[0]); err != nil {
		t.Fatal(err)
	}

	list, _ = db.ListOutlooks(ctx, "", 10)
	got = list[0]
	if !got.Resolved() {
		t.Fatal("the outlook was not marked resolved")
	}
	if got.ActualScenario != ai.ScenarioBull || got.BrierScore == nil || *got.BrierScore != 0.245 {
		t.Errorf("resolution lost: %+v", got)
	}

	// Already resolved, so no longer due.
	if due, _ := db.DueOutlooks(ctx, created.AddDate(0, 0, 40)); len(due) != 0 {
		t.Errorf("a resolved outlook is still reported as due")
	}
}

func TestResolveOutlookIsIdempotent(t *testing.T) {
	// A later price must not rewrite history. Re-resolving is a no-op.
	db := newTestDB(t)
	ctx := context.Background()
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	o := &ai.Outlook{
		Symbol: "AAPL", CreatedAt: created, HorizonDays: 10, PriceAtCreation: 100,
		Base: ai.Scenario{Probability: 1}, Bull: ai.Scenario{MovePercent: 10}, Bear: ai.Scenario{MovePercent: -10},
	}
	db.SaveOutlook(ctx, o)

	at := created.AddDate(0, 0, 15)
	first, firstScore := 110.0, 0.5
	o.ResolvedAt, o.RealizedPrice, o.BrierScore = &at, &first, &firstScore
	o.ActualScenario = ai.ScenarioBull
	if err := db.ResolveOutlook(ctx, o); err != nil {
		t.Fatal(err)
	}

	second, secondScore := 50.0, 1.9
	o.RealizedPrice, o.BrierScore = &second, &secondScore
	o.ActualScenario = ai.ScenarioBear
	if err := db.ResolveOutlook(ctx, o); err != nil {
		t.Fatal(err)
	}

	list, _ := db.ListOutlooks(ctx, "AAPL", 10)
	if *list[0].RealizedPrice != 110 || list[0].ActualScenario != ai.ScenarioBull {
		t.Errorf("a second resolution overwrote the first: %+v", list[0])
	}
}

func TestResolveWithoutTimeIsRejected(t *testing.T) {
	db := newTestDB(t)
	if err := db.ResolveOutlook(context.Background(), &ai.Outlook{ID: 1}); err == nil {
		t.Error("want an error when resolving with no resolution time")
	}
}

func TestSearchCache(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	if _, ok, err := db.LoadSearch(ctx, "missing"); err != nil || ok {
		t.Errorf("a miss should be ok=false with no error, got ok=%v err=%v", ok, err)
	}

	results := search.Results{
		Query:    "reliance news",
		Provider: "tavily",
		Results: []search.Result{
			{Title: "A story", URL: "https://example.com/a", Snippet: "text", Source: "example.com", Published: at},
		},
		FetchedAt: at,
	}
	if err := db.SaveSearch(ctx, "key1", results); err != nil {
		t.Fatal(err)
	}

	got, ok, err := db.LoadSearch(ctx, "key1")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.Query != "reliance news" || got.Provider != "tavily" {
		t.Errorf("provenance lost: %+v", got)
	}
	if len(got.Results) != 1 || got.Results[0].Title != "A story" {
		t.Errorf("results lost: %+v", got.Results)
	}
	if !got.FetchedAt.Equal(at) {
		t.Errorf("fetchedAt = %v, want %v", got.FetchedAt, at)
	}

	// Re-saving the same key replaces rather than duplicating.
	results.Provider = "brave"
	if err := db.SaveSearch(ctx, "key1", results); err != nil {
		t.Fatal(err)
	}
	got, _, _ = db.LoadSearch(ctx, "key1")
	if got.Provider != "brave" {
		t.Errorf("provider = %q, want the updated value", got.Provider)
	}
}

func TestArticleStorage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	articles := []news.Article{
		{Symbol: "AAPL", Title: "Apple story one", URL: "https://a.com/1", Source: "a.com", PublishedAt: at, FetchedAt: at},
		{Symbol: "AAPL", Title: "Apple story two", URL: "https://a.com/2", Source: "a.com", PublishedAt: at.Add(-time.Hour), FetchedAt: at},
		{Symbol: "RELIANCE.BSE", Title: "Reliance story", URL: "https://b.com/1", Source: "b.com", FetchedAt: at},
	}
	added, err := db.SaveArticles(ctx, articles)
	if err != nil {
		t.Fatal(err)
	}
	if added != 3 {
		t.Errorf("added = %d, want 3", added)
	}

	// Re-polling the same feed must converge, not duplicate.
	added, err = db.SaveArticles(ctx, articles)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Errorf("re-saving added %d, want 0", added)
	}
	got, _ := db.ListArticles(ctx, "AAPL", 10)
	if len(got) != 2 {
		t.Errorf("AAPL holds %d articles, want 2", len(got))
	}

	// The same URL under a different symbol is a separate row, because
	// relevance is a per-symbol judgement.
	added, err = db.SaveArticles(ctx, []news.Article{
		{Symbol: "MSFT", Title: "Apple story one", URL: "https://a.com/1", Source: "a.com", FetchedAt: at},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 for the same URL under a new symbol", added)
	}

	// An undated article keeps a null published_at rather than pretending to
	// have been published now.
	rel, _ := db.ListArticles(ctx, "RELIANCE.BSE", 10)
	if !rel[0].PublishedAt.IsZero() {
		t.Errorf("undated article got a published time of %v", rel[0].PublishedAt)
	}
}

func TestArticleScoring(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	db.SaveArticles(ctx, []news.Article{
		{Symbol: "AAPL", Title: "One", URL: "https://a.com/1", FetchedAt: at},
		{Symbol: "AAPL", Title: "Two", URL: "https://a.com/2", FetchedAt: at.Add(time.Minute)},
	})

	unscored, err := db.ListUnscored(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(unscored) != 2 {
		t.Fatalf("got %d unscored, want 2", len(unscored))
	}
	// Oldest first, so nothing starves at the back of the queue.
	if unscored[0].Title != "One" {
		t.Errorf("first unscored = %q, want the oldest", unscored[0].Title)
	}
	if unscored[0].Scored() {
		t.Error("an unscored article reports as scored")
	}

	if err := db.SaveScores(ctx, []news.Score{
		{ArticleID: unscored[0].ID, Relevance: 0.9, Sentiment: -0.4, OneLine: "Guidance cut.", Model: "cheap", ScoredAt: at},
	}); err != nil {
		t.Fatal(err)
	}

	unscored, _ = db.ListUnscored(ctx, 10)
	if len(unscored) != 1 {
		t.Errorf("got %d unscored after scoring one, want 1", len(unscored))
	}

	// Scored, relevant articles sort first — a prompt should get the most
	// useful context, not merely the newest.
	got, _ := db.ListArticles(ctx, "AAPL", 10)
	if got[0].Title != "One" {
		t.Errorf("first article = %q, want the scored one", got[0].Title)
	}
	if got[0].Relevance == nil || *got[0].Relevance != 0.9 {
		t.Errorf("relevance = %v, want 0.9", got[0].Relevance)
	}
	if got[0].Sentiment == nil || *got[0].Sentiment != -0.4 {
		t.Errorf("sentiment = %v, want -0.4", got[0].Sentiment)
	}
	if !got[0].Scored() {
		t.Error("a scored article does not report as scored")
	}
	// An unscored article keeps nil rather than a misleading zero.
	if got[1].Relevance != nil {
		t.Errorf("unscored relevance = %v, want nil", got[1].Relevance)
	}
}

func TestPruneArticles(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	db.SaveArticles(ctx, []news.Article{
		{Symbol: "AAPL", Title: "Old", URL: "https://a.com/old", PublishedAt: now.AddDate(0, 0, -60), FetchedAt: now.AddDate(0, 0, -60)},
		{Symbol: "AAPL", Title: "Recent", URL: "https://a.com/new", PublishedAt: now.AddDate(0, 0, -2), FetchedAt: now},
	})

	pruned, err := db.PruneArticles(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Errorf("pruned %d, want 1", pruned)
	}
	got, _ := db.ListArticles(ctx, "AAPL", 10)
	if len(got) != 1 || got[0].Title != "Recent" {
		t.Errorf("remaining = %+v, want only the recent article", got)
	}
}

func TestWatchedSymbols(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, s := range []string{"RELIANCE.BSE", "AAPL"} {
		db.AddWatchlist(ctx, mustSymbol(t, s), "")
	}
	got, err := db.WatchedSymbols(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "RELIANCE.BSE" {
		t.Errorf("watched symbols = %v", got)
	}
}

func TestRecentAlertSummaries(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	for i, s := range []string{"AAPL", "RELIANCE.BSE"} {
		db.InsertAlert(ctx, newAlert(int64(i+1), s, base.Add(time.Duration(i)*time.Hour)))
	}
	// One from long ago that the brief should not mention.
	db.InsertAlert(ctx, newAlert(3, "TCS.BSE", base.AddDate(0, 0, -5)))

	got, err := db.RecentAlertSummaries(ctx, base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d summaries, want 2", len(got))
	}
	if got[0].Symbol != "RELIANCE.BSE" {
		t.Errorf("first = %q, want the newest", got[0].Symbol)
	}
	if got[0].Summary == "" || got[0].AlgorithmName == "" {
		t.Errorf("summary is missing fields: %+v", got[0])
	}
}
