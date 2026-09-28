package sqlite

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage"
)

func pipelineFixtures(t *testing.T) (*DB, *events.Processor, context.Context, time.Time) {
	t.Helper()
	db := newTestDB(t)
	master, err := company.LoadEmbeddedUSMaster()
	if err != nil {
		t.Fatalf("LoadEmbeddedUSMaster: %v", err)
	}
	// SEC sources are gated on a declared contact string at runtime, so the
	// default registry does not carry them. The filing tests below need them
	// registered: the processor looks a raw item's source up by id to learn
	// its method and trust, and an unknown source is filtered out.
	sources := append(news.DefaultSources(), news.SECSources("TradeSys-test/1.0 (test@example.com)")...)
	registry, err := news.NewRegistry(sources...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	// SEC entity resolution goes through the filer's declared CIK rather than
	// a text guess, so the fixture supplies the same index main wires in.
	p := events.NewProcessor(db, master, registry,
		events.WithUSCIKIndex(map[string]string{
			"0000018230": "CAT",  // Caterpillar
			"0000789019": "MSFT", // Microsoft
			"0000073309": "NUE",  // Nucor
			"0001274494": "FSLR", // First Solar
		}),
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

// TestPipelineFilingBecomesEvent walks one SEC filing through the whole
// deterministic path: parsed, typed, entity-resolved from the filer's own CIK,
// and stored with the item codes it declared.
//
// This used to feed an NSE corporate announcement through ParseFiling. That
// parser and the format it read are both gone, so the test now exercises the
// filing path the product actually has.
func TestPipelineFilingBecomesEvent(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("sec-8k", "h1",
			"8-K - CATERPILLAR INC (0000018230) (Filer)",
			"Filed: 2026-08-25 AccNo: 0000018230-26-000123 Size: 191 KB "+
				"Item 1.01: Entry into a Material Definitive Agreement",
			"https://www.sec.gov/Archives/edgar/data/18230/000001823026000123-index.htm", now),
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
	if !e.Official {
		t.Error("an SEC filing must be marked official")
	}
	if len(e.Entities) != 1 || e.Entities[0].Symbol != "CAT" {
		t.Fatalf("entities = %+v, want CAT resolved from the filer's CIK", e.Entities)
	}
	if e.ConfirmedAt.IsZero() {
		t.Error("an official filing is its own confirmation")
	}

	// The filer's own declaration is kept verbatim beside the derived type:
	// it is a stronger signal than any classifier, and a filing naming
	// several items has more going on than one type can carry.
	facts, err := db.EventFacts(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := facts["SEC_ITEMS"]; got != "1.01" {
		t.Errorf("SEC_ITEMS = %q, want the declared item code", got)
	}
	if got := facts["SEC_CIK"]; got != "0000018230" {
		t.Errorf("SEC_CIK = %q, want the filer's CIK on the event", got)
	}
}

// TestPipelineExtractsFilingFactsWithoutAModel is the claim that the
// structured detail comes out deterministically.
//
// It used to assert the PURPOSE / RECORD DATE / EX DATE fields lifted out of
// NSE's pipe-delimited corporate-action format. Those fields do not exist in
// any feed this product now reads; the equivalent claim for SEC is that a
// filing declaring several 8-K items has all of them recorded, not just the
// one the type maps to.
func TestPipelineExtractsFilingFactsWithoutAModel(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("sec-8k", "d1",
			"8-K - NUCOR CORP (0000073309) (Filer)",
			"Filed: 2026-08-25 AccNo: 0000073309-26-000044 Size: 88 KB "+
				"Item 2.02: Results of Operations and Financial Condition "+
				"Item 8.01: Other Events",
			"https://www.sec.gov/Archives/edgar/data/73309/000007330926000044-index.htm", now),
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
	if list[0].Type != string(events.TypeEarnings) {
		t.Errorf("type = %s, want EARNINGS from item 2.02", list[0].Type)
	}
	facts, err := db.EventFacts(ctx, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// Both codes, in the order the filer declared them -- the second is the
	// part a single Type would have thrown away.
	if got := facts["SEC_ITEMS"]; got != "2.02,8.01" {
		t.Errorf("SEC_ITEMS = %q, want both declared items", got)
	}
	if list[0].OccurredAt.IsZero() {
		t.Error("a filing carries its own filed date as occurred_at")
	}
}

// TestPipelineFiltersFundNAV pins the noise filter. A fund's net asset value
// is arithmetic on holdings that already moved, not news about a company, so
// it never becomes an event -- the judgement is about the instrument, not
// about importance. See Type.Equity in internal/events/taxonomy.go.
func TestPipelineFiltersFundNAV(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("prnewswire", "n1", "Vanguard Growth Fund declaration of NAV",
			"Declaration of NAV for the period ending August 25.",
			"https://www.prnewswire.com/news-releases/nav-1.html", now),
		item("prnewswire", "n2", "Microsoft Corporation announces a quarterly dividend",
			"Microsoft Corporation declared a quarterly dividend of $0.83 per share.",
			"https://www.prnewswire.com/news-releases/msft-dividend.html", now),
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
		// The company's own release, then two rewrites of it -- the ordinary
		// US shape of one story arriving three times.
		item("globenewswire-public", "c1",
			"Nucor completes acquisition of a stake in a European mill",
			"Nucor Corporation today completed the acquisition of a stake in a European mill.",
			"https://www.globenewswire.com/news-release/nucor-european-mill", now),
		item("wsj-markets", "c2",
			"Nucor completes acquisition of a stake in a European mill", "",
			"https://www.wsj.com/articles/nucor-european-mill", now.Add(20*time.Minute)),
		item("cnbc-finance", "c3",
			"Nucor acquisition of European mill stake completes", "",
			"https://www.cnbc.com/2026/08/25/nucor-mill.html", now.Add(40*time.Minute)),
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
	// The release outranks both rewrites of it, so the event carries the
	// company's own trust rather than a newspaper's.
	if full.BestTrust != 90 {
		t.Errorf("best_trust = %d, want the company release's 90", full.BestTrust)
	}
}

// TestPipelineKeepsUnrelatedEventsApart is the other half of clustering, and
// the more important one: merging two different events is worse than showing
// a duplicate.
func TestPipelineKeepsUnrelatedEventsApart(t *testing.T) {
	db, p, ctx, now := pipelineFixtures(t)

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("wsj-markets", "k1",
			"First Solar wins a contract to supply a 900 MW project",
			"First Solar said it had won a contract to supply panels for a 900 MW utility project.",
			"https://www.wsj.com/articles/first-solar-contract", now),
		item("wsj-markets", "k2",
			"SEC fines First Solar over disclosure failures",
			"The SEC issued an order imposing a penalty on First Solar over disclosure failures.",
			"https://www.wsj.com/articles/first-solar-sec-penalty", now.Add(time.Hour)),
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

	raw := item("sec-press", "i1", "Accenture plc",
		"Accenture plc has informed the Exchange regarding a credit rating action |SUBJECT: Credit Rating",
		"https://www.sec.gov/Archives/edgar/data/WIPRO_25082026120000_c.pdf", now)

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
		item("globenewswire-public", "f1",
			"Microsoft wins a contract to supply a federal cloud programme",
			"Microsoft Corporation said it had won a contract to supply cloud services to a federal programme.",
			"https://www.globenewswire.com/news-release/msft-contract", now),
		item("globenewswire-public", "f2",
			"Accenture publishes a newspaper notice of its annual meeting",
			"Accenture plc published a newspaper notice of its annual general meeting.",
			"https://www.globenewswire.com/news-release/acn-notice", now),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}

	bySymbol, _ := db.ListEvents(ctx, EventFilter{Symbol: "MSFT"})
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
		item("sec-press", "x1", "First Solar Inc",
			"First Solar Inc has informed the Exchange about Bagging/Receiving of orders/contracts (Sub-para 4-Para B) |SUBJECT: Bagging/Receiving of orders/contracts",
			"https://www.sec.gov/Archives/edgar/data/xbrl/REG30_PARA_B_2328_WebXMLFile.xml", now),
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
		noise = append(noise, item("wsj-markets", "noise"+strconv.Itoa(i),
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
		item("sec-press", "x2", "First Solar Inc",
			"First Solar Inc has informed the Exchange about Bagging/Receiving of orders/contracts |SUBJECT: Bagging/Receiving of orders/contracts",
			"https://www.sec.gov/Archives/edgar/data/SUZLON1_25082026093153_S.pdf", now.Add(time.Minute)),
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

	suzlon, err := db.ListEvents(ctx, EventFilter{Symbol: "FSLR"})
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
		"Microsoft Corporation has informed the Exchange about a credit rating action |SUBJECT: Credit Rating",
		"Microsoft Corporation has informed the Exchange that a credit rating action occurred |SUBJECT: Credit Rating",
		"Microsoft Corporation has informed the Exchange regarding a credit rating action |SUBJECT: Credit Rating",
	} {
		if _, err := db.SaveRawItems(ctx, []news.RawItem{
			item("sec-press", "b"+strconv.Itoa(i), "Microsoft Corporation", phrasing,
				"https://www.sec.gov/Archives/edgar/data/INFY_2508202612000"+strconv.Itoa(i)+"_c.pdf",
				now.Add(time.Duration(i)*3*time.Hour)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.ProcessBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}
	list, _ := db.ListEvents(ctx, EventFilter{Symbol: "MSFT"})
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

	// A release whose substance is one clause followed by several sentences of
	// legal boilerplate -- the ordinary shape of a US press release, and the
	// reason a headline cannot just be the first 140 characters of the body.
	const boilerplate = "Significant movement in the price of Timken Company shares has been observed. " +
		"This release contains forward-looking statements which involve risks and uncertainties, and " +
		"the interests of shareholders are safeguarded by the disclosures set out in the company's " +
		"most recent annual report on file with the Commission. Readers are cautioned not to place " +
		"undue reliance on them. The response from the company is awaited."

	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("globenewswire-public", "s1",
			"Timken Company: significant price movement", boilerplate, "", now),
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
	if !strings.Contains(e.Headline, "Timken") {
		t.Errorf("headline lost the company: %q", e.Headline)
	}
	// No type assertion here any more. PRICE_MOVEMENT was only ever reachable
	// through NSE's own subject vocabulary, which went with that parser; this
	// test is about the condensing, which is what US releases need.
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
// "The oil major weighs a lithium entry" is about Exxon. No text matching will
// say so; the source knows, because that is what it searched for.
func TestWatchlistItemsAreAttributedToTheirCompany(t *testing.T) {
	db := newTestDB(t)
	master, err := company.LoadEmbeddedUSMaster()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	// A registry containing one watchlist source, as the running system builds.
	registry, err := news.NewRegistry(news.WatchlistSource(
		marketdata.Symbol{Ticker: "XOM", Exchange: marketdata.ExchangeUS}, "Exxon Mobil Corporation"))
	if err != nil {
		t.Fatal(err)
	}
	p := events.NewProcessor(db, master, registry,
		events.WithProcessorClock(func() time.Time { return now }))

	ctx := context.Background()
	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("watch-xom", "w1",
			"The oil major weighs a lithium entry, setting up a potential clash", "",
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

	list, err := db.ListEvents(ctx, storage.EventFilter{Symbol: "XOM"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("found %d events for RELIANCE.NSE, want 1", len(list))
	}
	ents := list[0].Entities
	if len(ents) == 0 || ents[0].Symbol != "XOM" {
		t.Fatalf("entities = %+v, want RELIANCE.NSE attributed from the query", ents)
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
	registry, err := news.NewRegistry(news.WatchlistSource(marketdata.Symbol{Ticker: "AAPL"}, ""))
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

// TestSECFilingResolvesEntityByCIK is the CIK path's regression: an SEC
// filing's issuer resolves to the correct US symbol without any text
// matching, and the "Reporting" (insider person) side of a Form 4 gets no
// entity at all rather than being matched against the wrong CIK.
func TestSECFilingResolvesEntityByCIK(t *testing.T) {
	db := newTestDB(t)
	master, err := company.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	registry, err := news.NewRegistry(news.SECSources("TradeSys/1.0 (test@example.com)")...)
	if err != nil {
		t.Fatal(err)
	}
	p := events.NewProcessor(db, master, registry,
		events.WithProcessorClock(func() time.Time { return now }),
		events.WithUSCIKIndex(map[string]string{"0001795586": "CHYM", "0001376066": "SOMEPERSON"}))

	ctx := context.Background()
	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("sec-form4", "issuer1", "4 - Chime Financial, Inc. (0001795586) (Issuer)",
			"Filed: 2026-09-14 AccNo: 0001376066-26-000007 Size: 25 KB",
			"https://www.sec.gov/Archives/edgar/data/1795586/x-index.htm", now),
		item("sec-form4", "reporting1", "4 - CAROLAN SHAWN T (0001376066) (Reporting)",
			"Filed: 2026-09-14 AccNo: 0001376066-26-000007 Size: 25 KB",
			"https://www.sec.gov/Archives/edgar/data/1376066/x-index.htm", now),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := p.ProcessBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 {
		t.Fatalf("created %d events, want 2 (one per raw item, filtered=%d noentity=%d)", res.Created, res.Filtered, res.NoEntity)
	}

	issuerEvents, err := db.ListEvents(ctx, storage.EventFilter{Symbol: "CHYM"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issuerEvents) != 1 {
		t.Fatalf("events for CHYM = %d, want 1", len(issuerEvents))
	}
	if len(issuerEvents[0].Entities) != 1 || issuerEvents[0].Entities[0].Symbol != "CHYM" ||
		issuerEvents[0].Entities[0].MatchMethod != "sec_cik" {
		t.Errorf("issuer entities = %+v, want one CHYM entity matched via sec_cik", issuerEvents[0].Entities)
	}

	// The Reporting-role entry must not resolve to anything -- it names a
	// person, not the company, and attaching a symbol to it via the same
	// CIK-lookup path would be attributing a form filed by an individual to
	// a company they merely work for or sit on the board of.
	reportingEvents, err := db.ListEvents(ctx, storage.EventFilter{Symbol: "SOMEPERSON"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reportingEvents) != 0 {
		t.Fatalf("events for SOMEPERSON = %d, want 0: the Reporting role must not resolve via CIK", len(reportingEvents))
	}
}

// TestFederalRegisterUsesAgencySectorTable is the regression for the
// agency-based sector signal: a Federal Register item from a known agency
// (USTR) must be tagged with that agency's sectors even when its own title
// carries none of the keywords InferSectors would otherwise need to find
// the same answer.
func TestFederalRegisterUsesAgencySectorTable(t *testing.T) {
	db := newTestDB(t)
	master, err := company.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	registry, err := news.NewRegistry(news.Source{
		ID: "federal-register", Name: "Federal Register", URL: "https://www.federalregister.gov/api/v1/documents.json",
		Method: news.MethodFederalRegister, Category: "regulatory", Country: "US", Language: "en",
		Trust: news.TrustOfficial, Refresh: 20 * time.Minute, Timeout: 20 * time.Second,
		Usage: news.UsageOfficial, Display: news.DisplayFull, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := events.NewProcessor(db, master, registry, events.WithProcessorClock(func() time.Time { return now }))

	ctx := context.Background()
	// Deliberately a title with no sector keyword at all -- the agency tag
	// is the only thing that can produce a sector for this headline.
	if _, err := db.SaveRawItems(ctx, []news.RawItem{
		item("federal-register", "fr1",
			"Notice of Actions in Section 301 Investigations",
			"A routine procedural notice. |AGENCY: Office of the United States Trade Representative |DOCTYPE: Notice",
			"https://www.federalregister.gov/documents/2026/09/14/example", now),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := p.ProcessBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 {
		t.Fatalf("created %d, want 1 (filtered=%d)", res.Created, res.Filtered)
	}

	list, err := db.ListEvents(ctx, storage.EventFilter{
		Query: "", Limit: 5, Since: now.Add(-time.Hour), IncludeUnattributedWatchlist: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("no events found")
	}
	sectors, err := db.EventSectors(ctx, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sectors {
		if s == "US: Industrials" {
			found = true
		}
	}
	if !found {
		t.Errorf("sectors = %v, want US: Industrials from the USTR agency table (headline carries no matching keyword)", sectors)
	}
}
