package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func quiet() Option { return WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// memBudget is an in-memory BudgetStore.
type memBudget struct {
	mu       sync.Mutex
	used     map[string]int
	features map[string]map[string]int
	readErr  error
	writeErr error
	writes   int
}

func newMemBudget() *memBudget {
	return &memBudget{used: map[string]int{}, features: map[string]map[string]int{}}
}

func (m *memBudget) LLMTokensUsed(_ context.Context, period string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readErr != nil {
		return 0, m.readErr
	}
	return m.used[period], nil
}

func (m *memBudget) RecordLLMUsage(_ context.Context, rec UsageRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.writeErr != nil {
		return m.writeErr
	}
	m.used[rec.Period] += rec.Usage.TotalTokens
	if m.features[rec.Period] == nil {
		m.features[rec.Period] = map[string]int{}
	}
	m.features[rec.Period][rec.Feature] += rec.Usage.TotalTokens
	return nil
}

func (m *memBudget) LLMUsageByFeature(_ context.Context, period string) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for k, v := range m.features[period] {
		out[k] = v
	}
	return out, nil
}

func (m *memBudget) totalUsed(period string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used[period]
}

// fakeLLM is a stand-in chat-completions endpoint.
type fakeLLM struct {
	*httptest.Server
	mu       sync.Mutex
	requests []chatRequest
	// replies are returned in order; the last one repeats.
	replies []string
	status  int
	rawBody string
	tokens  int
}

