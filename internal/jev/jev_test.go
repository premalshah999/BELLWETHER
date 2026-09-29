package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// docsResponse is the response body from TypeSafe's quickstart, verbatim.
// Decoding it is the test that this package reads what the API actually sends.
const docsResponse = `{
  "model": "jev-1.13.0",
  "answers": {
    "department": {"type": "choice", "choice": "technical", "confidence": 0.78,
      "probabilities": {"technical": 0.85, "sales": 0.0, "billing": 0.15}},
    "frustration": {"type": "score", "score": 1.0, "confidence": 1.0,
      "legend": {"0": "Calm, just stating facts", "1": "Frustrated but civil", "2": "Very angry, strong language"},
      "probabilities": {"0": 0.0, "1": 1.0, "2": 0.0}},
    "is_urgent": {"type": "noul", "noul": 1.0}
  },
  "usage": {"input_tokens": 392, "output_tokens": 65}
}`

func noWait(context.Context, time.Duration) error { return nil }

func docsRequest() Request {
	return Request{
		Feature: "test",
		State:   "Hi, the Stripe integration keeps failing. Please help ASAP.",
		Questions: map[string]Question{
			"department": Choice{Instructions: "Which team should handle this", Criteria: map[string]string{
				"billing": "Payment or subscription issues", "technical": "Bugs or integration problems",
				"sales": "Pricing or account questions"}},
			"frustration": Score{Instructions: "How frustrated the customer appears", Criteria: []string{
				"Calm, just stating facts", "Frustrated but civil", "Very angry, strong language"}},
			"is_urgent": Noul{Instructions: "The message conveys urgency"},
		},
	}
}

// The request has to match the documented wire format exactly: bearer auth,
// state, model, and questions keyed by name with type/instructions/criteria.
func TestRequestMatchesTheDocumentedWireFormat(t *testing.T) {
	var body map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		io.WriteString(w, docsResponse)
	}))
	defer srv.Close()

	c := New("ts_test_key", WithBaseURL(srv.URL))
	if _, err := c.Ask(context.Background(), docsRequest()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if auth != "Bearer ts_test_key" {
		t.Errorf("Authorization = %q, want the bearer form", auth)
	}
	if body["model"] != DefaultModel {
		t.Errorf("model = %v, want %s", body["model"], DefaultModel)
	}
	qs, _ := body["questions"].(map[string]any)
	dept, _ := qs["department"].(map[string]any)
	if dept["type"] != "choice" {
		t.Errorf("choice type = %v", dept["type"])
	}
	if _, isMap := dept["criteria"].(map[string]any); !isMap {
		t.Error("choice criteria must be an object of key to description")
	}
	fr, _ := qs["frustration"].(map[string]any)
	if _, isList := fr["criteria"].([]any); !isList {
		t.Error("score criteria must be an ordered list, lowest level first")
	}
	urg, _ := qs["is_urgent"].(map[string]any)
	if _, has := urg["criteria"]; has {
		t.Error("a noul carries no criteria")
	}
}

// Decoding the documentation's own example response, field for field.
func TestDecodesTheDocumentedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, docsResponse)
	}))
	defer srv.Close()

	resp, err := New("k", WithBaseURL(srv.URL)).Ask(context.Background(), docsRequest())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if resp.Model != "jev-1.13.0" || resp.Usage.InputTokens != 392 {
		t.Errorf("model/usage = %q/%+v", resp.Model, resp.Usage)
	}

	ch, ok := resp.Choice("department")
	if !ok || ch.Choice != "technical" || ch.Confidence != 0.78 || ch.Probabilities["billing"] != 0.15 {
		t.Errorf("choice = %+v, ok=%v", ch, ok)
	}
	sc, ok := resp.Score("frustration")
	if !ok || sc.Level() != 1 || sc.Legend["1"] != "Frustrated but civil" {
		t.Errorf("score = %+v, ok=%v", sc, ok)
	}
	nl, ok := resp.Noul("is_urgent")
	if !ok || nl.Noul != 1.0 {
		t.Errorf("noul = %+v, ok=%v", nl, ok)
	}

	// Asking for an answer under the wrong type is a miss, not a zero value
	// that looks like a real answer.
	if _, ok := resp.Score("department"); ok {
		t.Error("a choice answer must not decode as a score")
	}
	if _, ok := resp.Noul("missing"); ok {
		t.Error("an unasked question must report ok=false")
	}
}

// The expectation keeps what the single pick throws away. Split between two
// levels, it lands between their values rather than snapping to one.
func TestScoreExpectationUsesTheWholeDistribution(t *testing.T) {
	a := ScoreAnswer{Score: 3, Probabilities: map[string]float64{"3": 0.6, "4": 0.4}}
	values := []float64{1, 3, 5, 7, 9}
	if got := a.Expected(values); got < 7.79 || got > 7.81 {
		t.Errorf("Expected = %v, want 7.8 (0.6*7 + 0.4*9)", got)
	}
	// No distribution: fall back to the pick.
	if got := (ScoreAnswer{Score: 2}).Expected(values); got != 5 {
		t.Errorf("fallback Expected = %v, want the picked level's value 5", got)
	}
}

// 429 and 529 are the two statuses TypeSafe says to retry with backoff.
func TestRetriesRateLimitAndOverload(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, 529} {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if atomic.AddInt32(&calls, 1) < 3 {
				w.WriteHeader(status)
				return
			}
			io.WriteString(w, docsResponse)
		}))
		c := New("k", WithBaseURL(srv.URL), withSleep(noWait))
		if _, err := c.Ask(context.Background(), docsRequest()); err != nil {
			t.Errorf("status %d: want success after retries, got %v", status, err)
		}
		if calls != 3 {
			t.Errorf("status %d: %d attempts, want 3", status, calls)
		}
		srv.Close()
	}
}

// A rejected key and a malformed request do not change on the next attempt,
// so retrying them only spends time and hides the real error.
func TestDoesNotRetryAuthOrValidationFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusUnprocessableEntity} {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"nope"}`)
		}))
		_, err := New("k", WithBaseURL(srv.URL), withSleep(noWait)).Ask(context.Background(), docsRequest())
		if err == nil {
			t.Errorf("status %d: want an error", status)
		}
		if calls != 1 {
			t.Errorf("status %d: %d attempts, want exactly 1", status, calls)
		}
		if status == http.StatusUnauthorized && !errors.Is(err, ErrUnauthorized) {
			t.Errorf("401 must be ErrUnauthorized, got %v", err)
		}
		srv.Close()
	}
}

// Limits from the API reference, enforced before a request is spent.
func TestLocalValidationMatchesTheDocumentedLimits(t *testing.T) {
	big := map[string]string{}
	for i := 0; i < 256; i++ {
		big[string(rune('a'+i%26))+strings.Repeat("x", i/26)] = "opt"
	}
	for name, q := range map[string]Question{
		"choice over 255 options": Choice{Instructions: "x", Criteria: big},
		"choice with one option":  Choice{Instructions: "x", Criteria: map[string]string{"a": "only"}},
		"score with one level":    Score{Instructions: "x", Criteria: []string{"only"}},
		"score with eleven":       Score{Instructions: "x", Criteria: make([]string, 11)},
		"noul without text":       Noul{},
	} {
		t.Run(name, func(t *testing.T) {
			var called bool
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer srv.Close()
			_, err := New("k", WithBaseURL(srv.URL)).Ask(context.Background(),
				Request{State: "s", Questions: map[string]Question{"q": q}})
			if err == nil {
				t.Error("want a validation error")
			}
			if called {
				t.Error("an invalid question must fail before spending a request")
			}
		})
	}
}

// No key means Jev is unavailable, not broken: reported as skipped so it never
// turns the health dot red, and nothing is sent.
func TestUnconfiguredIsSkippedNotFailed(t *testing.T) {
	var got Outcome
	c := New("", WithOutcomeSink(func(_ context.Context, o Outcome) { got = o }))
	if _, err := c.Ask(context.Background(), docsRequest()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
	if !got.Skipped || got.OK {
		t.Errorf("outcome = %+v, want skipped", got)
	}
}

// Spend is recorded at the configured price, and only for requests that
// succeeded -- a failed request is not billed.
type spendLog struct{ cost float64 }

func (s *spendLog) RecordJevUsage(_ context.Context, _ string, _ Usage, cost float64, _ time.Time) error {
	s.cost += cost
	return nil
}

func TestSpendIsRecordedAtTheConfiguredPrice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, docsResponse) // 392 input tokens
	}))
	defer srv.Close()
	log := &spendLog{}
	c := New("k", WithBaseURL(srv.URL), WithInputPrice(0.042), WithSpendRecorder(log))
	if _, err := c.Ask(context.Background(), docsRequest()); err != nil {
		t.Fatal(err)
	}
	want := 392 * 0.042 / 1e6
	if diff := log.cost - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("recorded %.12f, want %.12f", log.cost, want)
	}
}
