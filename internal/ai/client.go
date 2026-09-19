package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// estimatedTokensPerChar is the ratio used to pre-check a request against the
// remaining budget. Real tokenisation varies by model and language; this is a
// deliberate over-estimate so a budget is never overshot by a large margin.
const estimatedTokensPerChar = 0.3

// Client speaks the OpenAI chat-completions protocol.
type Client struct {
	baseURL      string
	apiKey       string
	model        string
	cheapModel   string
	monthlyLimit int
	extraBody    map[string]any
	budget       BudgetStore
	http         *http.Client
	log          *slog.Logger
	now          func() time.Time
	sink         OutcomeSink
}

// Outcome is the result of one attempt to reach the model, reported to
// whoever is tracking dependency health.
type Outcome struct {
	// OK is true when the provider answered usably.
	OK bool
	// Skipped marks an expected non-answer -- no credentials, or a budget
	// deliberately spent. These are limitations the operator set, not faults,
	// and must not drive the dependency down.
	Skipped bool
	Err     error
}

// OutcomeSink receives every attempt. It must not block.
type OutcomeSink func(ctx context.Context, o Outcome)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// WithClock replaces the time source, used by tests to cross a month boundary.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithOutcomeSink registers a receiver for per-attempt outcomes.
//
// Status() deliberately answers without making a call, so it cannot tell a
// working key from a rejected one. This is the port through which a real
// round trip -- including its failure -- becomes visible to the health
// tracker, exactly as the market data router reports its providers.
func WithOutcomeSink(s OutcomeSink) Option { return func(c *Client) { c.sink = s } }

// Config holds the LLM connection settings.
type Config struct {
	BaseURL      string
	APIKey       string
	Model        string
	CheapModel   string
	MonthlyLimit int
	// ExtraBody is merged into every request body. It is the escape hatch for
	// vendor parameters that are not part of the OpenAI schema — for example
	// Xiaomi MiMo's {"thinking":{"type":"disabled"}}, which turns off the
	// chain of thought and makes structured output both faster and cheaper.
	ExtraBody map[string]any
}

