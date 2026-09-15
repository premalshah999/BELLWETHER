package ai

import (
	"context"
	"strings"

	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// AlertContext produces the three-sentence brief attached to an alert.
//
// It implements alerts.AIContextProvider. Its contract is the important part:
// it never returns an error. An alert must fire whether or not the model is
// reachable, so every failure becomes an empty string and a status that the
// pipeline renders as "AI context unavailable".
func (s *Service) AlertContext(ctx context.Context, a *alerts.Alert) (string, string) {
	status, err := s.guard(ctx)
	if err != nil {
		return "", string(status)
	}

	sym, err := marketdata.ParseSymbol(a.Symbol)
	if err != nil {
		s.log.Warn("alert has an unparseable symbol", "symbol", a.Symbol, "err", err)
		return "", string(StatusUnavailable)
	}

	prompt, err := UserPrompt(PromptAlertContext, map[string]any{
		"AlgorithmName": a.AlgorithmName,
		"Symbol":        a.Symbol,
		"Summary":       a.Summary,
		"Price":         formatFloat(a.Price),
		"News":          s.recentNewsFor(ctx, sym, 4),
	})
	if err != nil {
		s.log.Warn("could not render the alert context prompt", "err", err)
		return "", string(StatusUnavailable)
	}

	resp, err := s.client.Complete(ctx, Request{
		Feature:     FeatureAlertContext,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.3,
		// Three sentences of answer, plus headroom for a reasoning model to
		// think first. The cap still stops a rambling model eating the
		// month's budget one alert at a time.
		MaxTokens: 1500,
	})
	if err != nil {
		s.log.Debug("alert context unavailable", "symbol", a.Symbol, "err", err)
		return "", string(statusFor(err))
	}

	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return "", string(StatusUnavailable)
	}
	return text, string(StatusOK)
}

var _ alerts.AIContextProvider = (*Service)(nil)
