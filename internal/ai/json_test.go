package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testDigestPayload struct {
	Scores []struct {
		ID        string  `json:"id"`
		Relevance float64 `json:"relevance"`
		Sentiment float64 `json:"sentiment"`
		OneLine   string  `json:"one_line"`
	} `json:"scores"`
}

func TestExtractJSON(t *testing.T) {
	// Models wrap JSON in fences, prefix it with prose, and append remarks.
	// Fighting that with prompting alone does not work; extraction does.
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare object",
			in:   `{"a":1}`,
			want: `{"a":1}`,
		},
		{
			name: "surrounded by whitespace",
			in:   "  \n {\"a\":1}\n  ",
			want: `{"a":1}`,
		},
		{
			name: "fenced with a language tag",
			in:   "```json\n{\"a\":1}\n```",
			want: `{"a":1}`,
		},
		{
			name: "fenced without a tag",
			in:   "```\n{\"a\":1}\n```",
			want: `{"a":1}`,
		},
		{
			name: "prose before",
			in:   `Here is the JSON you asked for: {"a":1}`,
			want: `{"a":1}`,
		},
		{
			name: "prose after",
			in:   `{"a":1} — let me know if you need anything else.`,
			want: `{"a":1}`,
		},
		{
			name: "prose on both sides",
			in:   "Sure!\n\n{\"a\":1}\n\nHope that helps.",
			want: `{"a":1}`,
		},
		{
			name: "array at the top level",
			in:   `[1,2,3]`,
			want: `[1,2,3]`,
		},
		{
			name: "nested braces",
			in:   `{"a":{"b":{"c":1}}}`,
			want: `{"a":{"b":{"c":1}}}`,
		},
		{
			// A brace inside a string literal must not end the scan early.
			name: "braces inside a string value",
			in:   `{"note":"use {curly} braces","n":1}`,
			want: `{"note":"use {curly} braces","n":1}`,
		},
		{
			name: "escaped quote inside a string",
			in:   `{"note":"he said \"hi\"","n":1}`,
			want: `{"note":"he said \"hi\"","n":1}`,
		},
		{name: "no json at all", in: "I cannot help with that.", want: ""},
		{name: "empty", in: "", want: ""},
		{name: "unbalanced", in: `{"a":1`, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSON(tc.in); got != tc.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCompleteJSONFirstTry(t *testing.T) {
	srv := newFakeLLM(t, `{"scores":[{"id":"a1","relevance":0.9,"sentiment":-0.4,"one_line":"Guidance cut."}]}`)
	c := newClient(t, srv, newMemBudget(), 100000)

	var out testDigestPayload
	if _, err := c.CompleteJSON(context.Background(), simpleRequest("score these"), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Scores) != 1 || out.Scores[0].ID != "a1" || out.Scores[0].Relevance != 0.9 {
		t.Errorf("decoded = %+v", out.Scores)
	}
	if srv.count() != 1 {
		t.Errorf("made %d calls, want 1 — no retry was needed", srv.count())
	}
	// The JSON instruction must be appended to the final user message.
	if !strings.Contains(srv.at(0).Messages[0].Content, "single valid JSON value") {
		t.Error("the JSON instruction was not appended to the prompt")
	}
}

func TestCompleteJSONRepairsOnce(t *testing.T) {
	tests := []struct {
		name       string
		firstReply string
	}{
		{name: "prose instead of JSON", firstReply: "Sure, here are the scores!"},
		{name: "truncated JSON", firstReply: `{"scores":[{"id":"a1",`},
		{name: "single quotes", firstReply: `{'scores':[]}`},
		{name: "trailing comma", firstReply: `{"scores":[{"id":"a1","relevance":0.5,},]}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			good := `{"scores":[{"id":"a1","relevance":0.5,"sentiment":0,"one_line":"ok"}]}`
			srv := newFakeLLM(t, tc.firstReply, good)
			c := newClient(t, srv, newMemBudget(), 100000)

			var out testDigestPayload
			resp, err := c.CompleteJSON(context.Background(), simpleRequest("score these"), &out)
			if err != nil {
				t.Fatalf("the repair attempt should have succeeded: %v", err)
			}
			if len(out.Scores) != 1 {
				t.Fatalf("decoded %d scores, want 1", len(out.Scores))
			}
			if srv.count() != 2 {
				t.Errorf("made %d calls, want exactly 2", srv.count())
			}

			// The retry must show the model its own output and the error.
			retry := srv.at(1)
			if len(retry.Messages) < 3 {
				t.Fatalf("retry had %d messages, want the original plus the failed reply and the error", len(retry.Messages))
			}
			if retry.Messages[len(retry.Messages)-2].Role != RoleAssistant {
				t.Error("the retry did not include the model's failed output")
			}
			if !strings.Contains(retry.Messages[len(retry.Messages)-1].Content, "could not be parsed") {
				t.Error("the retry did not include the parse error")
			}

			// Both calls were billed, so the reported cost covers both.
			if resp.Usage.TotalTokens != 200 {
				t.Errorf("usage = %d, want 200 (both calls counted)", resp.Usage.TotalTokens)
			}
		})
	}
}

func TestCompleteJSONGivesUpAfterOneRepair(t *testing.T) {
	// Two failures is the limit. A third call would spend tokens on a model
	// that is evidently not going to comply.
	srv := newFakeLLM(t, "not json", "still not json", "definitely not json")
	c := newClient(t, srv, newMemBudget(), 100000)

	var out testDigestPayload
	_, err := c.CompleteJSON(context.Background(), simpleRequest("score these"), &out)
	if !errors.Is(err, ErrBadJSON) {
		t.Fatalf("error = %v, want ErrBadJSON", err)
	}
	if srv.count() != 2 {
		t.Errorf("made %d calls, want exactly 2", srv.count())
	}
}

func TestCompleteJSONBudgetExhaustionDuringRepair(t *testing.T) {
	// The first call succeeds but does not parse; the retry runs out of
	// budget. The caller must see the budget error, which is actionable,
	// rather than a parse error, which is not.
	srv := newFakeLLM(t, "not json", `{"scores":[]}`)
	// The first call spends the entire allowance, leaving nothing for the
	// repair attempt.
	srv.tokens = 600
	budget := newMemBudget()
	c := newClient(t, srv, budget, 600)

	var out testDigestPayload
	_, err := c.CompleteJSON(context.Background(), simpleRequest("score these"), &out)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("error = %v, want ErrBudgetExhausted", err)
	}
}

func TestCompleteJSONRefusesEmptyMessages(t *testing.T) {
	c := newClient(t, newFakeLLM(t, "{}"), newMemBudget(), 100000)
	var out testDigestPayload
	if _, err := c.CompleteJSON(context.Background(), Request{Feature: "x"}, &out); err == nil {
		t.Error("want an error when no messages are supplied")
	}
}

func TestDecodeJSONIntoTypedTargets(t *testing.T) {
	// Real payload shapes from the prompt templates.
	t.Run("outlook", func(t *testing.T) {
		var out struct {
			Base struct {
				Probability float64 `json:"probability"`
				MovePercent float64 `json:"move_percent"`
				Reasoning   string  `json:"reasoning"`
			} `json:"base"`
			KeyRisk string `json:"key_risk"`
		}
		text := "```json\n" + `{"base":{"probability":0.5,"move_percent":0.5,"reasoning":"Sideways."},` +
			`"bull":{"probability":0.25,"move_percent":6},"bear":{"probability":0.25,"move_percent":-6},` +
			`"key_risk":"Earnings"}` + "\n```"
		if err := decodeJSON(text, &out); err != nil {
			t.Fatal(err)
		}
		if out.Base.Probability != 0.5 || out.KeyRisk != "Earnings" {
			t.Errorf("decoded = %+v", out)
		}
	})

	t.Run("morning brief", func(t *testing.T) {
		var out struct {
			Headline string   `json:"headline"`
			Bullets  []string `json:"bullets"`
		}
		if err := decodeJSON(`Here you go:\n{"headline":"Quiet open","bullets":["a","b"]}`, &out); err != nil {
			t.Fatal(err)
		}
		if out.Headline != "Quiet open" || len(out.Bullets) != 2 {
			t.Errorf("decoded = %+v", out)
		}
	})
}