func newFakeLLM(t *testing.T, replies ...string) *fakeLLM {
	t.Helper()
	f := &fakeLLM{replies: replies, status: http.StatusOK, tokens: 100}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req chatRequest
		json.Unmarshal(raw, &req)

		f.mu.Lock()
		n := len(f.requests)
		f.requests = append(f.requests, req)
		status, rawBody, tokens := f.status, f.rawBody, f.tokens
		var reply string
		if len(f.replies) > 0 {
			if n < len(f.replies) {
				reply = f.replies[n]
			} else {
				reply = f.replies[len(f.replies)-1]
			}
		}
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if rawBody != "" {
			io.WriteString(w, rawBody)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model": "test-model",
			"choices": []any{map[string]any{
				"message":       map[string]string{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{
				"prompt_tokens": tokens / 2, "completion_tokens": tokens / 2, "total_tokens": tokens,
			},
		})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeLLM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeLLM) at(i int) chatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[i]
}

func newClient(t *testing.T, srv *fakeLLM, budget BudgetStore, limit int) *Client {
	t.Helper()
	url := ""
	if srv != nil {
		url = srv.URL
	}
	return New(Config{
		BaseURL: url, APIKey: "test-key", Model: "main-model",
		CheapModel: "cheap-model", MonthlyLimit: limit,
	}, budget, quiet(), WithClock(func() time.Time {
		return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	}))
}

func simpleRequest(text string) Request {
	return Request{
		Feature:  FeatureExplainMove,
		Messages: []Message{{Role: RoleUser, Content: text}},
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "complete", cfg: Config{BaseURL: "u", Model: "m", APIKey: "k"}, want: true},
		{name: "no base url", cfg: Config{Model: "m", APIKey: "k"}},
		{name: "no model", cfg: Config{BaseURL: "u", APIKey: "k"}},
		{name: "no key", cfg: Config{BaseURL: "u", Model: "m"}},
		{name: "empty", cfg: Config{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.cfg, nil, quiet()).Configured(); got != tc.want {
				t.Errorf("Configured = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnconfiguredClientRefusesWithoutCallingOut(t *testing.T) {
	c := New(Config{MonthlyLimit: 1000}, newMemBudget(), quiet())
	_, err := c.Complete(context.Background(), simpleRequest("hello"))
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("error = %v, want ErrNotConfigured", err)
	}
	if got := c.Status(context.Background()); got != StatusUnconfigured {
		t.Errorf("Status = %q, want unconfigured", got)
	}
}

func TestCheapModelRouting(t *testing.T) {
	srv := newFakeLLM(t, "hi")
	c := newClient(t, srv, newMemBudget(), 100000)

	if _, err := c.Complete(context.Background(), simpleRequest("a")); err != nil {
		t.Fatal(err)
	}
	if got := srv.at(0).Model; got != "main-model" {
		t.Errorf("model = %q, want main-model", got)
	}

	req := simpleRequest("b")
	req.Cheap = true
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := srv.at(1).Model; got != "cheap-model" {
		t.Errorf("cheap model = %q, want cheap-model", got)
	}
}

func TestCheapModelDefaultsToMain(t *testing.T) {
	c := New(Config{BaseURL: "u", Model: "m", APIKey: "k"}, nil, quiet())
	if c.CheapModel() != "m" {
		t.Errorf("CheapModel = %q, want it to fall back to the main model", c.CheapModel())
	}
}

// ---------------------------------------------------------------------------
// Budget
// ---------------------------------------------------------------------------

func TestBudgetEnforcement(t *testing.T) {
	tests := []struct {
		name        string
		limit       int
		preUsed     int
		wantAllowed bool
		wantErr     error
	}{
		{name: "well under the limit", limit: 100000, preUsed: 0, wantAllowed: true},
		{name: "near the limit", limit: 100000, preUsed: 99000, wantAllowed: true},
		{name: "at the limit", limit: 1000, preUsed: 1000, wantErr: ErrBudgetExhausted},
		{name: "over the limit", limit: 1000, preUsed: 5000, wantErr: ErrBudgetExhausted},
		{name: "zero limit disables AI", limit: 0, preUsed: 0, wantErr: ErrBudgetExhausted},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeLLM(t, "ok")
			budget := newMemBudget()
			budget.used["2026-08"] = tc.preUsed
			c := newClient(t, srv, budget, tc.limit)

			_, err := c.Complete(context.Background(), simpleRequest("hello"))
			if tc.wantAllowed {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if srv.count() != 1 {
					t.Errorf("upstream calls = %d, want 1", srv.count())
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if srv.count() != 0 {
				t.Errorf("upstream calls = %d, want 0 — a refused call must not reach the network", srv.count())
			}
		})
	}
}

func TestBudgetRefusesAnOversizedPrompt(t *testing.T) {
	// A prompt large enough to overshoot the remaining allowance is refused
	// before it is sent, rather than blowing through the budget in one call.
	srv := newFakeLLM(t, "ok")
	budget := newMemBudget()
	budget.used["2026-08"] = 900
	c := newClient(t, srv, budget, 1000)

	huge := strings.Repeat("x", 10000) // ~3000 estimated tokens
	_, err := c.Complete(context.Background(), simpleRequest(huge))
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("error = %v, want ErrBudgetExhausted", err)
	}
	if srv.count() != 0 {
		t.Error("the oversized request reached the network")
	}
}

func TestUsageIsRecorded(t *testing.T) {
	srv := newFakeLLM(t, "ok")
	srv.tokens = 250
	budget := newMemBudget()
	c := newClient(t, srv, budget, 100000)

	for i := 0; i < 3; i++ {
		if _, err := c.Complete(context.Background(), simpleRequest("hello")); err != nil {
			t.Fatal(err)
		}
	}
	if got := budget.totalUsed("2026-08"); got != 750 {
		t.Errorf("recorded %d tokens, want 750", got)
	}

	state, err := c.Budget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Used != 750 || state.Limit != 100000 || state.Exhausted {
		t.Errorf("budget state = %+v", state)
	}
	if state.Remaining() != 99250 {
		t.Errorf("remaining = %d, want 99250", state.Remaining())
	}
	if state.ByFeature[FeatureExplainMove] != 750 {
		t.Errorf("by-feature = %v", state.ByFeature)
	}
}

func TestUsageIsEstimatedWhenUpstreamOmitsIt(t *testing.T) {
	// Some OpenAI-compatible servers omit the usage block. Recording zero
	// would let the budget run unbounded.
	srv := newFakeLLM(t, "a reply of some length")
	srv.tokens = 0
	budget := newMemBudget()
	c := newClient(t, srv, budget, 100000)

	if _, err := c.Complete(context.Background(), simpleRequest("a prompt of some length")); err != nil {
		t.Fatal(err)
	}
	if got := budget.totalUsed("2026-08"); got <= 0 {
		t.Errorf("recorded %d tokens, want a positive estimate", got)
	}
}

func TestBudgetPeriodsAreMonthly(t *testing.T) {
	srv := newFakeLLM(t, "ok")
	// One call spends the whole month's allowance.
	srv.tokens = 200
	budget := newMemBudget()
	now := time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC)
	c := New(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", MonthlyLimit: 200,
	}, budget, quiet(), WithClock(func() time.Time { return now }))

	if _, err := c.Complete(context.Background(), simpleRequest("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), simpleRequest("b")); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("second call error = %v, want ErrBudgetExhausted", err)
	}

	// Crossing into September starts a fresh allowance.
	now = time.Date(2026, 9, 1, 0, 30, 0, 0, time.UTC)
	if _, err := c.Complete(context.Background(), simpleRequest("c")); err != nil {
		t.Errorf("the new month should have a fresh budget, got: %v", err)
	}
	if c.Period() != "2026-09" {
		t.Errorf("period = %q, want 2026-09", c.Period())
	}
}

func TestBudgetFailsClosedWhenUnreadable(t *testing.T) {
	// If we cannot tell how much has been spent, spending more risks an
	// unbounded bill.
	srv := newFakeLLM(t, "ok")
	budget := newMemBudget()
	budget.readErr = errors.New("database unavailable")
	c := newClient(t, srv, budget, 100000)

	if _, err := c.Complete(context.Background(), simpleRequest("hello")); err == nil {
		t.Error("want an error when the budget cannot be read")
	}
	if srv.count() != 0 {
		t.Error("a call was made while the budget was unreadable")
	}
}

func TestRecordFailureDoesNotFailTheCall(t *testing.T) {
	// The tokens are already spent; losing the accounting is bad but must not
	// discard the answer the operator is waiting for.
	srv := newFakeLLM(t, "the answer")
	budget := newMemBudget()
	budget.writeErr = errors.New("disk full")
	c := newClient(t, srv, budget, 100000)

	got, err := c.Complete(context.Background(), simpleRequest("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Text != "the answer" {
		t.Errorf("text = %q", got.Text)
	}
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		preUsed int
		want    Status
	}{
		{name: "healthy", limit: 1000, preUsed: 0, want: StatusOK},
		{name: "exhausted", limit: 1000, preUsed: 1000, want: StatusBudgetReached},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			budget := newMemBudget()
			budget.used["2026-08"] = tc.preUsed
			c := newClient(t, newFakeLLM(t, "x"), budget, tc.limit)
			if got := c.Status(context.Background()); got != tc.want {
				t.Errorf("Status = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transport failures
// ---------------------------------------------------------------------------

func TestUpstreamErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantText string
	}{
		{
			name:     "structured error payload",
			status:   http.StatusBadRequest,
			body:     `{"error":{"message":"model not found","type":"invalid_request_error"}}`,
			wantText: "model not found",
		},
		{
			name:     "html error page",
			status:   http.StatusBadGateway,
			body:     "<html>502 Bad Gateway</html>",
			wantText: "502",
		},
		{
			name:     "empty choices",
			status:   http.StatusOK,
			body:     `{"choices":[],"usage":{"total_tokens":1}}`,
			wantText: "no choices",
		},
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized,
			body:     `{"error":{"message":"invalid api key"}}`,
			wantText: "invalid api key",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeLLM(t)
			srv.status, srv.rawBody = tc.status, tc.body
			c := newClient(t, srv, newMemBudget(), 100000)

			_, err := c.Complete(context.Background(), simpleRequest("hello"))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not mention %q", err, tc.wantText)
			}
		})
	}
}

func TestAPIKeyIsNeverLeaked(t *testing.T) {
	const key = "sk-SUPER-SECRET-VALUE"
	c := New(Config{
		BaseURL: "http://127.0.0.1:1", APIKey: key, Model: "m", MonthlyLimit: 1000,
	}, newMemBudget(), quiet())

	_, err := c.Complete(context.Background(), simpleRequest("hello"))
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "SUPER-SECRET-VALUE") {
		t.Errorf("the API key leaked into an error: %v", err)
	}
}

func TestAuthorizationHeaderIsSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"x"}}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "abc123", Model: "m", MonthlyLimit: 1000},
		newMemBudget(), quiet())
	if _, err := c.Complete(context.Background(), simpleRequest("hi")); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer abc123" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := newFakeLLM(t, "ok")
	c := newClient(t, srv, newMemBudget(), 100000)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Complete(ctx, simpleRequest("hello")); err == nil {
		t.Error("want an error on a cancelled context")
	}
}
