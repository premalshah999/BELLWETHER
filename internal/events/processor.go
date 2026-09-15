package events

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
)

// Store is the persistence the processor needs. Declared at the point of use.
type Store interface {
	// ListPendingRawItems returns items not yet turned into events, oldest
	// first so the event stream is built in the order the world happened.
	ListPendingRawItems(ctx context.Context, limit int) ([]news.RawItem, error)
	MarkRawItemsProcessed(ctx context.Context, ids []int64, at time.Time) error

	// ClusterCandidates returns recent events that might absorb a new item.
	// Both a symbol set and a title key are supplied because they select
	// disjoint kinds of candidate and must not compete for one result budget.
	ClusterCandidates(ctx context.Context, symbols []string, titleKey string, since time.Time, limit int) ([]Candidate, error)

	// UpsertEvent creates or updates an event by fingerprint, reporting
	// whether it was newly created.
	UpsertEvent(ctx context.Context, e news.Event, titleKey string) (id int64, created bool, err error)
	AttachEvidence(ctx context.Context, eventID, rawItemID int64, sourceID string, trust int, at time.Time) error
	UpsertEventEntities(ctx context.Context, eventID int64, entities []news.EventEntity) error
	SaveEventFacts(ctx context.Context, eventID int64, facts map[string]string) error
	SaveEventSectors(ctx context.Context, eventID int64, sectors []string) error
	// TouchEvent refreshes the aggregates that change when evidence arrives.
	TouchEvent(ctx context.Context, eventID int64, at time.Time) error
}

// Processor turns raw items into events.
//
// It is deliberately deterministic from end to end. Nothing here calls a
// model: the exchange states a filing's category, the resolver names the
// company from a master list, and clustering is token overlap. The AI layer
// runs after this and refines what this produced — which means the event
// stream still works, in full, when the model is unavailable or out of budget.
type Processor struct {
	store    Store
	master   *company.Master
	registry *news.Registry
	log      *slog.Logger
	now      func() time.Time
	loc      *time.Location
	// heat, when set, is told about every event created so the ingestion
	// scheduler can spend more effort where something is happening.
	heat AttentionSink

	// minEntityConfidence is the floor for attaching a company to an event.
	// It sits above the lead-word tier: a browsable timeline can tolerate a
	// weak guess, but an event is a claim that something happened to a named
	// company and deserves better evidence than one capitalised word.
	minEntityConfidence float64
	// onEvent, when set, receives each newly created event for live delivery.
	onEvent func(EventNotice)

	// usByCIK resolves an SEC filer's CIK to its bare US ticker. The filer
	// declares its own CIK on every filing, which is a far stronger identity
	// signal than the NSE document-path regex ever was -- there is no text
	// matching involved at all, just a lookup.
	usByCIK map[string]string
}

// AttentionSink is told when something happens to a set of instruments.
// Declared here so this package does not depend on the ingestion engine.
type AttentionSink interface {
	Observe(symbols []string, sectors []string, importance int, official bool, reason string)
}

// EventNotice is a newly created event, in the shape a live client needs.
//
// Deliberately not the full event: a stream carries what a screen shows, and
// the client can fetch the rest by id if a reader opens it. Sending everything
// would put the whole archive through the socket for events most readers
// never look at.
type EventNotice struct {
	Headline   string    `json:"headline"`
	Type       string    `json:"type"`
	Importance int       `json:"importance"`
	Official   bool      `json:"official"`
	Symbols    []string  `json:"symbols,omitempty"`
	SourceID   string    `json:"source_id"`
	At         time.Time `json:"at"`
}

// WithEventSink is called for each newly created event.
func WithEventSink(f func(EventNotice)) ProcessorOption {
	return func(p *Processor) { p.onEvent = f }
}

// WithAttentionSink connects the processor to the attention tracker.
func WithAttentionSink(s AttentionSink) ProcessorOption {
	return func(p *Processor) { p.heat = s }
}

// ProcessorOption configures a Processor.
type ProcessorOption func(*Processor)

func WithProcessorLogger(l *slog.Logger) ProcessorOption {
	return func(p *Processor) { p.log = l }
}

