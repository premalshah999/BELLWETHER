// Package ai wraps one OpenAI-compatible chat-completions endpoint and the
// features built on it, under three constraints.
//
// The endpoint is assumed to support neither function calling nor schema
// enforcement, so structured output is asked for in the prompt and parsed
// defensively, with one repair attempt.
//
// Tokens cost money: every call is checked against the budget before it is
// made and recorded after, and a spent budget is reported plainly.
//
// Model output is never data. It is labelled as AI all the way to the UI, and
// nothing is load-bearing on it: alerts fire, charts render and algorithms
// evaluate whether or not the model is reachable.
package ai

import (
	"context"
	"errors"
	"time"
)

// ErrBudgetExhausted is returned when the monthly token budget is spent. It is
// an expected condition, not a fault: features surface "budget reached" and
// carry on without AI.
var ErrBudgetExhausted = errors.New("ai: monthly token budget exhausted")

// ErrNotConfigured is returned when no LLM credentials were supplied.
var ErrNotConfigured = errors.New("ai: no LLM configured")

// ErrBadJSON is returned when the model could not be coaxed into emitting
// parseable JSON, after the repair attempt.
var ErrBadJSON = errors.New("ai: model did not return usable JSON")

// Status describes why an AI feature has no output, for display in the UI.
type Status string

const (
	// StatusOK means the model answered.
	StatusOK Status = "ok"
	// StatusUnconfigured means no credentials were supplied.
	StatusUnconfigured Status = "unconfigured"
	// StatusBudgetReached means the monthly token budget is spent.
	StatusBudgetReached Status = "budget_reached"
	// StatusUnavailable means the model was asked and could not answer.
	StatusUnavailable Status = "unavailable"
)

// Role values for chat messages.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one turn in a chat completion.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage is the token accounting for one call.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CacheHitTokens and CacheMissTokens split PromptTokens by whether
	// DeepSeek served them from its context cache. A hit costs a fiftieth of
	// a miss, so without the split a call cannot be priced accurately -- see
	// CostUSD, which treats an unreported split as all misses.
	CacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	CacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
}

// UsageRecord is one billed call, stored for the budget meter and the
// settings page.
type UsageRecord struct {
	// Period is the budget bucket, a UTC month such as "2026-08".
	Period  string
	Feature string
	Model   string
	Usage   Usage
	At      time.Time
	// CostUSD is what the call cost at the rate in force when it was made.
	CostUSD float64
}

// BudgetStore persists token consumption. Declared here, at the point of use,
// so this package does not depend on the storage package.
type BudgetStore interface {
	// LLMTokensUsed reports total tokens consumed in a period.
	LLMTokensUsed(ctx context.Context, period string) (int, error)
	// RecordLLMUsage stores one call's consumption.
	RecordLLMUsage(ctx context.Context, rec UsageRecord) error
	// LLMUsageByFeature breaks a period down for the settings page.
	LLMUsageByFeature(ctx context.Context, period string) (map[string]int, error)
	// LLMSpendSince sums the USD cost of calls recorded at or after a moment.
	// The daily cap asks it for spend since UTC midnight.
	LLMSpendSince(ctx context.Context, since time.Time) (float64, error)
}

// BudgetState is what the UI renders on the budget meter.
// ErrDailyCapReached is returned when a call would take the day's spend past
// the configured USD cap. It is a limit the operator set, not a fault, and is
// reported to health as skipped rather than failed.
var ErrDailyCapReached = errors.New("ai: daily spend cap reached")

type BudgetState struct {
	Period    string         `json:"period"`
	Used      int            `json:"used"`
	Limit     int            `json:"limit"`
	Exhausted bool           `json:"exhausted"`
	ByFeature map[string]int `json:"by_feature,omitempty"`
}

// Remaining is how many tokens are left in the period.
func (b BudgetState) Remaining() int {
	if b.Limit <= 0 {
		return 0
	}
	if b.Used >= b.Limit {
		return 0
	}
	return b.Limit - b.Used
}

// Feature names, used for usage attribution and for the settings breakdown.
const (
	FeatureMorningBrief    = "morning_brief"
	FeatureNewsDigest      = "news_digest"
	FeatureExplainMove     = "explain_move"
	FeatureOutlook         = "outlook"
	FeatureCalcHelper      = "calc_helper"
	FeatureAlertContext    = "alert_context"
	FeatureEventBrief      = "event_brief"
	FeatureEventClassify   = "event_classify"
	FeatureDeepResearch    = "deep_research"
	FeatureResearchRewrite = "research_rewrite"
	FeatureSymbolDebrief   = "symbol_debrief"
)
