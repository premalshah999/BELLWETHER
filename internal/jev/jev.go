// Package jev is a client for TypeSafe AI's System One API and its Jev model.
//
// Jev is not a text model. It evaluates typed questions against a piece of
// state and returns structured answers directly -- a choice with a probability
// for every option, a score on a rubric, or a truth value -- with no text
// generation and no parsing on our side. That makes it the right tool for the
// decisions this app makes thousands of times a day (what kind of event is
// this, how much does it matter, is it an event at all) and the wrong tool for
// anything that has to be written for a person to read. Prose stays with the
// text model; see internal/ai.
//
// Wire format, authentication, limits and error codes follow TypeSafe's own
// documentation at https://docs.typesafe.ai -- in particular the quickstart
// and the API reference. Where the documentation is silent (the exact error
// body, a request timeout), this package says so rather than guessing.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// DefaultBaseURL is the System One endpoint.
const DefaultBaseURL = "https://api.typesafe.ai/v1/systemone"

// DefaultModel is the alias TypeSafe recommends. It follows their latest Jev
// release, which is what a caller wants unless it is pinning for
// reproducibility; the response reports the concrete version that answered.
const DefaultModel = "jev-latest"

// Limits from the API reference. Validated locally so a malformed question
// fails before it spends a request rather than as a 422 after.
const (
	maxChoiceOptions = 255 // "a maximum of 255 options per Choice"
	minScoreLevels   = 2   // "A Score should have at least two levels"
	maxScoreLevels   = 10  // "the API accepts up to 10"
)

// ErrNotConfigured is returned when no API key is set. Callers treat it as
// "Jev is unavailable", not as a failure: the app runs without it.
var ErrNotConfigured = errors.New("jev: no API key configured")

// ErrUnauthorized is a 401. It is never retried -- a rejected key does not
// start working on the second attempt, and retrying only spends time.
var ErrUnauthorized = errors.New("jev: API key rejected")

// ---------------------------------------------------------------------------
// Questions
// ---------------------------------------------------------------------------

// Question is one of Choice, Score or Noul.
type Question interface {
	wire() (map[string]any, error)
}

// Choice picks one option from a set. Criteria maps each option's key to a
// description of when it applies; the key is what comes back.
type Choice struct {
	Instructions string
	Criteria     map[string]string
}

func (q Choice) wire() (map[string]any, error) {
	if strings.TrimSpace(q.Instructions) == "" {
		return nil, errors.New("choice needs instructions")
	}
	if len(q.Criteria) < 2 {
		return nil, fmt.Errorf("choice needs at least two options, has %d", len(q.Criteria))
	}
	if len(q.Criteria) > maxChoiceOptions {
		return nil, fmt.Errorf("choice has %d options; the API accepts at most %d",
			len(q.Criteria), maxChoiceOptions)
	}
	return map[string]any{"type": "choice", "instructions": q.Instructions, "criteria": q.Criteria}, nil
}

// Score places the state on an ordered rubric. Criteria are the levels, lowest
// first; the answer is an index into them.
type Score struct {
	Instructions string
	Criteria     []string
}

func (q Score) wire() (map[string]any, error) {
	if strings.TrimSpace(q.Instructions) == "" {
		return nil, errors.New("score needs instructions")
	}
	if n := len(q.Criteria); n < minScoreLevels || n > maxScoreLevels {
		return nil, fmt.Errorf("score has %d levels; the API accepts %d to %d",
			n, minScoreLevels, maxScoreLevels)
	}
	return map[string]any{"type": "score", "instructions": q.Instructions, "criteria": q.Criteria}, nil
}

// Noul asks whether a statement about the state is true, answered from 0 to 1.
//
// It carries no confidence field -- the value is itself the probability -- so
// a caller gating on it thresholds the value directly.
type Noul struct {
	Instructions string
}

func (q Noul) wire() (map[string]any, error) {
	if strings.TrimSpace(q.Instructions) == "" {
		return nil, errors.New("noul needs instructions")
	}
	return map[string]any{"type": "noul", "instructions": q.Instructions}, nil
}

// ---------------------------------------------------------------------------
// Answers
// ---------------------------------------------------------------------------