func WithProcessorClock(now func() time.Time) ProcessorOption {
	return func(p *Processor) { p.now = now }
}

// WithUSCIKIndex supplies the CIK -> ticker map SEC filing entity resolution
// needs. Built once from the embedded SEC ticker reference (see
// company.LoadEmbeddedUS), the same way the NSE master is built once and
// passed in rather than loaded per-processor.
func WithUSCIKIndex(byCIK map[string]string) ProcessorOption {
	return func(p *Processor) { p.usByCIK = byCIK }
}

// NewProcessor builds a processor.
func NewProcessor(store Store, master *company.Master, registry *news.Registry, opts ...ProcessorOption) *Processor {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+1800)
	}
	p := &Processor{
		store: store, master: master, registry: registry,
		log: slog.Default(), now: time.Now, loc: loc,
		minEntityConfidence: 0.85,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ProcessResult summarises one batch.
type ProcessResult struct {
	Items    int
	Created  int
	Merged   int
	Filtered int // recognised, and deliberately not made into events
	NoEntity int
	Duration time.Duration
}

// ProcessBatch converts up to limit pending raw items into events.
func (p *Processor) ProcessBatch(ctx context.Context, limit int) (ProcessResult, error) {
	start := p.now()
	res := ProcessResult{}

	items, err := p.store.ListPendingRawItems(ctx, limit)
	if err != nil {
		return res, fmt.Errorf("events: list pending: %w", err)
	}
	res.Items = len(items)
	if len(items) == 0 {
		return res, nil
	}

	var done []int64
	for _, item := range items {
		created, merged, filtered, noEntity, err := p.processOne(ctx, item)
		if err != nil {
			// One bad item must not stall the queue behind it. It is left
			// unprocessed so a later fix can pick it up, and the rest of the
			// batch proceeds.
			p.log.Warn("could not process raw item", "id", item.ID, "source", item.SourceID, "err", err)
			continue
		}
		done = append(done, item.ID)
		switch {
		case filtered:
			res.Filtered++
		case created:
			res.Created++
		case merged:
			res.Merged++
		}
		if noEntity {
			res.NoEntity++
		}
	}

	if len(done) > 0 {
		if err := p.store.MarkRawItemsProcessed(ctx, done, p.now()); err != nil {
			return res, fmt.Errorf("events: mark processed: %w", err)
		}
	}
	res.Duration = p.now().Sub(start)
	return res, nil
}

// processOne handles a single raw item.
func (p *Processor) processOne(ctx context.Context, item news.RawItem) (created, merged, filtered, noEntity bool, err error) {
	src, known := p.registry.Get(item.SourceID)
	if !known {
		// The source has been removed from the catalog since this was
		// fetched. The item stays as a record; there is nothing to classify
		// it against, so it is not made into an event.
		return false, false, true, false, nil
	}

	typ, headline, summary, facts, occurredAt := p.interpret(src, item)

	// Mutual-fund NAV declarations are 440 of a day's 1,600 NSE
	// announcements. They are real disclosures about a different asset class,
	// so they are recognised and kept as raw items, but they do not enter an
	// equity operator's event stream.
	if !typ.Equity() {
		return false, false, true, false, nil
	}

	entities := p.resolveEntities(src, item, headline, summary, facts)
	noEntity = len(entities) == 0

	// A watchlist source is exempt from both relevance gates below. The
	// operator has stated that this instrument matters to them, which
	// settles the question those gates exist to answer — including for the
	// US names a content filter would otherwise discard as unactionable.
	//
	// An official source is exempt for a different reason: it is not
	// commentary to be judged relevant, it is the primary disclosure --
	// an SEC Form 4's own filer entry (a person, not a company, and
	// unresolvable by the CIK lookup on that account) is not "unproven
	// foreign noise" the way a wire story about a company nobody here can
	// trade would be. Indian official sources (NSE, RBI) never reached this
	// gate in the first place, since src.Indian() was already true for
	// them; this exemption is what makes the same true for SEC.
	if src.Watchlist() || src.Official() {
		return p.finish(ctx, src, item, typ, headline, summary, facts, occurredAt, entities, noEntity)
	}

	// A source that is not about India has to earn its place in the feed.
	//
	// Global desks are carried because a tariff decision or an oil move
	// reaches Indian equities before the Indian market opens. What they also
	// carry is a great deal of coverage of companies nobody here can trade.
	// The test is simple and checkable: name an Indian listed company, or be
	// the kind of event whose reach is sectoral or macro. Anything else is
	// kept as evidence and left out of the stream.
	if !src.Indian() && noEntity && !typ.SectorScope() {
		return false, false, true, true, nil
	}

	// An item nobody reading this feed can read is not a candidate for the top
	// of it, whatever its subject. Applied regardless of entity resolution,
	// because the resolver cannot match a non-Latin headline in the first
	// place and so every such item arrives here entity-free by construction.
	if NotReadableHere(headline) {
		return false, false, true, true, nil
	}

	// The same test applied to content rather than origin. Indian outlets
	// republish a great deal of US market commentary, which the source-level
	// gate cannot catch because the source is Indian.
	if noEntity && Unactionable(headline, summary) {
		return false, false, true, true, nil
	}

	return p.finish(ctx, src, item, typ, headline, summary, facts, occurredAt, entities, noEntity)
}

// finish clusters an interpreted item and stores it as an event.
func (p *Processor) finish(
	ctx context.Context,
	src news.Source,
	item news.RawItem,
	typ Type,
	headline, summary string,
	facts map[string]string,
	occurredAt time.Time,
	entities []news.EventEntity,
	noEntity bool,
) (created, merged, filtered, noEnt bool, err error) {
	noEnt = noEntity

	symbols := make([]string, 0, len(entities))
	for _, e := range entities {
		symbols = append(symbols, e.Symbol)
	}
	sort.Strings(symbols)

	when := item.DiscoveredAt
	since := when.Add(-officialWindow)
	titleKey := TitleKey(headline)
	candidates, err := p.store.ClusterCandidates(ctx, symbols, titleKey, since, 200)
	if err != nil {
		return false, false, false, noEnt, fmt.Errorf("cluster candidates: %w", err)
	}

	if m := FindCluster(headline, symbols, typ, when, candidates); m.Matched {
		if err := p.store.AttachEvidence(ctx, m.EventID, item.ID, src.ID, src.Trust, p.now()); err != nil {
			return false, false, false, noEnt, fmt.Errorf("attach evidence: %w", err)
		}
		// A second source raises corroboration, and an official one can
		// confirm a story that started as a report.
		if err := p.store.TouchEvent(ctx, m.EventID, p.now()); err != nil {
			return false, false, false, noEnt, fmt.Errorf("touch event: %w", err)
		}
		if len(entities) > 0 {
			if err := p.store.UpsertEventEntities(ctx, m.EventID, entities); err != nil {
				return false, false, false, noEnt, fmt.Errorf("merge entities: %w", err)
			}
		}
		return false, true, false, noEnt, nil
	}

	importance := typ.BaselineImportance()
	ev := news.Event{
		Fingerprint:  Fingerprint(symbols, typ, titleKey, when, p.loc),
		Type:         string(typ),
		Headline:     headline,
		Summary:      summary,
		OccurredAt:   occurredAt,
		PublishedAt:  item.PublishedAt,
		DiscoveredAt: item.DiscoveredAt,
		UpdatedAt:    p.now(),
		Importance:   &importance,
		BestTrust:    src.Trust,
		SourceCount:  1,
		Official:     src.Official(),
	}
	if src.Official() {
		ev.ConfirmedAt = item.DiscoveredAt
	}

	id, isNew, err := p.store.UpsertEvent(ctx, ev, titleKey)
	if err != nil {
		return false, false, false, noEnt, fmt.Errorf("upsert event: %w", err)
	}
	if err := p.store.AttachEvidence(ctx, id, item.ID, src.ID, src.Trust, p.now()); err != nil {
		return false, false, false, noEnt, fmt.Errorf("attach evidence: %w", err)
	}
	if len(entities) > 0 {
		if err := p.store.UpsertEventEntities(ctx, id, entities); err != nil {
			return false, false, false, noEnt, fmt.Errorf("save entities: %w", err)
		}
	}
	if len(facts) > 0 {
		if err := p.store.SaveEventFacts(ctx, id, facts); err != nil {
			return false, false, false, noEnt, fmt.Errorf("save facts: %w", err)
		}
	}
	// A policy or macro event reaches companies it never names, and it does
	// so through their industry. Only types whose reach is genuinely sectoral
	// are expanded; a company's own order win stays attached to that company
	// however the headline is worded.
	if typ.SectorScope() {
		if sectors := InferSectors(headline + " " + summary); len(sectors) > 0 {
			if err := p.store.SaveEventSectors(ctx, id, sectors); err != nil {
				return false, false, false, noEnt, fmt.Errorf("save sectors: %w", err)
			}
		}
	}
	if !isNew {
		// The fingerprint already existed, so this is the same event reaching
		// us again rather than a new one.
		return false, true, false, noEnt, nil
	}

	// Tell the scheduler that something happened here. This is what turns a
	// filing into faster polling of that company's coverage for the next
	// hour, rather than the fixed cadence a cron job would keep.
	if p.heat != nil && len(symbols) > 0 {
		p.heat.Observe(symbols, sectorsFor(typ, headline, summary), importance, src.Official(), string(typ))
	}

	// Push the event to anything watching live.
	//
	// Only genuinely new events, and only after the fingerprint check above,
	// so a story reaching us from six outlets streams once rather than six
	// times. A stream whose messages are mostly duplicates teaches its
	// readers to ignore it.
	if p.onEvent != nil {
		p.onEvent(EventNotice{
			Headline:   headline,
			Type:       string(typ),
			Importance: importance,
			Official:   src.Official(),
			Symbols:    symbols,
			SourceID:   src.ID,
			At:         p.now(),
		})
	}
	return true, false, false, noEnt, nil
}

// sectorsFor returns the industries an event reaches, for attention purposes.
func sectorsFor(typ Type, headline, summary string) []string {
	if !typ.SectorScope() {
		return nil
	}
	return InferSectors(headline + " " + summary)
}

// interpret reads an item according to what kind of source it came from.
func (p *Processor) interpret(src news.Source, item news.RawItem) (typ Type, headline, summary string, facts map[string]string, occurredAt time.Time) {
	if src.Method == news.MethodSECFiling {
		f := ParseSECFiling(item.Title, item.Description)
		headline = buildSECHeadline(f)
		facts = map[string]string{}
		// Only the company-side entry carries a CIK worth resolving against
		// -- a Form 4's "Reporting" entry names the filing person, and
		// looking that up in a company index would find nothing at best and
		// a coincidentally-numbered company at worst.
		if f.CIK != "" && (f.Role == "Issuer" || f.Role == "Filer") {
			facts["SEC_CIK"] = f.CIK
		}
		if f.AccNo != "" {
			facts["SEC_ACCESSION"] = f.AccNo
		}
		if f.FormType != "" {
			facts["SEC_FORM_TYPE"] = f.FormType
		}
		if len(f.ItemCodes) > 0 {
			facts["SEC_ITEMS"] = strings.Join(f.ItemCodes, ",")
		}
		return f.Type, headline, headline, facts, f.FiledDate
	}
	if src.Method == news.MethodNSEAnnounce {
		f := ParseFiling(src.ID, item.Title, item.Description, item.URL, p.loc)
		headline = buildFilingHeadline(f)
		facts = map[string]string{}
		for k, v := range f.Facts {
			facts[k] = v
		}
		if f.Symbol != "" {
			facts["NSE_SYMBOL_PATH"] = f.Symbol
		}
		if f.Subject != "" {
			facts["NSE_SUBJECT"] = f.Subject
		}
		return f.Type, headline, f.Summary, facts, f.OccurredAt
	}

	// Regulators and ministries are official but are not company filings.
	// What they publish is policy, and policy reaches companies through
	// sectors rather than by name.
	if src.Official() {
		return classifyPolicy(src, item.Title), item.Title, item.Description, nil, time.Time{}
	}

	// News from ordinary publishers gets the keyword pass. Without it these
	// would sit as UNCLASSIFIED until the hourly classifier reached them,
	// which for a system whose whole point is timing means the event exists
	// but says nothing useful for up to an hour.
	fast, matched := ClassifyHeadline(item.Title, item.Description)
	if !matched {
		return TypeUnclassified, item.Title, item.Description, nil, time.Time{}
	}
	facts = map[string]string{"FAST_RULE_MATCH": fast.Matched}
	return fast.Type, item.Title, item.Description, facts, time.Time{}
}

// buildFilingHeadline composes a readable headline from a filing.
//
// NSE's title field is the company's legal name and its description is the
// substance, so neither alone reads as a headline. Joined and condensed, they
// do.
func buildFilingHeadline(f Filing) string {
	company := strings.TrimSpace(f.Company)
	summary := condenseHeadline(f.Summary)
	switch {
	case company == "" && summary == "":
		return "Exchange filing"
	case company == "":
		return summary
	case summary == "":
		return company
	}
	// Filings frequently name the company inside the description, and the
	// exchange's surveillance notices name it in the middle of the sentence
	// rather than at the start ("Significant movement in price has been
	// observed in X"). Prefixing the company again produces "X: … observed in
	// X", so the prefix is added only when the sentence does not already
	// carry the name.
	if strings.Contains(strings.ToLower(summary), strings.ToLower(company)) {
		return summary
	}
	return company + ": " + summary
}

// maxHeadlineChars is where a headline stops being a headline.
const maxHeadlineChars = 110

// condenseHeadline reduces a filing's text to something that reads as a title.
//
// Most NSE descriptions are a phrase and need no work. The exceptions are the
// exchange's surveillance notices, whose entire substance is a single
// 300-character sentence of statutory boilerplate: "Significant movement in
// price has been observed in X. The Exchange, in order to ensure that
// investors have latest relevant information about the company and to inform
// the market place so that the interest of the investors is safeguarded, has
// written to the company. The response from the company is awaited."
//
// Used whole, that is not a headline — it is a paragraph, and a feed of them
// is unreadable. The first sentence carries the news; the rest is the same
// sentence on every such notice, and it stays available in the summary and in
// the evidence.
func condenseHeadline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxHeadlineChars {
		return s
	}
	// Prefer a sentence boundary, when one falls somewhere useful.
	if i := strings.Index(s, ". "); i > 24 && i <= maxHeadlineChars {
		return s[:i]
	}
	// Otherwise cut on a word boundary rather than mid-word.
	cut := s[:maxHeadlineChars]
	if i := strings.LastIndexByte(cut, ' '); i > 40 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}

