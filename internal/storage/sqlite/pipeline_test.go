package sqlite

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage"
)

func pipelineFixtures(t *testing.T) (*DB, *events.Processor, context.Context, time.Time) {
	t.Helper()
	db := newTestDB(t)
	master, err := company.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	registry, err := news.DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry: %v", err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	p := events.NewProcessor(db, master, registry,
		events.WithProcessorClock(func() time.Time { return now }))
	return db, p, context.Background(), now
}

func item(source, hash, title, desc, url string, discovered time.Time) news.RawItem {
	return news.RawItem{
		SourceID: source, ContentHash: hash, Title: title, Description: desc,
		URL: url, CanonicalURL: news.CanonicalURL(url),
		PublishedAt: discovered.Add(-2 * time.Minute), DiscoveredAt: discovered, FetchedAt: discovered,
	}
}

// TestPipelineFilingBecomesEvent walks one exchange filing through the whole
// deterministic path: parsed, typed, entity-resolved, stored with its facts.
func TestPipelineFilingBecomesEvent(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-announcements", "h1",
			"Larsen & Toubro Limited",
			"Larsen & Toubro Limited has informed the Exchange regarding receipt of an order worth Rs 4,200 crore |SUBJECT: Bagging/Receiving of orders/contracts",
			"https://nsearchives.nseindia.com/corporate/LT_25082026120000_Order.pdf", now),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := p.ProcessBatch(ctx, 100)
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("created %d events, want 1 (result %+v)", res.Created, res)
	}

	list, err := db.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d events, want 1", len(list))
	}
	e := list[0]
	if e.Type != string(events.TypeOrderWin) {
		t.Errorf("type = %s, want ORDER_WIN", e.Type)
	}
	if !e.Official {
		t.Error("an NSE filing must be marked official")
	}
	if e.Importance == nil || *e.Importance < 7 {
		t.Errorf("importance = %v, want the ORDER_WIN baseline", e.Importance)
	}
	if len(e.Entities) != 1 || e.Entities[0].Symbol != "LT" {
		t.Fatalf("entities = %+v, want LT resolved from the document path", e.Entities)
	}
	if e.Entities[0].MatchMethod != "nse_document_path" {
		t.Errorf("match method = %s, want the path symbol to win", e.Entities[0].MatchMethod)
	}
	if e.ConfirmedAt.IsZero() {
		t.Error("an official filing is its own confirmation")
	}
}

// TestPipelineExtractsDividendFacts is the claim that the numbers come out
// without a model.
func TestPipelineExtractsDividendFacts(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-corp-actions", "d1",
			"Relaxo Footwears Limited - Ex-Date: 17-Sep-2026",
			"SERIES:EQ |PURPOSE:DIVIDEND - RS 3.50 PER SHARE |FACE VALUE:1 |RECORD DATE:18-Sep-2026",
			"https://www.nseindia.com/companies-listing/corporate-filings-actions", now),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}

	list, _ := db.ListEvents(ctx, EventFilter{})
	if len(list) != 1 {
		t.Fatalf("got %d events", len(list))
	}
	if list[0].Type != string(events.TypeDividend) {
		t.Errorf("type = %s, want DIVIDEND", list[0].Type)
	}
	facts, err := db.EventFacts(ctx, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := facts["PURPOSE"]; got != "DIVIDEND - RS 3.50 PER SHARE" {
		t.Errorf("PURPOSE = %q", got)
	}
	if got := facts["RECORD DATE"]; got != "18-Sep-2026" {
		t.Errorf("RECORD DATE = %q", got)
	}
	// The ex-date lives in the title, not the description.
	if got := facts["EX DATE"]; got != "17-Sep-2026" {
		t.Errorf("EX DATE = %q, want it lifted out of the title", got)
	}
	if list[0].OccurredAt.IsZero() {
		t.Error("a dated corporate action must carry an occurred_at")
	}
}

// TestPipelineFiltersFundNAV pins the noise filter: 440 of a day's 1,600 NSE
// announcements are mutual-fund NAV declarations.
func TestPipelineFiltersFundNAV(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-announcements", "n1", "Some AMC Mutual Fund",
			"Some AMC has informed the Exchange regarding NAV |SUBJECT: Declaration of NAV",
			"https://nsearchives.nseindia.com/corporate/x.pdf", now),
		item("nse-announcements", "n2", "Infosys Limited",
			"Infosys Limited has informed the Exchange regarding a press release |SUBJECT: Press Release",
			"https://nsearchives.nseindia.com/corporate/INFY_25082026120000_pr.pdf", now),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := p.ProcessBatch(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filtered != 1 {
		t.Errorf("filtered = %d, want the NAV declaration held back", res.Filtered)
	}
	if res.Created != 1 {
		t.Errorf("created = %d, want only the equity filing", res.Created)
	}
	// The filtered item is still on record: raw evidence is never discarded.
	held, _ := db.CountRawItems(ctx)
	if held != 2 {
		t.Errorf("raw items held = %d, want both retained", held)
	}
}