// New builds an LLM client. Missing credentials produce an unconfigured
// client, whose calls return ErrNotConfigured rather than failing at startup.
func New(cfg Config, budget BudgetStore, opts ...Option) *Client {
	cheap := cfg.CheapModel
	if cheap == "" {
		cheap = cfg.Model
	}
	c := &Client{
		baseURL:      strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:       cfg.APIKey,
		model:        cfg.Model,
		cheapModel:   cheap,
		monthlyLimit: cfg.MonthlyLimit,
		extraBody:    cfg.ExtraBody,
		budget:       budget,
		// Generous: a long brief on a slow endpoint legitimately takes a while.
		// Generous, because the ceiling here is a reasoning model writing a
		// long structured answer, not a normal API call. At a 12,000-token
		// completion budget a research synthesis was exceeding two minutes
		// and being cut off mid-response — which surfaced as "context
		// deadline exceeded" and degraded the whole turn to sources-only.
		//
		// Per-request deadlines still apply on top of this; what this bound
		// prevents is a hung connection, not a slow one.
		http: &http.Client{Timeout: 8 * time.Minute},
		log:  slog.Default(),
		now:  func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Configured reports whether the client can make calls.
func (c *Client) Configured() bool {
	return c.baseURL != "" && c.model != "" && c.apiKey != ""
}

// Model is the default model name.
func (c *Client) Model() string { return c.model }

// CheapModel is the model used for high-volume work such as news scoring.
func (c *Client) CheapModel() string { return c.cheapModel }

// Period is the budget bucket for now: a UTC month.
func (c *Client) Period() string { return c.now().UTC().Format("2006-01") }

// Budget reports current consumption for the UI.
func (c *Client) Budget(ctx context.Context) (BudgetState, error) {
	state := BudgetState{Period: c.Period(), Limit: c.monthlyLimit}
	if c.budget == nil {
		return state, nil
	}
	used, err := c.budget.LLMTokensUsed(ctx, state.Period)
	if err != nil {
		return state, err
	}
	state.Used = used
	state.Exhausted = c.monthlyLimit > 0 && used >= c.monthlyLimit

	byFeature, err := c.budget.LLMUsageByFeature(ctx, state.Period)
	if err == nil {
		state.ByFeature = byFeature
	}
	return state, nil
}

// Status reports why AI output may be unavailable, without making a call.
func (c *Client) Status(ctx context.Context) Status {
	if !c.Configured() {
		return StatusUnconfigured
	}
	state, err := c.Budget(ctx)
	if err != nil {
		return StatusUnavailable
	}
	if state.Exhausted {
		return StatusBudgetReached
	}
	return StatusOK
}

// Request is one completion to perform.
type Request struct {
	// Feature attributes the token spend, e.g. FeatureMorningBrief.
	Feature string
	// Cheap routes to the cheaper model tier.
	Cheap       bool
	Messages    []Message
	Temperature float64
	MaxTokens   int
}

// ErrTruncated is returned when the model hit its token ceiling before
// finishing. On a reasoning model this is the common failure: the model spends
// its allowance thinking and the answer itself never arrives. Retrying the
// same request unchanged would truncate identically, so callers must not.
var ErrTruncated = errors.New("ai: the model ran out of tokens before finishing")

// Response is a completed call.
type Response struct {
	Text  string
	Model string
	Usage Usage
	// FinishReason is the upstream's reason for stopping: "stop", "length",
	// or a vendor-specific value. "length" means the answer was cut off.
	FinishReason string
}

// wire types for the OpenAI chat-completions protocol.
type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Complete performs one chat completion, enforcing the budget around it.
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	if !c.Configured() {
		c.report(ctx, Outcome{Skipped: true, Err: ErrNotConfigured})
		return Response{}, ErrNotConfigured
	}
	if err := c.checkBudget(ctx, req); err != nil {
		c.report(ctx, Outcome{Skipped: true, Err: err})
		return Response{}, err
	}

	out, err := c.complete(ctx, req)
	switch {
	case err == nil, errors.Is(err, ErrTruncated):
		// A truncated answer is still a completed round trip: the provider
		// answered, and the ceiling that cut it off was ours. Calling that a
		// fault would take the dependency red over a max_tokens we chose.
		c.report(ctx, Outcome{OK: true})
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The caller gave up waiting. That says nothing about the provider,
		// so it must not count against it.
		c.report(ctx, Outcome{Skipped: true, Err: err})
	default:
		c.report(ctx, Outcome{Err: err})
	}
	return out, err
}

// report forwards one attempt to the sink, when a sink is registered.
func (c *Client) report(ctx context.Context, o Outcome) {
	if c.sink != nil {
		c.sink(ctx, o)
	}
}

// complete is the round trip itself. Every path out of it is an outcome the
// caller above classifies; it does no reporting of its own.
func (c *Client) complete(ctx context.Context, req Request) (Response, error) {
	model := c.model
	if req.Cheap {
		model = c.cheapModel
	}

	body, err := c.encodeRequest(chatRequest{
		Model:       model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      false,
	})
	if err != nil {
		return Response{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("ai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("ai: request failed: %w", redact(err, c.apiKey))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("ai: read response: %w", err)
	}

	var parsed chatResponse
	if jsonErr := json.Unmarshal(raw, &parsed); jsonErr != nil {
		if resp.StatusCode >= 400 {
			return Response{}, fmt.Errorf("ai: http %d: %s", resp.StatusCode, snippet(raw))
		}
		return Response{}, fmt.Errorf("ai: unreadable response: %w", jsonErr)
	}
	if parsed.Error != nil {
		return Response{}, fmt.Errorf("ai: upstream error: %s", parsed.Error.Message)
	}
	if resp.StatusCode >= 400 {
		return Response{}, fmt.Errorf("ai: http %d: %s", resp.StatusCode, snippet(raw))
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("ai: response contained no choices")
	}

	choice := parsed.Choices[0]
	out := Response{
		Text:         choice.Message.Content,
		Model:        firstNonEmpty(parsed.Model, model),
		FinishReason: choice.FinishReason,
		Usage: Usage{
			PromptTokens:     parsed.Usage.PromptTokens,
			CompletionTokens: parsed.Usage.CompletionTokens,
			TotalTokens:      parsed.Usage.TotalTokens,
		},
	}
	// Some OpenAI-compatible servers omit the usage block. Estimating is
	// better than recording zero, which would let a budget run unbounded.
	if out.Usage.TotalTokens == 0 {
		out.Usage.TotalTokens = estimateTokens(req.Messages) + estimateChars(out.Text)
		c.log.Debug("upstream reported no token usage; estimating",
			"feature", req.Feature, "estimated", out.Usage.TotalTokens)
	}

	c.record(ctx, req.Feature, out)

	// A reasoning model emits its chain of thought before the answer. When the
	// token ceiling is too low the thinking consumes it all and content comes
	// back empty — with a perfectly successful HTTP 200. Reporting that as a
	// parse failure would send the caller into a repair retry that truncates
	// exactly the same way, at exactly the same cost.
	if strings.TrimSpace(out.Text) == "" && out.FinishReason == "length" {
		return out, fmt.Errorf("%w: %d completion tokens spent, max_tokens=%d; raise the limit for this feature",
			ErrTruncated, out.Usage.CompletionTokens, req.MaxTokens)
	}
	return out, nil
}

// checkBudget refuses a call that the remaining allowance cannot cover.
func (c *Client) checkBudget(ctx context.Context, req Request) error {
	if c.budget == nil || c.monthlyLimit <= 0 {
		if c.monthlyLimit == 0 {
			// An explicit zero budget disables AI entirely, which is a
			// legitimate way to turn the feature off.
			return ErrBudgetExhausted
		}
		return nil
	}
	used, err := c.budget.LLMTokensUsed(ctx, c.Period())
	if err != nil {
		// Fail closed. If we cannot tell how much has been spent, spending
		// more risks an unbounded bill.
		c.log.Error("could not read the token budget; refusing the call", "err", err)
		return fmt.Errorf("ai: budget unreadable: %w", err)
	}
	if used >= c.monthlyLimit {
		return ErrBudgetExhausted
	}
	// Refuse a request whose prompt alone would overshoot.
	if estimate := estimateTokens(req.Messages); used+estimate > c.monthlyLimit {
		return fmt.Errorf("%w: %d used of %d, and this request needs about %d",
			ErrBudgetExhausted, used, c.monthlyLimit, estimate)
	}
	return nil
}

func (c *Client) record(ctx context.Context, feature string, out Response) {
	if c.budget == nil {
		return
	}
	// Recording must not be cancelled along with the request it describes, or
	// a user navigating away would lose the accounting for tokens already
	// spent.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	err := c.budget.RecordLLMUsage(recordCtx, UsageRecord{
		Period:  c.Period(),
		Feature: feature,
		Model:   out.Model,
		Usage:   out.Usage,
		At:      c.now(),
	})
	if err != nil {
		c.log.Error("could not record token usage", "feature", feature, "err", err)
	}
}

// encodeRequest marshals the request, merging any vendor-specific extras.
func (c *Client) encodeRequest(req chatRequest) ([]byte, error) {
	if len(c.extraBody) == 0 {
		body, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("ai: encode request: %w", err)
		}
		return body, nil
	}

	// Round-trip through a map so extras can add fields the struct does not
	// model. Extras never overwrite a field the caller set explicitly.
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ai: encode request: %w", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil, fmt.Errorf("ai: encode request: %w", err)
	}
	for k, v := range c.extraBody {
		if _, taken := merged[k]; !taken {
			merged[k] = v
		}
	}
	body, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("ai: encode request: %w", err)
	}
	return body, nil
}

func estimateTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateChars(m.Content)
	}
	return total
}

func estimateChars(s string) int {
	n := int(float64(len(s)) * estimatedTokensPerChar)
	if n < 1 && len(s) > 0 {
		return 1
	}
	return n
}

func snippet(b []byte) string {
	const max = 300
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// redact removes the API key from an error, since transport errors can carry
// request details.
func redact(err error, key string) error {
	if key == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), key, "[redacted]")

	// Keep the two context sentinels reachable through errors.Is.
	//
	// Flattening to a string error scrubbed the key but also erased the
	// error's identity, so a caller who gave up mid-request was
	// indistinguishable from an upstream that had broken -- which is exactly
	// the distinction the health sink has to draw. Only the bare sentinel is
	// exposed, and it carries no message of its own, so nothing unredacted
	// becomes reachable by unwrapping.
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return &redactedError{msg: msg, cause: sentinel}
		}
	}
	return fmt.Errorf("%s", msg)
}

// redactedError carries a scrubbed message while leaving one known sentinel
// reachable underneath it.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }
