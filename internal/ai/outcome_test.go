package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordSink collects every outcome the client reports.
type recordSink struct {
	mu  sync.Mutex
	got []Outcome
}

func (r *recordSink) sink() OutcomeSink {
	return func(_ context.Context, o Outcome) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got = append(r.got, o)
	}
}

func (r *recordSink) only(t *testing.T) Outcome {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) != 1 {
		t.Fatalf("want exactly 1 reported outcome, got %d: %+v", len(r.got), r.got)
	}
	return r.got[0]
}

// clientWithSink builds a client pointed at url that reports to rec.
func clientWithSink(t *testing.T, url string, budget BudgetStore, limit int, rec *recordSink) *Client {
	t.Helper()
	return New(Config{
		BaseURL: url, APIKey: "test-key", Model: "main-model",
		CheapModel: "cheap-model", MonthlyLimit: limit,
	}, budget, quiet(), WithOutcomeSink(rec.sink()), WithClock(func() time.Time {
		return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	}))
}

// The production defect this whole port exists for.
//
// Status() answers from configuration and budget without making a call, so a
// key the upstream rejects looks identical to a working one. Before the sink,
// /api/health reported the LLM green and "not yet contacted" while every AI
// feature in the app was failing 401, and nothing watching that endpoint
// could have found out.
func TestRejectedKeyIsReportedAsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Invalid API Key","code":"401","type":"invalid_key"}}`)
	}))
	defer srv.Close()

	rec := &recordSink{}
	c := clientWithSink(t, srv.URL, newMemBudget(), 100000, rec)

	if _, err := c.Complete(context.Background(), simpleRequest("hello")); err == nil {
		t.Fatal("want an error from a rejected key")
	}
	// Status still reports OK, which is exactly why the sink is needed: the
	// two answer different questions and only one of them made a call.
	if got := c.Status(context.Background()); got != StatusOK {
		t.Logf("Status() = %v (it does not probe, so this is not the signal)", got)
	}

	o := rec.only(t)
	if o.OK || o.Skipped {
		t.Fatalf("a rejected key must count as a fault, got %+v", o)
	}
	if o.Err == nil {
		t.Fatal("want the upstream error carried through to the tracker")
	}
}

// A budget the operator set is a limitation, not a broken dependency. If this
// reported a fault, spending the monthly allowance would take the dot red and
// keep it there until the next month.
func TestSpentBudgetIsSkippedNotFailed(t *testing.T) {
	budget := newMemBudget()
	budget.used["2026-08"] = 1000

	rec := &recordSink{}
	c := clientWithSink(t, "http://127.0.0.1:1", budget, 1000, rec)

	if _, err := c.Complete(context.Background(), simpleRequest("hello")); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("want ErrBudgetExhausted, got %v", err)
	}
	o := rec.only(t)
	if o.OK || !o.Skipped {
		t.Fatalf("a spent budget must be skipped, not a fault, got %+v", o)
	}
}

// An unconfigured client never calls out, so it has nothing to say about the
// provider's health either.
func TestUnconfiguredIsSkipped(t *testing.T) {
	rec := &recordSink{}
	c := clientWithSink(t, "", newMemBudget(), 100000, rec)

	if _, err := c.Complete(context.Background(), simpleRequest("hello")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
	o := rec.only(t)
	if o.OK || !o.Skipped {
		t.Fatalf("unconfigured must be skipped, got %+v", o)
	}
}

func TestSuccessIsReportedOK(t *testing.T) {
	srv := newFakeLLM(t, "an answer")
	rec := &recordSink{}
	c := clientWithSink(t, srv.URL, newMemBudget(), 100000, rec)

	if _, err := c.Complete(context.Background(), simpleRequest("hello")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o := rec.only(t); !o.OK {
		t.Fatalf("want OK, got %+v", o)
	}
}

// Truncation is a completed round trip against a ceiling we chose. Counting
// it as a provider fault would take the dependency red over our own
// max_tokens, which is the opposite of what the dot is for.
func TestTruncationIsNotAProviderFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"test-model","choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":90,"total_tokens":100}}`)
	}))
	defer srv.Close()

	rec := &recordSink{}
	c := clientWithSink(t, srv.URL, newMemBudget(), 100000, rec)

	if _, err := c.Complete(context.Background(), simpleRequest("hello")); !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	if o := rec.only(t); !o.OK {
		t.Fatalf("truncation must not count against the provider, got %+v", o)
	}
}

// A caller that gives up says nothing about the provider.
func TestCallerCancellationIsNotAProviderFault(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	rec := &recordSink{}
	c := clientWithSink(t, srv.URL, newMemBudget(), 100000, rec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Complete(ctx, simpleRequest("hello")); err == nil {
		t.Fatal("want an error from a cancelled context")
	}
	o := rec.only(t)
	if o.OK || !o.Skipped {
		t.Fatalf("caller cancellation must be skipped, got %+v", o)
	}
}

// Redaction has to survive the sentinel path added above. The key can reach
// an error message when it is embedded in a URL, and an error message reaches
// logs, so this asserts the scrub on both the flattened and the unwrappable
// branch -- including through errors.Unwrap, which is the one way the new
// type could have exposed an unredacted cause.
func TestRedactionHoldsOnBothBranches(t *testing.T) {
	const key = "sk-secret-value"

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"plain", errors.New("dial https://host/" + key + "/v1: refused")},
		{"wraps a context sentinel", fmt.Errorf("Post \"https://host/%s/v1\": %w", key, context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.err, key)
			if strings.Contains(got.Error(), key) {
				t.Fatalf("key leaked: %q", got.Error())
			}
			if inner := errors.Unwrap(got); inner != nil && strings.Contains(inner.Error(), key) {
				t.Fatalf("key leaked through Unwrap: %q", inner.Error())
			}
		})
	}

	// And the sentinel really is still reachable, which is the whole point.
	wrapped := redact(fmt.Errorf("Post \"https://host/%s/v1\": %w", key, context.DeadlineExceeded), key)
	if !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatal("want context.DeadlineExceeded to survive redaction")
	}
}
