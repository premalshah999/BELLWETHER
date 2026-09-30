package events

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
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

// NewProcessor builds a processor.
func NewProcessor(store Store, master *company.Master, registry *news.Registry, opts ...ProcessorOption) *Processor {
	p := &Processor{
		store: store, master: master, registry: registry,
		// Fingerprints bucket events by the market's calendar day.
		log: slog.Default(), now: time.Now, loc: marketdata.Market,
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
	if src.Method != news.MethodSECFiling {
		headline = StripPublisher(headline, item.Publisher)
	}

	// Disclosures about other asset classes are kept as raw items but do not
	// enter an equity event stream.
	if !typ.Equity() {
		return false, false, true, false, nil
	}

	entities := p.resolveEntities(src, item, headline, summary, facts)
	noEntity = len(entities) == 0

	// A watchlist source is exempt from the relevance gates below: the
	// operator has said this instrument matters. So is an official source,
	// which is the primary disclosure rather than commentary to be judged.
	if src.Watchlist() || src.Official() {
		return p.finish(ctx, src, item, typ, headline, summary, facts, occurredAt, entities, noEntity)
	}

	// Every other source earns its place on the same terms: name a listed
	// company, or be an event whose reach is sectoral or macro. Anything
	// else is kept as evidence and left out of the stream.
	if noEntity && !typ.SectorScope() {
		return false, false, true, true, nil
	}

	// An item nobody reading this feed can read is not a candidate for the top
	// of it, whatever its subject. Applied regardless of entity resolution,
	// because the resolver cannot match a non-Latin headline in the first
	// place and so every such item arrives here entity-free by construction.
	if NotReadableHere(headline) {
		return false, false, true, true, nil
	}

	// Market commentary that names no company is not an event either.
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
		if sectors := sectorsForEvent(headline, summary, facts); len(sectors) > 0 {
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
		p.heat.Observe(symbols, sectorsFor(typ, headline, summary, facts), importance, src.Official(), string(typ))
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
func sectorsFor(typ Type, headline, summary string, facts map[string]string) []string {
	if !typ.SectorScope() {
		return nil
	}
	return sectorsForEvent(headline, summary, facts)
}

// sectorsForEvent decides which sectors an event reaches. A Federal Register
// item that resolved to a known issuing agency uses that structured signal
// directly -- a USTR notice is a trade action regardless of whether the word
// "tariff" happens to appear in this particular one's title, which keyword
// inference cannot know. Everything else falls back to InferSectors over the
// headline and summary.
func sectorsForEvent(headline, summary string, facts map[string]string) []string {
	if agency := facts["FR_AGENCY"]; agency != "" {
		if sectors, ok := SectorsForAgency(agency); ok {
			return sectors
		}
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
	if src.Method == news.MethodFederalRegister {
		pf := parsePipeFacts(item.Description)
		facts = map[string]string{}
		if agency := pf["AGENCY"]; agency != "" {
			facts["FR_AGENCY"] = agency
		}
		if doctype := pf["DOCTYPE"]; doctype != "" {
			facts["FR_DOCTYPE"] = doctype
		}
		// The abstract is everything before the first "|KEY: VALUE" marker.
		summary := item.Description
		if i := strings.IndexByte(summary, '|'); i >= 0 {
			summary = strings.TrimSpace(summary[:i])
		}
		return classifyPolicy(src, item.Title), item.Title, summary, facts, time.Time{}
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

// classifyPolicy types a regulator or ministry publication.
func classifyPolicy(src news.Source, title string) Type {
	t := strings.ToLower(title)
	switch {
	case containsAny(t, "monetary policy", "federal funds", "interest rate", "inflation", "gdp", "employment situation"):
		return TypeMacroEvent
	case containsAny(t, "final rule", "proposed rule", "regulation", "amendment", "guidance"):
		return TypeRegulatoryPolicy
	case containsAny(t, "penalty", "charges", "settles", "order against", "bars ", "debars"):
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

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// resolveEntities decides which companies an item concerns, from the
// strongest evidence available: a scoped search, then a filer's own CIK, then
// the text. Weaker evidence is not stacked on a stronger answer.
func (p *Processor) resolveEntities(src news.Source, item news.RawItem, headline, summary string, facts map[string]string) []news.EventEntity {
	if p.master == nil {
		return nil
	}
	text := headline
	if summary != "" && summary != headline {
		text = headline + ". " + summary
	}

	// A watchlist source searched for one company, so every result is about
	// it by construction, even when the headline never names it. The text
	// still runs, for the other companies an article concerns.
	if len(src.Symbols) == 1 && src.Watchlist() {
		if c, listed := p.master.Lookup(src.Symbols[0]); listed {
			out := []news.EventEntity{{
				Symbol: c.Symbol, Relationship: news.RelPrimary,
				// High, but a scoped search returns the odd unrelated result.
				MatchConfidence: 0.9, MatchMethod: "watchlist_query",
			}}
			for _, m := range p.master.ResolveAbove(text, p.minEntityConfidence) {
				if m.Symbol == c.Symbol {
					out[0].MatchConfidence, out[0].MatchMethod = m.Confidence, string(m.Method)
					continue
				}
				out = append(out, news.EventEntity{
					Symbol: m.Symbol, Relationship: news.RelMentioned,
					MatchConfidence: m.Confidence, MatchMethod: string(m.Method),
				})
			}
			return out
		}
	}

	// An SEC filing declares its filer's CIK under penalty of the securities
	// laws: no text matching involved.
	if cik, ok := facts["SEC_CIK"]; ok {
		if ticker, listed := p.master.ByCIK(cik); listed {
			return []news.EventEntity{{
				Symbol: ticker, Relationship: news.RelPrimary,
				MatchConfidence: 0.99, MatchMethod: "sec_cik",
			}}
		}
		p.log.Debug("sec filing names an unrecognized cik", "cik", cik, "source", src.ID)
	}

	matches := p.master.ResolveAbove(text, p.minEntityConfidence)
	out := make([]news.EventEntity, 0, len(matches))
	for i, m := range matches {
		// The strongest match is the subject when an official source filed it
		// or the name matched near-certainly; otherwise it is only mentioned.
		rel := news.RelMentioned
		if i == 0 && (src.Official() || m.Confidence >= 0.95) {
			rel = news.RelPrimary
		}
		out = append(out, news.EventEntity{
			Symbol: m.Symbol, Relationship: rel,
			MatchConfidence: m.Confidence, MatchMethod: string(m.Method),
		})
	}
	return out
}
