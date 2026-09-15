package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// jsonInstruction is appended to every structured request.
//
// The endpoint is assumed to support neither function calling nor JSON-schema
// enforcement, so the only lever available is asking clearly and parsing
// defensively.
const jsonInstruction = "\n\nRespond with a single valid JSON value and nothing else. " +
	"Do not wrap it in markdown code fences. Do not add commentary before or after it."

// CompleteJSON performs a completion and decodes the result into target.
//
// If the first attempt does not parse, it retries exactly once, showing the
// model its own output and the parse error. A second failure returns ErrBadJSON
// and the caller degrades — every AI feature here is optional by construction.
func (c *Client) CompleteJSON(ctx context.Context, req Request, target any) (Response, error) {
	if len(req.Messages) == 0 {
		return Response{}, fmt.Errorf("ai: no messages supplied")
	}

	// Append the instruction to the final user message.
	attempt := req
	attempt.Messages = append([]Message(nil), req.Messages...)
	last := len(attempt.Messages) - 1
	attempt.Messages[last] = Message{
		Role:    attempt.Messages[last].Role,
		Content: attempt.Messages[last].Content + jsonInstruction,
	}

	// This also covers ErrTruncated: a cut-off answer would be cut off
	// identically on a retry, for the same cost, so the real cause is
	// surfaced instead of being buried under a parse error.
	resp, err := c.Complete(ctx, attempt)
	if err != nil {
		return resp, err
	}

	parseErr := decodeJSON(resp.Text, target)
	if parseErr == nil {
		return resp, nil
	}

	// A truncated answer is not a malformed one, and the difference decides
	// whether retrying can possibly help.
	//
	// When the model runs out of completion budget mid-object, the text it
	// did produce is well-formed right up to the cut and then simply stops.
	// That parses as garbage, but asking again changes nothing: the second
	// answer is cut at the same place for the same cost, and the operator is
	// left with a "bad JSON" error that points at the model rather than at
	// the token cap that actually caused it. Only the empty-output case was
	// recognised before, which missed every partial response.
	if resp.FinishReason == "length" {
		return resp, fmt.Errorf(
			"%w: response cut off after %d completion tokens (max_tokens=%d); raise the limit or send fewer items per request",
			ErrTruncated, resp.Usage.CompletionTokens, req.MaxTokens)
	}

	c.log.Warn("model returned unparseable JSON; retrying once",
		"feature", req.Feature, "err", parseErr, "output", snippet([]byte(resp.Text)))

	// One repair attempt, with the model's own output and the error in hand.
	repair := attempt
	repair.Messages = append(append([]Message(nil), attempt.Messages...),
		Message{Role: RoleAssistant, Content: resp.Text},
		Message{Role: RoleUser, Content: fmt.Sprintf(
			"That could not be parsed as JSON: %v\n\nReturn only the corrected JSON value, with no fences and no commentary.",
			parseErr)},
	)

	retry, err := c.Complete(ctx, repair)
	if err != nil {
		// The retry itself failed — budget, network, upstream. Report that
		// rather than the parse error, since it is the more actionable one.
		return retry, err
	}
	// Both calls were billed, so report their combined cost.
	retry.Usage = Usage{
		PromptTokens:     resp.Usage.PromptTokens + retry.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens + retry.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens + retry.Usage.TotalTokens,
	}

	if err := decodeJSON(retry.Text, target); err != nil {
		c.log.Error("model failed to produce JSON after a repair attempt",
			"feature", req.Feature, "err", err, "output", snippet([]byte(retry.Text)))
		return retry, fmt.Errorf("%w: %v", ErrBadJSON, err)
	}
	return retry, nil
}

// decodeJSON parses model output that is *meant* to be JSON.
//
// Models wrap JSON in markdown fences, prefix it with "Here is the JSON:", and
// occasionally append a closing remark. Rather than fight that with prompting
// alone, extract the JSON value and parse it.
func decodeJSON(text string, target any) error {
	candidate := extractJSON(text)
	if candidate == "" {
		return fmt.Errorf("no JSON value found in the response")
	}
	if err := json.Unmarshal([]byte(candidate), target); err != nil {
		return err
	}
	return nil
}

// extractJSON pulls the most likely JSON value out of a model response.
func extractJSON(text string) string {
	s := strings.TrimSpace(text)
	if s == "" {
		return ""
	}

	// Markdown fences, with or without a language tag.
	if strings.HasPrefix(s, "```") {
		if end := strings.Index(s[3:], "```"); end >= 0 {
			inner := s[3 : 3+end]
			// Drop a leading language tag such as "json\n".
			if nl := strings.IndexByte(inner, '\n'); nl >= 0 {
				firstLine := strings.TrimSpace(inner[:nl])
				if firstLine == "" || !strings.ContainsAny(firstLine, "{[\"") {
					inner = inner[nl+1:]
				}
			}
			s = strings.TrimSpace(inner)
		}
	}

	if isJSONStart(s) && json.Valid([]byte(s)) {
		return s
	}

	// Otherwise find the outermost balanced object or array.
	if v := balancedSpan(s, '{', '}'); v != "" {
		return v
	}
	return balancedSpan(s, '[', ']')
}

func isJSONStart(s string) bool {
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}

// balancedSpan returns the first balanced open..close run, respecting string
// literals so a brace inside a quoted value does not confuse the scan.
func balancedSpan(s string, open, close byte) string {
	start := strings.IndexByte(s, open)
	if start < 0 {
		return ""
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case escaped:
			escaped = false
		case ch == '\\' && inString:
			escaped = true
		case ch == '"':
			inString = !inString
		case inString:
			// Braces inside a string are literal text.
		case ch == open:
			depth++
		case ch == close:
			depth--
			if depth == 0 {
				candidate := s[start : i+1]
				if json.Valid([]byte(candidate)) {
					return candidate
				}
				return ""
			}
		}
	}
	return ""
}
