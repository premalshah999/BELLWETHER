package ai

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/jev"
	"github.com/tradesys/dashboard/internal/news"
)

// Confidence gates for Jev's answers.
//
// TypeSafe's guidance is to gate each action by what getting it wrong costs,
// not to use one threshold for everything, and these follow that:
//
//   - typeConfidenceFloor: below this the deterministic type stands. The
//     keyword rules already assigned one, so declining to override costs
//     little, and a confident-sounding wrong label costs a lot.
//   - directionConfidenceFloor: below this the direction is "unclear", which
//     is what the prompt this replaces asked the model to say rather than
//     flip a coin. Jev's calibrated confidence makes that rule mechanical.
//   - notAnEventCeiling: a truth value below this means Jev is at least 80%
//     sure the item is not a market event -- commentary, a quote page, a
//     data listing. Those get importance zero, which takes them out of the
//     default feed without deleting anything.
const (
	typeConfidenceFloor      = 0.5
	directionConfidenceFloor = 0.5
	notAnEventCeiling        = 0.2
	// maxEntityQuestions bounds the per-company questions on one event. A
	// sector event can name dozens of companies; the ones that matter are
	// the primaries, and a request with forty questions costs forty times
	// the input for little more information.
	maxEntityQuestions = 5
	jevConcurrency     = 8
)

// WithJev routes decisions to Jev. With a configured client, event
// classification goes to Jev instead of the text model.
func WithJev(c *jev.Client) ServiceOption { return func(s *Service) { s.jev = c } }

// JevConfigured reports whether decisions are being made by Jev.
func (s *Service) JevConfigured() bool { return s.jev != nil && s.jev.Configured() }

// factStore is the optional ability to record facts, used to keep Jev's
// admission verdict on the event so a hidden item can be explained later.
type factStore interface {
	SaveEventFacts(ctx context.Context, eventID int64, facts map[string]string) error
}

// classifyEventsWithJev is the Jev path of ClassifyEvents.
//
// One request per event, rather than the text model's batches of six: Jev
// evaluates every question against a single state, and it answers in well
// under a second, so batching buys nothing and one event per request keeps a
// failure to that one event.
func (s *Service) classifyEventsWithJev(ctx context.Context, store EventStore, pending []news.Event) (int, error) {
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		classified int
		failures   int
		slots      = make(chan struct{}, jevConcurrency)
	)
	for _, ev := range pending {
		select {
		case <-ctx.Done():
			wg.Wait()
			return classified, nil
		case slots <- struct{}{}:
		}
		wg.Add(1)
		go func(ev news.Event) {
			defer wg.Done()
			defer func() { <-slots }()
			if err := s.classifyOneWithJev(ctx, store, ev); err != nil {
				s.log.Warn("jev classification failed", "event", ev.ID, "err", err)
				mu.Lock()
				failures++
				mu.Unlock()
				return
			}
			mu.Lock()
			classified++
			mu.Unlock()
		}(ev)
	}
	wg.Wait()
	if classified == 0 && failures > 0 {
		return 0, fmt.Errorf("jev: every event in the batch failed (%d)", failures)
	}
	return classified, nil
}