// TestPipelineClustersAcrossSources is the article-versus-story distinction:
// one event, several pieces of evidence.
func TestPipelineClustersAcrossSources(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-announcements", "c1", "Tata Steel Limited",
			"Tata Steel Limited has informed the Exchange regarding acquisition of a stake in a European mill |SUBJECT: Acquisition",
			"https://nsearchives.nseindia.com/corporate/TATASTEEL_25082026120000_a.pdf", now),
		item("et-markets", "c2",
			"Tata Steel acquisition of stake in European mill", "",
			"https://economictimes.indiatimes.com/x", now.Add(20*time.Minute)),
		item("bs-markets", "c3",
			"Tata Steel acquisition European mill stake", "",
			"https://www.business-standard.com/y", now.Add(40*time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := p.ProcessBatch(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 {
		t.Errorf("created = %d, want one event (result %+v)", res.Created, res)
	}
	if res.Merged != 2 {
		t.Errorf("merged = %d, want the two reports attached as evidence", res.Merged)
	}

	list, _ := db.ListEvents(ctx, EventFilter{})
	if len(list) != 1 {
		t.Fatalf("got %d events, want 1", len(list))
	}
	full, err := db.GetEvent(ctx, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Evidence) != 3 {
		t.Errorf("evidence = %d items, want 3", len(full.Evidence))
	}
	if full.SourceCount != 3 {
		t.Errorf("source_count = %d, want 3", full.SourceCount)
	}
	if full.BestTrust != 100 {
		t.Errorf("best_trust = %d, want the exchange's 100", full.BestTrust)
	}
}

// TestPipelineKeepsUnrelatedEventsApart is the other half of clustering, and
// the more important one: merging two different events is worse than showing
// a duplicate.
func TestPipelineKeepsUnrelatedEventsApart(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-announcements", "k1", "Reliance Industries Limited",
			"Reliance Industries Limited has informed the Exchange regarding receipt of a large order |SUBJECT: Bagging/Receiving of orders/contracts",
			"https://nsearchives.nseindia.com/corporate/RELIANCE_25082026120000_a.pdf", now),
		item("nse-announcements", "k2", "Reliance Industries Limited",
			"Reliance Industries Limited has informed the Exchange regarding a SEBI order imposing a penalty |SUBJECT: Actions initiated/taken or orders passed",
			"https://nsearchives.nseindia.com/corporate/RELIANCE_25082026130000_b.pdf", now.Add(time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := p.ProcessBatch(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 {
		t.Fatalf("created = %d, want two distinct events for one company on one day", res.Created)
	}
	list, _ := db.ListEvents(ctx, EventFilter{})
	types := map[string]bool{}
	for _, e := range list {
		types[e.Type] = true
	}
	if !types[string(events.TypeOrderWin)] || !types[string(events.TypeRegulatoryAction)] {
		t.Errorf("types = %v, want ORDER_WIN and REGULATORY_ACTION kept apart", types)
	}
}

// TestPipelineIsIdempotent checks that reprocessing cannot fork an event,
// which is what makes a parser fix safe to deploy.
func TestPipelineIsIdempotent(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	raw := item("nse-announcements", "i1", "Wipro Limited",
		"Wipro Limited has informed the Exchange regarding a credit rating action |SUBJECT: Credit Rating",
		"https://nsearchives.nseindia.com/corporate/WIPRO_25082026120000_c.pdf", now)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// Re-open the item for processing, as a reprocessing job would.
	if _, err := db.db.ExecContext(ctx, `UPDATE raw_items SET processed_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}

	n, err := db.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("events = %d after reprocessing, want 1", n)
	}
}

func TestListEventsFilters(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-announcements", "f1", "Infosys Limited",
			"Infosys Limited has informed the Exchange regarding an order win |SUBJECT: Bagging/Receiving of orders/contracts",
			"https://nsearchives.nseindia.com/corporate/INFY_25082026120000_a.pdf", now),
		item("nse-announcements", "f2", "Wipro Limited",
			"Wipro Limited has informed the Exchange regarding a newspaper publication |SUBJECT: Copy of Newspaper Publication",
			"https://nsearchives.nseindia.com/corporate/WIPRO_25082026120100_b.pdf", now),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}

	bySymbol, _ := db.ListEvents(ctx, EventFilter{Symbol: "INFY"})
	if len(bySymbol) != 1 {
		t.Errorf("symbol filter returned %d, want 1", len(bySymbol))
	}
	byImportance, _ := db.ListEvents(ctx, EventFilter{MinImportance: 6})
	if len(byImportance) != 1 {
		t.Errorf("importance filter returned %d, want only the order win", len(byImportance))
	}
	byType, _ := db.ListEvents(ctx, EventFilter{Types: []string{"ORDER_WIN"}})
	if len(byType) != 1 {
		t.Errorf("type filter returned %d, want 1", len(byType))
	}
	byQuery, _ := db.ListEvents(ctx, EventFilter{Query: "newspaper"})
	if len(byQuery) != 1 {
		t.Errorf("text query returned %d, want 1", len(byQuery))
	}
}

// TestClusteringSurvivesABacklog reproduces a bug found in production.
//
// During the first bulk ingest, 293 events shared a single discovery second.
// The candidate query at the time asked for "events sharing a symbol OR events
// with no symbol at all" under one LIMIT, so hundreds of unrelated entity-less
// news items filled the result budget and the matching event was never
// considered. NSE publishes many filings twice — once as XBRL, once as PDF —
// and both copies became separate events.
//
// The fix gives each kind of candidate its own budget. This test pins that by
// burying the match under far more noise than the limit allows.
func TestClusteringSurvivesABacklog(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	// The filing, as XBRL: no symbol in the path.
	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-announcements", "x1", "Suzlon Energy Limited",
			"Suzlon Energy Limited has informed the Exchange about Bagging/Receiving of orders/contracts (Sub-para 4-Para B) |SUBJECT: Bagging/Receiving of orders/contracts",
			"https://nsearchives.nseindia.com/corporate/xbrl/REG30_PARA_B_2328_WebXMLFile.xml", now),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessBatch(ctx, 10); err != nil {
		t.Fatal(err)
	}

	// Now bury it under 300 entity-less items sharing the same second, which
	// is what a backfill looks like.
	var noise []news.RawItem
	for i := 0; i < 300; i++ {
		noise = append(noise, item("et-markets", "noise"+strconv.Itoa(i),
			"Global markets update number "+strconv.Itoa(i), "",
			"https://economictimes.indiatimes.com/n"+strconv.Itoa(i), now))
	}
	if _, err := db.SaveRawItems(ctx, noise); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessBatch(ctx, 400); err != nil {
		t.Fatal(err)
	}

	// The same filing arrives as a PDF. It must still find its twin.
	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-announcements", "x2", "Suzlon Energy Limited",
			"Suzlon Energy Limited has informed the Exchange about Bagging/Receiving of orders/contracts |SUBJECT: Bagging/Receiving of orders/contracts",
			"https://nsearchives.nseindia.com/corporate/SUZLON1_25082026093153_S.pdf", now.Add(time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := p.ProcessBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 1 {
		t.Errorf("merged = %d, want the duplicate filing attached to the existing event", res.Merged)
	}

	suzlon, err := db.ListEvents(ctx, EventFilter{Symbol: "SUZLON"})
	if err != nil {
		t.Fatal(err)
	}
	if len(suzlon) != 1 {
		t.Fatalf("got %d SUZLON events, want 1 — XBRL and PDF are one filing", len(suzlon))
	}
	if suzlon[0].SourceCount < 1 {
		t.Errorf("source count = %d", suzlon[0].SourceCount)
	}
}

// TestFilingHeadlineDropsBoilerplate pins the three prepositions NSE uses.
// Matching only "regarding" left the boilerplate on two thirds of headlines.
func TestFilingHeadlineDropsBoilerplate(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	for i, phrasing := range []string{
		"Infosys Limited has informed the Exchange about a credit rating action |SUBJECT: Credit Rating",
		"Infosys Limited has informed the Exchange that a credit rating action occurred |SUBJECT: Credit Rating",
		"Infosys Limited has informed the Exchange regarding a credit rating action |SUBJECT: Credit Rating",
	} {
		if _, err := db.SaveRawItems(ctx, []news.RawItem{
			item("nse-announcements", "b"+strconv.Itoa(i), "Infosys Limited", phrasing,
				"https://nsearchives.nseindia.com/corporate/INFY_2508202612000"+strconv.Itoa(i)+"_c.pdf",
				now.Add(time.Duration(i)*3*time.Hour)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.ProcessBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}
	list, _ := db.ListEvents(ctx, EventFilter{Symbol: "INFY"})
	if len(list) == 0 {
		t.Fatal("expected events")
	}
	for _, e := range list {
		if strings.Contains(strings.ToLower(e.Headline), "informed the exchange") {
			t.Errorf("headline still carries boilerplate: %q", e.Headline)
		}
	}
}

// TestSurveillanceHeadlineIsCondensed covers the exchange's surveillance
// notices, whose entire text is one 300-character statutory sentence. Used
// whole it is a paragraph, not a headline, and a feed of them is unreadable.
func TestSurveillanceHeadlineIsCondensed(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	const boilerplate = "Significant movement in price has been observed in Shanthi Gears Limited. " +
		"The Exchange, in order to ensure that investors have latest relevant information about the " +
		"company and to inform the market place so that the interest of the investors is safeguarded, " +
		"has written to the company. The response from the company is awaited. |SUBJECT: Price movement"

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("nse-announcements", "s1", "Shanthi Gears Limited", boilerplate, "", now),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessBatch(ctx, 10); err != nil {
		t.Fatal(err)
	}
	list, _ := db.ListEvents(ctx, EventFilter{})
	if len(list) != 1 {
		t.Fatalf("got %d events", len(list))
	}
	e := list[0]
	if len(e.Headline) > 140 {
		t.Errorf("headline is %d chars, want it condensed: %q", len(e.Headline), e.Headline)
	}
	if strings.Contains(e.Headline, "safeguarded") {
		t.Errorf("statutory boilerplate leaked into the headline: %q", e.Headline)
	}
	if !strings.Contains(e.Headline, "Shanthi Gears") {
		t.Errorf("headline lost the company: %q", e.Headline)
	}
	if e.Type != string(events.TypePriceMovement) {
		t.Errorf("type = %s, want PRICE_MOVEMENT", e.Type)
	}
	// The full text must survive in the summary; only the headline is cut.
	if !strings.Contains(e.Summary, "safeguarded") {
		t.Errorf("the full notice must remain in the summary, got %q", e.Summary)
	}
}

// TestWatchlistItemsAreAttributedToTheirCompany covers a defect that made
// watchlist coverage useless: 799 of 800 items from per-company searches were
// discarded, because the processor resolved companies from the headline and
// ignored the fact that the query itself named one.
//
// "Ambani weighs aluminium entry" is about Reliance. No text matching will say
// so; the source knows because that is what it searched for.
func TestWatchlistItemsAreAttributedToTheirCompany(t *testing.T) {
	db := newTestDB(t)
	master, err := company.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	// A registry containing one watchlist source, as the running system builds.
	registry, err := news.NewRegistry(news.WatchlistSource("RELIANCE", "Reliance Industries Limited"))
	if err != nil {
		t.Fatal(err)
	}
	p := events.NewProcessor(db, master, registry,
		events.WithProcessorClock(func() time.Time { return now }))

	ctx := context.Background()
	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("watch-reliance", "w1",
			"Ambani weighs aluminium entry, setting up potential clash", "",
			"https://news.google.com/rss/articles/abc", now),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := p.ProcessBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 {
		t.Fatalf("created %d, want 1 (filtered %d) — a watchlist item must not be discarded",
			res.Created, res.Filtered)
	}

	list, err := db.ListEvents(ctx, storage.EventFilter{Symbol: "RELIANCE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("found %d events for RELIANCE, want 1", len(list))
	}
	ents := list[0].Entities
	if len(ents) == 0 || ents[0].Symbol != "RELIANCE" {
		t.Fatalf("entities = %+v, want RELIANCE attributed from the query", ents)
	}
	if ents[0].Relationship != news.RelPrimary {
		t.Errorf("relationship = %s, want primary", ents[0].Relationship)
	}
	if ents[0].MatchMethod != "watchlist_query" {
		t.Errorf("method = %s, want watchlist_query: the headline does not name the company",
			ents[0].MatchMethod)
	}
}

// TestWatchlistBypassesTheRelevanceFilter: the operator has said this
// instrument matters, which settles the question the filter exists to ask.
func TestWatchlistBypassesTheRelevanceFilter(t *testing.T) {
	db := newTestDB(t)
	master, _ := company.LoadEmbedded()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	// A US name the content filter would otherwise discard as unactionable.
	registry, err := news.NewRegistry(news.WatchlistSource("AAPL", ""))
	if err != nil {
		t.Fatal(err)
	}
	p := events.NewProcessor(db, master, registry,
		events.WithProcessorClock(func() time.Time { return now }))

	ctx := context.Background()
	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("watch-aapl", "a1", "US stocks close higher as Apple leads tech rebound", "",
			"https://news.google.com/rss/articles/xyz", now),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := p.ProcessBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filtered != 0 {
		t.Errorf("filtered %d, want 0: a watched instrument is not noise", res.Filtered)
	}
	if res.Created != 1 {
		t.Errorf("created %d, want 1", res.Created)
	}
}