// classifyPolicy types a regulator or ministry publication.
func classifyPolicy(src news.Source, title string) Type {
	t := strings.ToLower(title)
	switch {
	case strings.Contains(t, "monetary policy") || strings.Contains(t, "repo rate") ||
		strings.Contains(t, "inflation") || strings.Contains(t, "gdp"):
		return TypeMacroEvent
	case strings.Contains(t, "circular") || strings.Contains(t, "regulation") ||
		strings.Contains(t, "amendment") || strings.Contains(t, "directions"):
		return TypeRegulatoryPolicy
	case strings.Contains(t, "penalty") || strings.Contains(t, "order against") ||
		strings.Contains(t, "bars ") || strings.Contains(t, "debars"):
		return TypeRegulatoryAction
	}
	switch src.Category {
	case "regulatory":
		return TypeRegulatoryPolicy
	case "government":
		return TypeMacroEvent
	}
	return TypeUnclassified
}

// resolveEntities decides which companies an item concerns.
//
// The strongest evidence available is used and weaker evidence is not stacked
// on top of it. An NSE filing whose document path names a symbol has told us
// the company outright, and running a text resolver over the headline as well
// would only add opportunities to be wrong.
// nseSymbol renders a bare NSE ticker (what company.Master resolves text
// against) as the canonical, venue-qualified symbol everything downstream of
// entity resolution is stored under. p.master is always the NSE master here
// -- there is no US text resolver wired into this pipeline yet -- so every
// resolution this function produces is unambiguously NSE.
func nseSymbol(ticker string) string {
	return marketdata.Symbol{Ticker: ticker, Exchange: marketdata.ExchangeNSE}.String()
}

