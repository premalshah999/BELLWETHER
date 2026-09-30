package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/paper"
)

// TradePlan asks the text model what a paper-trading agent should do now.
// It satisfies paper.Strategy. The risk limits bind whatever it answers.
func (s *Service) TradePlan(ctx context.Context, a paper.Agent, snap paper.Snapshot) (string, []paper.Intent, string, error) {
	if _, err := s.guard(ctx); err != nil {
		return "", nil, "", err
	}
	data := map[string]any{
		"At":             snap.At.In(marketdata.Market).Format("Mon 2 Jan 15:04"),
		"SessionOpen":    snap.SessionOpen,
		"MinutesToClose": snap.MinutesToClose,
		"Intraday":       snap.Intraday,
		"Equity":         fmt.Sprintf("%.2f", snap.Equity),
		"Cash":           fmt.Sprintf("%.2f", snap.Cash),
		"BuyingPower":    fmt.Sprintf("%.2f", snap.BuyingPower),
		"DayChangePct":   snap.DayChangePct,
		"DrawdownPct":    snap.DrawdownPct,
		"MaxPositions":   snap.MaxPositions,
		"MaxPositionPct": snap.Limits.MaxPositionPct,
		"StopLossPct":    snap.Limits.StopLossPct,
		"TakeProfitPct":  snap.Limits.TakeProfitPct,
		"Holdings":       snap.Holdings,
		"Candidates":     snap.Candidates,
		"Instructions":   strings.TrimSpace(snap.Instructions),
	}
	prompt, err := UserPrompt(PromptTradeAgent, data)
	if err != nil {
		return "", nil, "", err
	}
	var plan struct {
		Summary string         `json:"summary"`
		Intents []paper.Intent `json:"intents"`
	}
	resp, err := s.client.CompleteJSON(ctx, Request{
		Feature:     FeatureTradeAgent,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.3,
		MaxTokens:   2500,
	}, &plan)
	if err != nil {
		return "", nil, "", err
	}
	// Only what the model was shown may be traded.
	allowed := map[string]bool{}
	for _, h := range snap.Holdings {
		allowed[h.Symbol] = true
	}
	for _, c := range snap.Candidates {
		allowed[c.Symbol] = true
	}
	kept := plan.Intents[:0]
	for _, in := range plan.Intents {
		in.Symbol = strings.ToUpper(strings.TrimSpace(in.Symbol))
		if allowed[in.Symbol] && (in.Side == paper.Buy || in.Side == paper.Sell) {
			kept = append(kept, in)
		}
	}
	return plan.Summary, kept, resp.Model, nil
}

// TradeStrategy adapts the service to paper.Strategy.
type TradeStrategy struct{ Service *Service }

// Decide implements paper.Strategy.
func (t TradeStrategy) Decide(ctx context.Context, a paper.Agent, snap paper.Snapshot) (string, []paper.Intent, string, error) {
	return t.Service.TradePlan(ctx, a, snap)
}
