package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reasoningLLM mimics a model that thinks before answering: it can return a
// successful HTTP 200 whose content is empty because the thinking consumed the
// token allowance.
func reasoningLLM(t *testing.T, content, finishReason string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"model": "reasoner",
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role":              "assistant",
					"content":           content,
					"reasoning_content": "Let me think about this at length...",
				},
				"finish_reason": finishReason,
			}},
			"usage": map[string]int{"prompt_tokens": 300, "completion_tokens": 900, "total_tokens": 1200},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTruncatedAnswerIsReportedAsTruncation(t *testing.T) {
	// The reasoning ate the allowance and the answer never arrived. This is a
	// successful HTTP call with useless output, and it must not be mistaken
	// for a parse failure.
	srv := reasoningLLM(t, "", "length")
	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MonthlyLimit: 100000},
		newMemBudget(), quiet())

	_, err := c.Complete(context.Background(), simpleRequest("hello"))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("error = %v, want ErrTruncated", err)
	}
	// The message must point at the fix.
	if !contains(err.Error(), "max_tokens") {
		t.Errorf("error %q should name the setting to change", err)
	}
}

func TestTruncationDoesNotTriggerARepairRetry(t *testing.T) {
	// Retrying an identical request truncates identically and bills twice.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"model": "reasoner",
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": ""},
				"finish_reason": "length",
			}},
			"usage": map[string]int{"total_tokens": 1200},
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MonthlyLimit: 100000},
		newMemBudget(), quiet())

	var out testDigestPayload
	_, err := c.CompleteJSON(context.Background(), simpleRequest("score these"), &out)
	if !errors.Is(err, ErrTruncated) {
		t.Errorf("error = %v, want ErrTruncated", err)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1 — a truncated answer must not be retried", calls)
	}
}

func TestEmptyContentThatFinishedNormallyIsNotTruncation(t *testing.T) {
	// finish_reason "stop" with empty content is a different problem: the
	// model genuinely had nothing to say.
	srv := reasoningLLM(t, "", "stop")
	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MonthlyLimit: 100000},
		newMemBudget(), quiet())

	resp, err := c.Complete(context.Background(), simpleRequest("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text != "" {
		t.Errorf("text = %q, want empty", resp.Text)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", resp.FinishReason)
	}
}

func TestExtraBodyIsMerged(t *testing.T) {
	// The escape hatch for vendor parameters: MiMo's thinking switch is not
	// part of the OpenAI schema, and hardcoding it would make this client
	// vendor-specific.
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":10}}`)
	}))
	defer srv.Close()

	c := New(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", MonthlyLimit: 100000,
		ExtraBody: map[string]any{"thinking": map[string]any{"type": "disabled"}},
	}, newMemBudget(), quiet())

	if _, err := c.Complete(context.Background(), simpleRequest("hi")); err != nil {
		t.Fatal(err)
	}
	thinking, ok := got["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Errorf("request body = %v, want the thinking parameter merged in", got)
	}
	// The standard fields must survive alongside it.
	if got["model"] != "m" {
		t.Errorf("model = %v, want m", got["model"])
	}
}

func TestExtraBodyNeverOverwritesAnExplicitField(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":10}}`)
	}))
	defer srv.Close()

	c := New(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "real-model", MonthlyLimit: 100000,
		ExtraBody: map[string]any{"model": "hijacked", "temperature": 9.9},
	}, newMemBudget(), quiet())

	c.Complete(context.Background(), Request{
		Feature: "x", Temperature: 0.3,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if got["model"] != "real-model" {
		t.Errorf("model = %v, want the caller's value to win", got["model"])
	}
	if got["temperature"] != 0.3 {
		t.Errorf("temperature = %v, want the caller's value to win", got["temperature"])
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestCompleteJSONReportsTruncationInsteadOfRetrying covers a real failure.
//
// When a batch overran the completion budget, the model returned well-formed
// JSON that simply stopped mid-object. That failed to parse, and the client
// retried — producing an identically truncated answer at twice the cost, then
// reporting "bad JSON", which points at the model rather than at the token cap
// that actually caused it.
func TestCompleteJSONReportsTruncationInsteadOfRetrying(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Well-formed right up to the cut, then nothing.
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"events\": [{\"id\": \"1\", \"summary\": \"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":2600,"total_tokens":2700}}`)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MonthlyLimit: 1_000_000}, newMemBudget(), quiet())
	var target struct {
		Events []struct{ ID string } `json:"events"`
	}
	_, err := c.CompleteJSON(context.Background(), Request{
		Feature: "test", MaxTokens: 2600,
		Messages: []Message{{Role: RoleUser, Content: "go"}},
	}, &target)

	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1 — a truncated answer must not be retried", calls)
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Errorf("error should name the cap that caused it: %v", err)
	}
}