func (p *Processor) resolveEntities(src news.Source, item news.RawItem, headline, summary string, facts map[string]string) []news.EventEntity {
	if p.master == nil {
		return nil
	}

	// Path 0: the query was scoped to one company.
	//
	// A watchlist source searches for a named instrument, so every result is
	// about it by construction. Resolving from the headline instead threw
	// that away and discarded most of what these sources returned — a Reuters
	// piece headlined "Ambani weighs aluminium entry" is about Reliance, and
	// no amount of text matching will say so.
	//
	// Text resolution still runs, because an article can concern several
	// companies and the others are worth having. This only guarantees the one
	// that was asked for.
	if len(src.Symbols) == 1 && src.Watchlist() {
		want := strings.ToUpper(src.Symbols[0])
		if c, listed := p.master.Lookup(want); listed {
			out := []news.EventEntity{{
				Symbol: nseSymbol(c.Symbol), Relationship: news.RelPrimary,
				// High, but below a filing naming itself: a scoped search
				// does return the occasional unrelated result.
				MatchConfidence: 0.9, MatchMethod: "watchlist_query",
			}}
			text := headline
			if summary != "" && summary != headline {
				text = headline + ". " + summary
			}
			for _, m := range p.master.ResolveAbove(text, p.minEntityConfidence) {
				if m.Symbol == c.Symbol {
					// The text confirms it; upgrade to what the text says.
					out[0].MatchConfidence = m.Confidence
					out[0].MatchMethod = string(m.Method)
					continue
				}
				out = append(out, news.EventEntity{
					Symbol: nseSymbol(m.Symbol), Relationship: news.RelMentioned,
					MatchConfidence: m.Confidence, MatchMethod: string(m.Method),
				})
			}
			return out
		}
	}

	// Path 1: the filing names its own symbol.
	if sym, ok := facts["NSE_SYMBOL_PATH"]; ok {
		if c, listed := p.master.Lookup(sym); listed {
			return []news.EventEntity{{
				Symbol: nseSymbol(c.Symbol), Relationship: news.RelPrimary,
				MatchConfidence: 0.99, MatchMethod: "nse_document_path",
			}}
		}
		// The path named a symbol the master does not list. That happens
		// after a rename — a filing arrived under IHFL when the listing had
		// become SAMMAANCAP — so the symbol is not trusted and the company
		// name is resolved instead.
		p.log.Debug("filing path names an unlisted symbol", "symbol", sym, "source", src.ID)
	}

	// Path 1b: an SEC filing names its own CIK. Stronger even than NSE's
	// document-path symbol, because there is no text pattern involved at
	// all -- the filer declares an identifier under penalty of the
	// securities laws, and it either resolves or it does not.
	if cik, ok := facts["SEC_CIK"]; ok && p.usByCIK != nil {
		if ticker, listed := p.usByCIK[cik]; listed {
			return []news.EventEntity{{
				Symbol: ticker, Relationship: news.RelPrimary,
				MatchConfidence: 0.99, MatchMethod: "sec_cik",
			}}
		}
		p.log.Debug("sec filing names an unrecognized cik", "cik", cik, "source", src.ID)
	}

	// Path 2: resolve from the text. For a filing that is the exchange's own
	// rendering of the legal name, which is the resolver's best case.
	text := headline
	if summary != "" && summary != headline {
		text = headline + ". " + summary
	}
	matches := p.master.ResolveAbove(text, p.minEntityConfidence)
	if len(matches) == 0 {
		return nil
	}

	out := make([]news.EventEntity, 0, len(matches))
	for i, m := range matches {
		rel := news.RelMentioned
		// The highest-confidence match in a filing is the filer. In a news
		// headline the first company named is usually the subject, but that
		// is a weaker claim, so only official sources get RelPrimary here.
		if i == 0 && (src.Official() || m.Confidence >= 0.95) {
			rel = news.RelPrimary
		}
		out = append(out, news.EventEntity{
			Symbol: nseSymbol(m.Symbol), Relationship: rel,
			MatchConfidence: m.Confidence, MatchMethod: string(m.Method),
		})
	}
	return out
}

// numericFact parses a fact value, reporting whether it is a number at all.
func numericFact(v string) (float64, bool) {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "%"))
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