func (s *Service) classifyOneWithJev(ctx context.Context, store EventStore, ev news.Event) error {
	facts, _ := store.EventFacts(ctx, ev.ID)

	questions := map[string]jev.Question{
		"event_type": jev.Choice{
			Instructions: "Which kind of market event does this report? Judge by what happened, not by the words used.",
			Criteria:     typeChoiceCriteria(),
		},
		"importance": jev.Score{
			Instructions: "How much does this matter to someone who owns or trades the company's stock? " +
				"Judge it against the size of the company: a $50 million contract transforms a small company and is routine for a large one.",
			Criteria: importanceLevels,
		},
		"is_event": jev.Noul{
			Instructions: "This reports a specific thing that happened or was decided at a company, " +
				"a regulator or in the economy. It is not market commentary, an opinion piece, a price " +
				"quote or chart page, a list of stock prices, or a data table.",
		},
	}

	entities := primaryEntities(ev.Entities, maxEntityQuestions)
	for _, e := range entities {
		questions[directionKey(e.Symbol)] = jev.Choice{
			Instructions: "For " + e.Symbol + " specifically, is this good or bad news? The same event can be good " +
				"for one company and bad for another. Choose unclear when it genuinely could go either way.",
			Criteria: map[string]string{
				string(news.DirectionPositive): "Likely to help " + e.Symbol + "'s business or share price.",
				string(news.DirectionNegative): "Likely to hurt " + e.Symbol + "'s business or share price.",
				string(news.DirectionUnclear):  "Direction for " + e.Symbol + " cannot be told from this.",
			},
		}
		questions[impactKey(e.Symbol)] = jev.Score{
			Instructions: "How much does this move the needle for " + e.Symbol + " specifically?",
			Criteria: []string{
				"A passing mention; barely relevant to " + e.Symbol + ".",
				"Relevant but minor for " + e.Symbol + ".",
				"Material for " + e.Symbol + ".",
				"Company-defining for " + e.Symbol + ".",
			},
		}
	}

	resp, err := s.jev.Ask(ctx, jev.Request{
		Feature:   FeatureEventClassify,
		State:     eventState(ev, facts),
		Questions: questions,
	})
	if err != nil {
		return err
	}

	c := events.Classification{
		EventID:      ev.ID,
		Model:        resp.Model,
		ClassifiedAt: s.now().UTC(),
	}

	// Type: only overridden when Jev is sure. An empty type leaves the
	// deterministic one in place -- SaveEventClassification COALESCEs it.
	if a, ok := resp.Choice("event_type"); ok {
		conf := a.Confidence
		c.Confidence = &conf
		if a.Confidence >= typeConfidenceFloor && events.KnownType(events.Type(a.Choice)) {
			c.Type = a.Choice
		}
	}

	// Importance from the whole distribution, not the single pick.
	if a, ok := resp.Score("importance"); ok {
		imp := int(a.Expected(importanceValues) + 0.5)
		c.Importance = &imp
	}

	// Admission. Jev being sure this is not an event overrides the importance
	// it gave above, because a quote page scored "worth reading" is still a
	// quote page.
	var isEvent float64 = -1
	if a, ok := resp.Noul("is_event"); ok {
		isEvent = a.Noul
		if a.Noul < notAnEventCeiling {
			zero := 0
			c.Importance = &zero
		}
	}

	for _, e := range entities {
		reading := events.EntityReading{
			Symbol:         e.Symbol,
			Relationship:   e.Relationship,
			Direction:      news.DirectionUnclear,
			ImpactStrength: 0,
		}
		if a, ok := resp.Choice(directionKey(e.Symbol)); ok && a.Confidence >= directionConfidenceFloor {
			reading.Direction = news.Direction(a.Choice)
		}
		if a, ok := resp.Score(impactKey(e.Symbol)); ok {
			reading.ImpactStrength = a.Expected([]float64{0.1, 0.35, 0.65, 0.95})
		}
		c.Entities = append(c.Entities, reading)
	}

	if err := store.SaveEventClassification(ctx, c); err != nil {
		return fmt.Errorf("save classification: %w", err)
	}
	if fs, ok := store.(factStore); ok && isEvent >= 0 {
		// Kept so a hidden item can be explained: the feed drops it on
		// importance, and this says why.
		_ = fs.SaveEventFacts(ctx, ev.ID, map[string]string{
			"JEV_IS_EVENT": fmt.Sprintf("%.2f", isEvent),
			"JEV_MODEL":    resp.Model,
		})
	}
	return nil
}

// eventState renders an event as the text Jev evaluates.
func eventState(ev news.Event, facts map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Headline: %s\n", strings.TrimSpace(ev.Headline))
	if s := strings.TrimSpace(ev.Summary); s != "" && s != strings.TrimSpace(ev.Headline) {
		fmt.Fprintf(&b, "Detail: %s\n", s)
	}
	if ev.Type != "" && ev.Type != string(events.TypeUnclassified) {
		fmt.Fprintf(&b, "Type assigned by keyword rules: %s\n", ev.Type)
	}
	if ev.Official {
		b.WriteString("Source: an official filing or regulator release.\n")
	}
	fmt.Fprintf(&b, "Independent sources reporting it: %d\n", ev.SourceCount)
	if len(ev.Entities) > 0 {
		var names []string
		for _, e := range ev.Entities {
			names = append(names, fmt.Sprintf("%s (%s)", e.Symbol, e.Relationship))
		}
		fmt.Fprintf(&b, "Companies: %s\n", strings.Join(names, ", "))
	}
	if len(facts) > 0 {
		keys := make([]string, 0, len(facts))
		for k := range facts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("Facts:\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, facts[k])
		}
	}
	return b.String()
}

// primaryEntities picks the companies worth a per-company question: primaries
// first, then by how confidently they were matched.
func primaryEntities(all []news.EventEntity, limit int) []news.EventEntity {
	out := append([]news.EventEntity(nil), all...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Relationship == news.RelPrimary, out[j].Relationship == news.RelPrimary
		if pi != pj {
			return pi
		}
		return out[i].MatchConfidence > out[j].MatchConfidence
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// typeChoiceCriteria is the choice question's options, built once.
var typeChoiceCriteria = sync.OnceValue(func() map[string]string {
	out := make(map[string]string, len(typeCriteria))
	for t, desc := range typeCriteria {
		out[string(t)] = desc
	}
	return out
})

func directionKey(symbol string) string { return "direction_" + questionSafe(symbol) }
func impactKey(symbol string) string    { return "impact_" + questionSafe(symbol) }

// questionSafe makes a symbol usable as a question name. BRK-B and a
// venue-qualified symbol both carry characters a key should not.
func questionSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, s)
}
