package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/news"
)

// EventStore is what the classifier needs from storage.
type EventStore interface {
	ListUnclassifiedEvents(ctx context.Context, limit int) ([]news.Event, error)
	EventFacts(ctx context.Context, eventID int64) (map[string]string, error)
	SaveEventClassification(ctx context.Context, c events.Classification) error
	// ResolveTicker reports the single venue a bare ticker belongs to, or
	// ok=false if it names nothing or names something on both venues (ABB,
	// INFY) -- a model-supplied entity has no other signal to break that tie
	// with, so it is dropped rather than guessed.
	ResolveTicker(ctx context.Context, ticker string) (venue string, ok bool, err error)
}

// classifyBatchSize is how many events go into one request.
//
// Batching is the cost strategy: the marginal cost of an extra event is its
// headline, while the prompt explaining the task is paid once. The ceiling is
// the *response*, not the prompt. Each classified event returns a summary, a
// why-it-matters line and an entity list, so twelve events at once overran the
// completion budget and came back cut in half — which parses as garbage and
// looks like a model failure rather than a sizing mistake.
//
// Six events against a much larger completion cap leaves real headroom, and
// the model here is a reasoning model whose thinking also draws on that cap.
const classifyBatchSize = 6

// ClassifyEvents runs the model over unclassified events.
//
// Events arrive most-important-first from storage, so a limited token budget
// is spent on what matters rather than on whatever happened to be newest. The
// deterministic pipeline has already typed and entity-resolved everything, so
// a failure here degrades the feed rather than emptying it.
func (s *Service) ClassifyEvents(ctx context.Context, limit int) (int, error) {
	if s.client == nil {
		return 0, ErrNotConfigured
	}
	store, ok := s.store.(EventStore)
	if !ok {
		return 0, fmt.Errorf("ai: the configured store cannot classify events")
	}
	if limit <= 0 {
		limit = 60
	}

	pending, err := store.ListUnclassifiedEvents(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("ai: list unclassified events: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	// Batches run concurrently, because they are independent and the wall
	// time is otherwise dominated by waiting.
	//
	// Serially this classified roughly five events a minute, which against a
	// backlog of several thousand is a week of real time — and the events
	// most worth classifying would still be waiting at the end of it. The
	// concurrency is bounded rather than unbounded: a burst of forty
	// simultaneous requests would be rate-limited by the provider and would
	// exhaust the token budget in one go.
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		classified int
		slots      = make(chan struct{}, classifyConcurrency)
	)

	for start := 0; start < len(pending); start += classifyBatchSize {
		end := start + classifyBatchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]

		select {
		case <-ctx.Done():
			// The caller's deadline has passed. What finished is saved;
			// the rest stays pending for the next run.
			wg.Wait()
			return classified, nil
		case slots <- struct{}{}:
		}

		wg.Add(1)
		go func(batch []news.Event) {
			defer wg.Done()
			defer func() { <-slots }()

			n, err := s.classifyBatch(ctx, store, batch)
			if err != nil {
				// One failed batch must not abandon the ones already done,
				// nor the ones after it. Those events stay unclassified and
				// are picked up on the next run.
				s.log.Warn("event classification batch failed", "events", len(batch), "err", err)
				return
			}
			mu.Lock()
			classified += n
			mu.Unlock()
		}(batch)
	}
	wg.Wait()
	return classified, nil
}

// classifyConcurrency bounds simultaneous requests to the model.
//
// Three is chosen against the provider's tolerance rather than the machine's:
// the work is entirely waiting, so more goroutines cost nothing locally, but
// more concurrent requests risk rate limiting and spend the monthly token
// budget faster than an operator can notice.
const classifyConcurrency = 3

// classifyEventView is one event as the prompt sees it.
type classifyEventView struct {
	ID          string
	Type        string
	Importance  string
	Headline    string
	Summary     string
	Facts       string
	Companies   string
	SourceCount int
	Official    bool
}

func (s *Service) classifyBatch(ctx context.Context, store EventStore, batch []news.Event) (int, error) {
	views := make([]classifyEventView, 0, len(batch))
	byID := map[string]news.Event{}

	for _, e := range batch {
		id := strconv.FormatInt(e.ID, 10)
		byID[id] = e

		var companies []string
		for _, ent := range e.Entities {
			companies = append(companies, ent.Symbol)
		}
		importance := "unset"
		if e.Importance != nil {
			importance = strconv.Itoa(*e.Importance)
		}

		// Facts are the numbers the exchange stated. Handing them to the
		// model is what lets it judge a dividend against a share price, and
		// it costs nothing to include because they were extracted already.
		factStr := ""
		if facts, err := store.EventFacts(ctx, e.ID); err == nil && len(facts) > 0 {
			factStr = formatFacts(facts)
		}

		views = append(views, classifyEventView{
			ID: id, Type: e.Type, Importance: importance,
			Headline: e.Headline, Summary: truncateWords(e.Summary, 60),
			Facts: factStr, Companies: strings.Join(companies, ", "),
			SourceCount: e.SourceCount, Official: e.Official,
		})
	}

	prompt, err := UserPrompt(PromptEventClassify, map[string]any{
		"Events": views,
		"Types":  strings.Join(events.AllTypeNames(), ", "),
	})
	if err != nil {
		return 0, err
	}

	var out struct {
		Events []struct {
			ID           string   `json:"id"`
			EventType    string   `json:"event_type"`
			Importance   *int     `json:"importance"`
			Confidence   *float64 `json:"confidence"`
			Summary      string   `json:"summary"`
			WhyItMatters string   `json:"why_it_matters"`
			Entities     []struct {
				Symbol         string   `json:"symbol"`
				Relationship   string   `json:"relationship"`
				Direction      string   `json:"direction"`
				ImpactStrength *float64 `json:"impact_strength"`
			} `json:"entities"`
		} `json:"events"`
	}

	resp, err := s.client.CompleteJSON(ctx, Request{
		Feature: FeatureEventClassify,
		// Triage across many events, not deep analysis of one: the cheap
		// tier is the right place for it, and the deterministic pipeline
		// has already done the work that must not be got wrong.
		Cheap:       true,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.1,
		// Generous on purpose. Six events of structured output is perhaps
		// 1,200 tokens; the rest is headroom for a reasoning model that
		// spends part of its budget before emitting anything at all.
		MaxTokens: 6000,
	}, &out)
	if err != nil {
		return 0, err
	}

	saved := 0
	for _, r := range out.Events {
		src, ok := byID[r.ID]
		if !ok {
			// The model returned an event we did not ask about. Ignoring it
			// is the only safe response: writing it would attach a
			// classification to an arbitrary row.
			s.log.Debug("classifier returned an unknown event id", "id", r.ID)
			continue
		}
		c := buildClassification(src, r.EventType, r.Importance, r.Confidence,
			r.Summary, r.WhyItMatters, resp.Model, s.now())
		for _, ent := range r.Entities {
			ticker := strings.ToUpper(strings.TrimSpace(ent.Symbol))
			if ticker == "" {
				continue
			}
			// A model-supplied symbol is a guess, not a citation, so it is
			// only trusted when the listed universe confirms it -- and
			// confirms it unambiguously. Storing it bare would either
			// silently reintroduce the pre-migration namespace collision or
			// (for a name on neither venue) attach the classification to an
			// instrument that does not exist.
			venue, ok, rerr := store.ResolveTicker(ctx, ticker)
			if rerr != nil {
				s.log.Warn("could not resolve model-supplied ticker", "ticker", ticker, "err", rerr)
				continue
			}
			if !ok {
				s.log.Debug("dropped unresolvable or ambiguous model-supplied ticker",
					"ticker", ticker, "event", src.ID)
				continue
			}
			sym := ticker
			if venue != "US" {
				sym = ticker + "." + venue
			}
			c.Entities = append(c.Entities, events.EntityReading{
				Symbol:         sym,
				Relationship:   normalizeRelationship(ent.Relationship),
				Direction:      normalizeDirection(ent.Direction),
				ImpactStrength: clamp01(ent.ImpactStrength),
			})
		}
		if err := store.SaveEventClassification(ctx, c); err != nil {
			s.log.Warn("could not save event classification", "event", src.ID, "err", err)
			continue
		}
		saved++
	}
	return saved, nil
}

// buildClassification assembles the stored result, keeping the deterministic
// values wherever the model declined to improve on them.
func buildClassification(src news.Event, typ string, importance *int, confidence *float64,
	summary, why, model string, at time.Time) events.Classification {

	c := events.Classification{
		EventID:      src.ID,
		Type:         src.Type,
		Summary:      strings.TrimSpace(summary),
		WhyItMatters: strings.TrimSpace(why),
		Model:        model,
		ClassifiedAt: at,
	}

	// A type is only overridden when the model names a type we recognise.
	// An exchange stated the original, so a hallucinated category must not
	// be able to overwrite it.
	if t := strings.ToUpper(strings.TrimSpace(typ)); t != "" && events.KnownType(events.Type(t)) {
		c.Type = t
	}
	if importance != nil {
		v := *importance
		if v < 0 {
			v = 0
		}
		if v > 10 {
			v = 10
		}
		c.Importance = &v
	} else if src.Importance != nil {
		c.Importance = src.Importance
	}
	if confidence != nil {
		v := *confidence
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		c.Confidence = &v
	}
	return c
}

// normalizeDirection maps the model's answer onto the enumeration, defaulting
// to "unclear".
//
// Defaulting to unclear rather than to neutral-positive matters: an unparseable
// direction means we do not know, and recording that honestly is the whole
// reason the enumeration has three values instead of being a signed number.
func normalizeDirection(s string) news.Direction {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "positive", "bullish", "up":
		return news.DirectionPositive
	case "negative", "bearish", "down":
		return news.DirectionNegative
	default:
		return news.DirectionUnclear
	}
}

func normalizeRelationship(s string) news.Relationship {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "primary", "subject":
		return news.RelPrimary
	case "peer", "competitor":
		return news.RelPeer
	case "sector", "industry":
		return news.RelSector
	default:
		return news.RelMentioned
	}
}

func clamp01(p *float64) float64 {
	if p == nil {
		return 0
	}
	switch {
	case *p < 0:
		return 0
	case *p > 1:
		return 1
	default:
		return *p
	}
}

// formatFacts renders extracted facts compactly and deterministically.
//
// Sorting is not cosmetic here: a stable rendering means the same event
// produces the same prompt, which is what allows a response to be cached
// against an evidence hash instead of being paid for twice.
func formatFacts(facts map[string]string) string {
	keys := make([]string, 0, len(facts))
	for k := range facts {
		// Internal bookkeeping is not worth prompt tokens.
		if strings.HasPrefix(k, "NSE_SYMBOL_PATH") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+facts[k])
		if len(parts) >= 8 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func truncateWords(s string, max int) string {
	fields := strings.Fields(s)
	if len(fields) <= max {
		return strings.Join(fields, " ")
	}
	return strings.Join(fields[:max], " ") + "…"
}

// EvidenceHash identifies the exact set of evidence an event rests on.
//
// It is the cache key for classification. Ten outlets reporting one story
// produce one cluster and therefore one model call; when an eleventh arrives,
// the hash changes and the event is reclassified, because new corroboration
// can genuinely change how important something is.
func EvidenceHash(e news.Event) string {
	hashes := make([]string, 0, len(e.Evidence))
	for _, ev := range e.Evidence {
		hashes = append(hashes, ev.ContentHash)
	}
	sort.Strings(hashes)
	sum := sha256.Sum256([]byte(strings.Join(hashes, "|")))
	return hex.EncodeToString(sum[:12])
}
