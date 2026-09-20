package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/research"
)

func TestResearchGatesEveryGeneratedClaimOnSuppliedEvidence(t *testing.T) {
	srv := newFakeLLM(t, `{"summary":"Supported summary","summary_sources":[1],"sections":[{"heading":"Unsupported","body":"Invented section","sources":[2]}],"findings":[{"claim":"Valid claim","sources":[1,1,2,99]},{"claim":"Headline inference","sources":[2]}],"companies":[{"symbol":"MADEUP","sources":[2]}],"gaps":[]}`)
	svc := NewService(newClient(t, srv, newMemBudget(), 100000), nil, nil, time.UTC)
	turn := &research.Turn{Question: "revenue"}
	result := research.Result{Findings: []research.Finding{
		{Title: "Report", URL: "https://filing.example/report", Body: strings.Repeat("Revenue grew. ", 100), Words: 200},
		{Title: "Headline", URL: "https://headline.example/story"},
	}}
	if err := svc.synthesise(context.Background(), research.Conversation{}, turn, result); err != nil {
		t.Fatal(err)
	}
	if len(turn.Sections) != 1 || turn.Sections[0].Heading != "Research brief" || len(turn.Findings) != 1 || len(turn.Findings[0].Sources) != 1 || len(turn.Companies) != 0 {
		t.Fatalf("unsupported claims survived: %+v", turn)
	}
	if turn.Answer != "" {
		t.Fatal("uncited summary emitted")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.requests) != 1 || srv.requests[0].MaxTokens != 6000 {
		t.Fatal("unexpected synthesis count or output budget")
	}
}

func TestResearchSkipsModelWhenOnlyHeadlinesExist(t *testing.T) {
	srv := newFakeLLM(t, `{}`)
	svc := NewService(newClient(t, srv, newMemBudget(), 100000), nil, nil, time.UTC)
	err := svc.synthesise(context.Background(), research.Conversation{}, &research.Turn{}, research.Result{Findings: []research.Finding{{Title: "Only a headline"}}})
	if err == nil {
		t.Fatal("accepted headline-only evidence")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.requests) != 0 {
		t.Fatal("spent AI tokens without readable evidence")
	}
}

func TestCompletionBudgetIncludesRequestedOutput(t *testing.T) {
	srv := newFakeLLM(t, "hello")
	client := newClient(t, srv, newMemBudget(), 1000)
	req := simpleRequest("hello")
	req.MaxTokens = 1100
	if _, err := client.Complete(context.Background(), req); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("output not reserved: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.requests) != 0 {
		t.Fatal("oversize request reached provider")
	}
}

func TestResearchCapacityRefusesBeforePersistence(t *testing.T) {
	svc := NewService(nil, nil, nil, time.UTC)
	svc.researchJobs = 2
	_, _, err := svc.AskWithMode(context.Background(), research.NewEngine(nil), nil, 0, "Question", 12, true)
	if !errors.Is(err, ErrResearchBusy) {
		t.Fatalf("capacity was not enforced: %v", err)
	}
}

type researchTestStore struct {
	research.Store
	completed chan research.Turn
	failed    chan string
}

func (s *researchTestStore) CreateConversation(context.Context, string) (int64, error) { return 1, nil }
func (s *researchTestStore) StartTurn(_ context.Context, id int64, question string) (*research.Turn, error) {
	return &research.Turn{ID: 1, ConversationID: id, Question: question, Status: research.StatusRunning}, nil
}
func (s *researchTestStore) GetConversation(context.Context, int64) (research.Conversation, error) {
	return research.Conversation{ID: 1, Turns: []research.Turn{{ID: 2, Question: "Earlier question", Answer: "Earlier answer", Status: research.StatusDone}}}, nil
}
func (s *researchTestStore) RecordProgress(context.Context, int64, string, string) error { return nil }
func (s *researchTestStore) CompleteTurn(_ context.Context, turn *research.Turn) error {
	s.completed <- *turn
	return nil
}
func (s *researchTestStore) FailTurn(_ context.Context, _ int64, reason string) error {
	s.failed <- reason
	return nil
}

type researchTestScraper struct{}

func (researchTestScraper) Name() string { return "official" }
func (researchTestScraper) Trust() int   { return 100 }
func (researchTestScraper) Search(context.Context, string, int) ([]research.Finding, error) {
	return []research.Finding{{Title: "Company revenue", URL: "https://official.example/revenue", Publisher: "Official", Trust: 100, Body: strings.Repeat("Revenue increased during the year. ", 40)}}, nil
}

func TestEvidenceOnlyCompletesAndDoesNotSpendOnFollowupRewrite(t *testing.T) {
	srv := newFakeLLM(t, `{}`)
	svc := NewService(newClient(t, srv, newMemBudget(), 100000), nil, nil, time.UTC)
	store := &researchTestStore{completed: make(chan research.Turn, 1), failed: make(chan string, 1)}
	queued, _, err := svc.AskWithMode(context.Background(), research.NewEngine([]research.Scraper{researchTestScraper{}}), store, 1, "What about revenue?", 12, true)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-store.completed:
		if len(result.Sources) != 1 || result.Answer == "" || result.Model != "" || result.Degraded {
			t.Fatalf("evidence result: %+v", result)
		}
		if queued.Answer != "" {
			t.Fatal("background worker mutated the queued response")
		}
	case reason := <-store.failed:
		t.Fatal(reason)
	case <-time.After(time.Second):
		t.Fatal("evidence job did not complete")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.requests) != 0 {
		t.Fatal("evidence-only mode spent AI tokens")
	}
}