// ChoiceAnswer is the picked option and the distribution it was picked from.
type ChoiceAnswer struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// ScoreAnswer is a level on a rubric. Score is the index of the level, as a
// float because the API returns it as one.
type ScoreAnswer struct {
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// Level is the chosen rubric index as an integer.
func (a ScoreAnswer) Level() int { return int(math.Round(a.Score)) }

// Expected maps each rubric level to a value and returns the probability-
// weighted average.
//
// This is the reason to prefer Jev's distribution over its single pick. When
// the probability is split between "moves the stock" and "rewrites the
// investment case", the pick throws the split away and the expectation keeps
// it -- a smooth, calibrated number instead of a coarse bucket. Falls back to
// the pick when the response carries no distribution.
func (a ScoreAnswer) Expected(values []float64) float64 {
	var sum, mass float64
	for k, p := range a.Probabilities {
		var idx int
		if _, err := fmt.Sscanf(k, "%d", &idx); err != nil || idx < 0 || idx >= len(values) {
			continue
		}
		sum += p * values[idx]
		mass += p
	}
	if mass <= 0 {
		if l := a.Level(); l >= 0 && l < len(values) {
			return values[l]
		}
		return 0
	}
	return sum / mass
}

// NoulAnswer is a truth value from 0 to 1.
type NoulAnswer struct {
	Noul float64 `json:"noul"`
}

// Usage is what a request consumed. Output tokens are reported but are not
// billed under TypeSafe's published pricing at the time of writing.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response holds every answer, keyed by the name the question was asked under.
type Response struct {
	Model string
	Usage Usage
	raw   map[string]json.RawMessage
}

// Choice returns a choice answer. ok is false when the name was not asked, or
// was asked as a different type.
func (r Response) Choice(name string) (ChoiceAnswer, bool) {
	var a struct {
		Type string `json:"type"`
		ChoiceAnswer
	}
	if !r.decode(name, &a) || a.Type != "choice" {
		return ChoiceAnswer{}, false
	}
	return a.ChoiceAnswer, true
}

// Score returns a score answer.
func (r Response) Score(name string) (ScoreAnswer, bool) {
	var a struct {
		Type string `json:"type"`
		ScoreAnswer
	}
	if !r.decode(name, &a) || a.Type != "score" {
		return ScoreAnswer{}, false
	}
	return a.ScoreAnswer, true
}

// Noul returns a noul answer.
func (r Response) Noul(name string) (NoulAnswer, bool) {
	var a struct {
		Type string `json:"type"`
		NoulAnswer
	}
	if !r.decode(name, &a) || a.Type != "noul" {
		return NoulAnswer{}, false
	}
	return a.NoulAnswer, true
}

func (r Response) decode(name string, into any) bool {
	raw, ok := r.raw[name]
	if !ok {
		return false
	}
	return json.Unmarshal(raw, into) == nil
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Outcome is one attempt, reported to whoever tracks dependency health.
type Outcome struct {
	OK      bool
	Skipped bool
	Err     error
}

// OutcomeSink receives every attempt. It must not block.
type OutcomeSink func(ctx context.Context, o Outcome)

// SpendRecorder stores what a request cost, so spend can be capped and shown.
type SpendRecorder interface {
	RecordJevUsage(ctx context.Context, feature string, usage Usage, costUSD float64, at time.Time) error
}

// Client calls the System One endpoint.
type Client struct {
	baseURL  string
	apiKey   string
	model    string
	pricePer float64 // USD per 1M input tokens
	http     *http.Client
	sink     OutcomeSink
	spend    SpendRecorder
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	log      *slog.Logger
	attempts int
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client somewhere other than production. Tests use it.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithModel pins a model rather than following jev-latest.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

// WithHTTPClient supplies the transport.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithOutcomeSink reports each attempt to the health tracker.
func WithOutcomeSink(s OutcomeSink) Option { return func(c *Client) { c.sink = s } }

// WithSpendRecorder stores each request's cost.
func WithSpendRecorder(s SpendRecorder) Option { return func(c *Client) { c.spend = s } }

// WithInputPrice sets the price per million input tokens, in USD.
func WithInputPrice(usdPerMillion float64) Option {
	return func(c *Client) { c.pricePer = usdPerMillion }
}

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// WithClock replaces the time source. Tests use it.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// withSleep replaces the backoff wait, so retry tests do not actually wait.
func withSleep(f func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = f }
}

// New builds a client. An empty key produces a client whose calls return
// ErrNotConfigured, so the app can start and run without Jev.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		apiKey:  strings.TrimSpace(apiKey),
		model:   DefaultModel,
		// 70-500ms end to end per TypeSafe's own figures. Thirty seconds is
		// generous for that on purpose: the timeout exists to stop a hung
		// connection, not to judge a slow one. The docs do not specify a
		// server-side timeout, so this does not assume one.
		http:     &http.Client{Timeout: 30 * time.Second},
		now:      func() time.Time { return time.Now().UTC() },
		log:      slog.Default(),
		attempts: 4,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Configured reports whether calls can be made.
func (c *Client) Configured() bool { return c != nil && c.apiKey != "" }

// Model is the model alias this client asks for.
func (c *Client) Model() string { return c.model }

// Request is one evaluation: questions asked against a single piece of state.
type Request struct {
	// Feature attributes the spend, e.g. "event_classify".
	Feature string
	// State is what the questions are asked about.
	State string
	// Questions are keyed by the name the answer comes back under. They are
	// evaluated in parallel and in isolation, so one question cannot see
	// another's answer -- a dependent decision needs a second request.
	Questions map[string]Question
}

// Ask evaluates the questions and reports the attempt.
func (c *Client) Ask(ctx context.Context, req Request) (Response, error) {
	if !c.Configured() {
		c.report(ctx, Outcome{Skipped: true, Err: ErrNotConfigured})
		return Response{}, ErrNotConfigured
	}
	body, err := c.encode(req)
	if err != nil {
		// Our bug, not the provider's: never reported against its health.
		return Response{}, err
	}

	resp, err := c.send(ctx, body)
	switch {
	case err == nil:
		c.report(ctx, Outcome{OK: true})
		c.recordSpend(ctx, req.Feature, resp.Usage)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		c.report(ctx, Outcome{Skipped: true, Err: err})
	default:
		c.report(ctx, Outcome{Err: err})
	}
	return resp, err
}

func (c *Client) encode(req Request) ([]byte, error) {
	if strings.TrimSpace(req.State) == "" {
		return nil, errors.New("jev: empty state")
	}
	if len(req.Questions) == 0 {
		return nil, errors.New("jev: no questions")
	}
	questions := make(map[string]any, len(req.Questions))
	for name, q := range req.Questions {
		w, err := q.wire()
		if err != nil {
			return nil, fmt.Errorf("jev: question %q: %w", name, err)
		}
		questions[name] = w
	}
	return json.Marshal(map[string]any{
		"state":     req.State,
		"model":     c.model,
		"questions": questions,
	})
}

// send posts, retrying the two statuses TypeSafe says to retry.
//
// 429 (rate limited) and 529 (overloaded) are retried with exponential backoff,
// as the API reference directs: "retry the request with exponential backoff
// instead of retrying immediately". Nothing else is. A 401 is a rejected key
// and a 422 is a malformed request, and neither changes on the next attempt.
func (c *Client) send(ctx context.Context, body []byte) (Response, error) {
	var lastErr error
	for attempt := 0; attempt < c.attempts; attempt++ {
		if attempt > 0 {
			// 500ms, 1s, 2s: short, because the model answers in well under a
			// second and a rate limit on a fast endpoint clears quickly.
			wait := time.Duration(500<<uint(attempt-1)) * time.Millisecond
			if err := c.sleep(ctx, wait); err != nil {
				return Response{}, err
			}
		}
		resp, retry, err := c.post(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retry {
			return Response{}, err
		}
		c.log.Debug("jev: retrying", "attempt", attempt+1, "err", err)
	}
	return Response{}, fmt.Errorf("jev: gave up after %d attempts: %w", c.attempts, lastErr)
}

func (c *Client) post(ctx context.Context, body []byte) (Response, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return Response{}, false, fmt.Errorf("jev: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// A transport error is worth one more try; a context error is not.
		if ctx.Err() != nil {
			return Response{}, false, ctx.Err()
		}
		return Response{}, true, fmt.Errorf("jev: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Response{}, true, fmt.Errorf("jev: read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return Response{}, false, fmt.Errorf("%w: %s", ErrUnauthorized, snippet(raw))
	case http.StatusTooManyRequests, 529:
		return Response{}, true, fmt.Errorf("jev: http %d: %s", resp.StatusCode, snippet(raw))
	default:
		// 422 and anything undocumented. The API reference says errors carry
		// "a JSON body describing what went wrong" without specifying its
		// shape, so the body is passed through rather than parsed.
		return Response{}, false, fmt.Errorf("jev: http %d: %s", resp.StatusCode, snippet(raw))
	}

	var out struct {
		Model   string                     `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   Usage                      `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, false, fmt.Errorf("jev: unreadable response: %w", err)
	}
	return Response{Model: out.Model, Usage: out.Usage, raw: out.Answers}, false, nil
}

// Cost is what a request's usage costs, in USD, at this client's price.
func (c *Client) Cost(u Usage) float64 {
	return float64(u.InputTokens) * c.pricePer / 1e6
}

func (c *Client) recordSpend(ctx context.Context, feature string, u Usage) {
	if c.spend == nil {
		return
	}
	// Not cancelled with the request: the tokens are spent either way.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.spend.RecordJevUsage(rctx, feature, u, c.Cost(u), c.now()); err != nil {
		c.log.Warn("jev: could not record usage", "feature", feature, "err", err)
	}
}

func (c *Client) report(ctx context.Context, o Outcome) {
	if c != nil && c.sink != nil {
		c.sink(ctx, o)
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Keys returns a choice's option keys in a stable order, for callers that
// need to present or iterate them deterministically.
func (q Choice) Keys() []string {
	keys := make([]string, 0, len(q.Criteria))
	for k := range q.Criteria {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
