package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/jev"
	"github.com/tradesys/dashboard/internal/news"
)

// classifyStore is an OutputStore and EventStore that remembers what was saved.
type classifyStore struct {
	OutputStore
	mu      sync.Mutex
	pending []news.Event
	saved   map[int64]events.Classification
	facts   map[int64]map[string]string
}

func newClassifyStore(evs ...news.Event) *classifyStore {
	return &classifyStore{pending: evs, saved: map[int64]events.Classification{}, facts: map[int64]map[string]string{}}
}

func (s *classifyStore) ListUnclassifiedEvents(context.Context, int) ([]news.Event, error) {
	return s.pending, nil
}
func (s *classifyStore) EventFacts(context.Context, int64) (map[string]string, error) {
	return map[string]string{}, nil
}
func (s *classifyStore) SaveEventClassification(_ context.Context, c events.Classification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved[c.EventID] = c
	return nil
}
func (s *classifyStore) SaveEventFacts(_ context.Context, id int64, f map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.facts[id] = f
	return nil
}
func (s *classifyStore) ResolveTicker(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// jevServer answers every question with a canned answer, and records what it
// was asked.
func jevServer(t *testing.T, answers map[string]any) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var req struct {
			Questions map[string]any `json:"questions"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		out := map[string]any{}
		for name := range req.Questions {
			if a, ok := answers[name]; ok {
				out[name] = a
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-test", "answers": out, "usage": map[string]int{"input_tokens": 300},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func choice(pick string, conf float64) map[string]any {
	return map[string]any{"type": "choice", "choice": pick, "confidence": conf,
		"probabilities": map[string]float64{pick: conf}}
}
func score(level int, probs map[string]float64) map[string]any {
	return map[string]any{"type": "score", "score": float64(level), "confidence": 0.9, "probabilities": probs}
}
func noul(v float64) map[string]any { return map[string]any{"type": "noul", "noul": v} }

func appleEvent() news.Event {
	return news.Event{
		ID: 1, Type: "UNCLASSIFIED", Headline: "Apple reports record quarterly revenue",
		DiscoveredAt: time.Now(), SourceCount: 2,
		Entities: []news.EventEntity{{Symbol: "AAPL", Relationship: news.RelPrimary, MatchConfidence: 0.99}},
	}
}

// With Jev configured, classification is Jev's. The text model must not be
// called at all -- that is what protects its daily budget for prose.
func TestClassificationGoesToJevWhenConfigured(t *testing.T) {
	srv, jevCalls := jevServer(t, map[string]any{
		"event_type": choice("EARNINGS", 0.92),
		"importance": score(3, map[string]float64{"3": 1}),
		"is_event":   noul(0.97),
	})
	var llmCalls int32
	llm := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&llmCalls, 1)
	}))
	defer llm.Close()

	store := newClassifyStore(appleEvent())
	svc := NewService(newClient(t, &fakeLLM{Server: llm}, newMemBudget(), 1e9), nil, store, time.UTC,
		WithJev(jev.New("ts_k", jev.WithBaseURL(srv.URL))))

	n, err := svc.ClassifyEvents(context.Background(), 10)
	if err != nil || n != 1 {
		t.Fatalf("ClassifyEvents = %d, %v", n, err)
	}
	if *jevCalls != 1 {
		t.Errorf("jev called %d times, want 1", *jevCalls)
	}
	if llmCalls != 0 {
		t.Errorf("the text model was called %d times; classification belongs to Jev", llmCalls)
	}
	if got := store.saved[1]; got.Type != "EARNINGS" || got.Model != "jev-test" {
		t.Errorf("saved = %+v", got)
	}
}

// A type Jev is unsure of does not override the one the keyword rules gave.
// The empty type is what SaveEventClassification treats as "keep what is there".
func TestLowConfidenceTypeKeepsTheDeterministicOne(t *testing.T) {
	srv, _ := jevServer(t, map[string]any{
		"event_type": choice("MERGER", 0.31),
		"importance": score(2, map[string]float64{"2": 1}),
		"is_event":   noul(0.9),
	})
	store := newClassifyStore(appleEvent())
	svc := NewService(nil, nil, store, time.UTC, WithJev(jev.New("k", jev.WithBaseURL(srv.URL))))
	if _, err := svc.ClassifyEvents(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	got := store.saved[1]
	if got.Type != "" {
		t.Errorf("type = %q at 0.31 confidence; below the floor the rules' type must stand", got.Type)
	}
	if got.Confidence == nil || *got.Confidence != 0.31 {
		t.Error("the confidence is still recorded, so the uncertainty is visible")
	}
}

// Importance is the expectation over the rubric, not the single pick. Split
// 60/40 between "moves the stock" and "rewrites the case", it lands between.
func TestImportanceUsesTheWholeDistribution(t *testing.T) {
	srv, _ := jevServer(t, map[string]any{
		"event_type": choice("EARNINGS", 0.9),
		"importance": score(3, map[string]float64{"3": 0.6, "4": 0.4}),
		"is_event":   noul(0.95),
	})
	store := newClassifyStore(appleEvent())
	svc := NewService(nil, nil, store, time.UTC, WithJev(jev.New("k", jev.WithBaseURL(srv.URL))))
	if _, err := svc.ClassifyEvents(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	// 0.6*7.5 + 0.4*9.5 = 8.3 -> 8
	if got := store.saved[1].Importance; got == nil || *got != 8 {
		t.Errorf("importance = %v, want 8 from the distribution", got)
	}
}

// The admission filter. Jev at least 80% sure this is not an event -- a quote
// page, a commentary piece -- takes it to importance zero, which drops it from
// the default feed without deleting anything, and records why.
func TestNonEventsAreDemotedAndExplained(t *testing.T) {
	srv, _ := jevServer(t, map[string]any{
		"event_type": choice("PRESS_RELEASE", 0.7),
		"importance": score(2, map[string]float64{"2": 1}),
		"is_event":   noul(0.08),
	})
	ev := appleEvent()
	ev.Headline = "AAPL stock price, news, quote and history"
	store := newClassifyStore(ev)
	svc := NewService(nil, nil, store, time.UTC, WithJev(jev.New("k", jev.WithBaseURL(srv.URL))))
	if _, err := svc.ClassifyEvents(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := store.saved[1].Importance; got == nil || *got != 0 {
		t.Errorf("importance = %v; a quote page scored as worth reading is still a quote page", got)
	}
	if store.facts[1]["JEV_IS_EVENT"] != "0.08" {
		t.Errorf("facts = %v; the verdict must be kept so the hidden item can be explained", store.facts[1])
	}
}

// Direction below the floor is "unclear", which is what the prompt Jev
// replaces asked for instead of a coin flip.
func TestUnsureDirectionIsUnclear(t *testing.T) {
	srv, _ := jevServer(t, map[string]any{
		"event_type":     choice("MANAGEMENT_CHANGE", 0.9),
		"importance":     score(3, map[string]float64{"3": 1}),
		"is_event":       noul(0.95),
		"direction_AAPL": choice("negative", 0.41),
		"impact_AAPL":    score(2, map[string]float64{"2": 1}),
	})
	store := newClassifyStore(appleEvent())
	svc := NewService(nil, nil, store, time.UTC, WithJev(jev.New("k", jev.WithBaseURL(srv.URL))))
	if _, err := svc.ClassifyEvents(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	ents := store.saved[1].Entities
	if len(ents) != 1 || ents[0].Direction != news.DirectionUnclear {
		t.Fatalf("entities = %+v, want AAPL unclear at 0.41 confidence", ents)
	}
	if ents[0].ImpactStrength <= 0 {
		t.Error("impact must be filled, or the upsert would zero the stored value")
	}
}

// Before Jev credits exist, classification stays on the text model, as it was.
func TestFallsBackToTheTextModelWithoutJev(t *testing.T) {
	llm := newFakeLLM(t, `{"events":[{"id":"1","event_type":"EARNINGS","importance":7,"confidence":0.8}]}`)
	store := newClassifyStore(appleEvent())
	svc := NewService(newClient(t, llm, newMemBudget(), 1e9), nil, store, time.UTC,
		WithJev(jev.New(""))) // present but unconfigured: no key
	if _, err := svc.ClassifyEvents(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if llm.count() == 0 {
		t.Error("with no Jev key, classification must still run on the text model")
	}
}

type digestStore struct {
	pending []news.Article
	saved   []news.Score
}

func (d *digestStore) ListUnscored(context.Context, int) ([]news.Article, error) {
	return d.pending, nil
}
func (d *digestStore) SaveScores(_ context.Context, s []news.Score) error {
	d.saved = append(d.saved, s...)
	return nil
}

// The digest is Jev's when Jev is configured, and must run even when the text
// model has no client at all -- its guard is irrelevant to work it never does.
func TestDigestScoresWithJevAndIgnoresTheTextModelGuard(t *testing.T) {
	srv, calls := jevServer(t, map[string]any{
		"relevance": score(3, map[string]float64{"3": 0.5, "2": 0.5}),
		"sentiment": map[string]any{"type": "choice", "choice": "positive", "confidence": 0.6,
			"probabilities": map[string]float64{"positive": 0.7, "neutral": 0.2, "negative": 0.1}},
	})
	store := &digestStore{pending: []news.Article{
		{ID: 7, Symbol: "AAPL", Title: "Apple beats on iPhone sales", Source: "wsj", PublishedAt: time.Now()},
	}}
	// No text-model client: with the old guard this returned ErrNotConfigured.
	svc := NewService(nil, nil, nil, time.UTC, WithScoreStore(store),
		WithJev(jev.New("k", jev.WithBaseURL(srv.URL))))
	if _, err := svc.RunNewsDigest(context.Background(), 10); err != nil {
		t.Fatalf("RunNewsDigest: %v", err)
	}
	if *calls != 1 || len(store.saved) != 1 {
		t.Fatalf("jev calls=%d saved=%d, want 1 and 1", *calls, len(store.saved))
	}
	sc := store.saved[0]
	// 0.5*1.0 + 0.5*0.67 = 0.835
	if sc.Relevance < 0.83 || sc.Relevance > 0.84 {
		t.Errorf("relevance = %v, want ~0.835 from the distribution", sc.Relevance)
	}
	// 0.7 - 0.1
	if sc.Sentiment < 0.599 || sc.Sentiment > 0.601 {
		t.Errorf("sentiment = %v, want 0.6 (positive minus negative weight)", sc.Sentiment)
	}
}
