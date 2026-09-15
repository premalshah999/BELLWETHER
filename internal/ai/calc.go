package ai

import (
	"context"
	"fmt"
	"math"
	"time"
)

// PositionRequest is the input to the position-sizing calculator.
type PositionRequest struct {
	Question     string  `json:"question"`
	Symbol       string  `json:"symbol,omitempty"`
	AccountValue float64 `json:"account_value"`
	RiskPercent  float64 `json:"risk_percent"`
	EntryPrice   float64 `json:"entry_price"`
	StopPrice    float64 `json:"stop_price"`
	TargetPrice  float64 `json:"target_price,omitempty"`
	Currency     string  `json:"currency,omitempty"`
}

// CalcField is one labelled number in the calculation.
type CalcField struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Raw is the unformatted number, for the UI to use in its own layout.
	Raw float64 `json:"raw"`
}

// PositionResult is the calculator's output.
//
// Every number here is computed in Go. The model's only job is to explain
// them: an LLM doing arithmetic on somebody's position size is a category of
// error this application will not take on.
type PositionResult struct {
	Inputs      []CalcField `json:"inputs"`
	Results     []CalcField `json:"results"`
	Warnings    []string    `json:"warnings"`
	Explanation string      `json:"explanation,omitempty"`
	Status      Status      `json:"status"`
	Model       string      `json:"model,omitempty"`
	GeneratedAt time.Time   `json:"generated_at"`
}

// ComputePosition does the risk arithmetic deterministically.
//
// It is exported and tested independently of the AI layer, because it must
// keep working — and keep being correct — when the model is unreachable, over
// budget, or wrong.
func ComputePosition(req PositionRequest) (PositionResult, error) {
	res := PositionResult{}

	switch {
	case req.AccountValue <= 0:
		return res, fmt.Errorf("account value must be positive, got %g", req.AccountValue)
	case req.RiskPercent <= 0 || req.RiskPercent > 100:
		return res, fmt.Errorf("risk percent must be between 0 and 100, got %g", req.RiskPercent)
	case req.EntryPrice <= 0:
		return res, fmt.Errorf("entry price must be positive, got %g", req.EntryPrice)
	case req.StopPrice <= 0:
		return res, fmt.Errorf("stop price must be positive, got %g", req.StopPrice)
	case req.StopPrice == req.EntryPrice:
		return res, fmt.Errorf("the stop cannot equal the entry price; there would be no risk per share to size against")
	}

	cur := req.Currency
	riskAmount := req.AccountValue * req.RiskPercent / 100
	riskPerShare := math.Abs(req.EntryPrice - req.StopPrice)
	shares := math.Floor(riskAmount / riskPerShare)
	positionValue := shares * req.EntryPrice
	actualRisk := shares * riskPerShare

	res.Inputs = []CalcField{
		{Label: "Account value", Value: money(req.AccountValue, cur), Raw: req.AccountValue},
		{Label: "Risk per trade", Value: fmt.Sprintf("%.2f%%", req.RiskPercent), Raw: req.RiskPercent},
		{Label: "Entry", Value: money(req.EntryPrice, cur), Raw: req.EntryPrice},
		{Label: "Stop", Value: money(req.StopPrice, cur), Raw: req.StopPrice},
	}
	if req.TargetPrice > 0 {
		res.Inputs = append(res.Inputs,
			CalcField{Label: "Target", Value: money(req.TargetPrice, cur), Raw: req.TargetPrice})
	}

	res.Results = []CalcField{
		{Label: "Risk budget", Value: money(riskAmount, cur), Raw: riskAmount},
		{Label: "Risk per share", Value: money(riskPerShare, cur), Raw: riskPerShare},
		{Label: "Shares", Value: fmt.Sprintf("%.0f", shares), Raw: shares},
		{Label: "Position value", Value: money(positionValue, cur), Raw: positionValue},
		{Label: "Actual risk", Value: money(actualRisk, cur), Raw: actualRisk},
	}

	if req.TargetPrice > 0 {
		rewardPerShare := math.Abs(req.TargetPrice - req.EntryPrice)
		rr := rewardPerShare / riskPerShare
		res.Results = append(res.Results,
			CalcField{Label: "Reward per share", Value: money(rewardPerShare, cur), Raw: rewardPerShare},
			CalcField{Label: "Reward:risk", Value: fmt.Sprintf("%.2f : 1", rr), Raw: rr},
			CalcField{Label: "Profit at target", Value: money(shares*rewardPerShare, cur), Raw: shares * rewardPerShare},
		)
		if rr < 1 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"Reward:risk is %.2f:1 — the target is closer than the stop.", rr))
		}
		// A long has its target above entry; a short below. A target on the
		// same side as the stop means the inputs contradict each other.
		longTrade := req.StopPrice < req.EntryPrice
		targetAbove := req.TargetPrice > req.EntryPrice
		if longTrade != targetAbove {
			res.Warnings = append(res.Warnings,
				"The target is on the same side of the entry as the stop; check the direction of this trade.")
		}
	}

	if shares < 1 {
		res.Warnings = append(res.Warnings,
			"The risk budget does not cover a single share at this stop distance.")
	}
	if positionValue > req.AccountValue {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"The position (%s) exceeds the account value (%s) and would need leverage.",
			money(positionValue, cur), money(req.AccountValue, cur)))
	} else if positionValue > req.AccountValue*0.25 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"The position is %.0f%% of the account, which is concentrated even at %.2f%% risk.",
			positionValue/req.AccountValue*100, req.RiskPercent))
	}

	return res, nil
}

// CalcHelper computes the arithmetic and asks the model only to explain it.
func (s *Service) CalcHelper(ctx context.Context, req PositionRequest) (PositionResult, error) {
	res, err := ComputePosition(req)
	if err != nil {
		return res, err
	}
	res.GeneratedAt = s.now()

	// The numbers are already correct and useful without the model. If the AI
	// layer is unavailable, return them with an explanatory status rather than
	// failing the request.
	status, guardErr := s.guard(ctx)
	res.Status = status
	if guardErr != nil {
		return res, nil
	}

	question := req.Question
	if question == "" {
		question = "Explain this position sizing."
	}

	prompt, err := UserPrompt(PromptCalcHelper, map[string]any{
		"Question": question,
		"Inputs":   res.Inputs,
		"Results":  res.Results,
		"Warnings": res.Warnings,
	})
	if err != nil {
		res.Status = StatusUnavailable
		return res, nil
	}

	resp, err := s.client.Complete(ctx, Request{
		Feature:     FeatureCalcHelper,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.2,
		MaxTokens:   2000,
	})
	if err != nil {
		// Same principle: the arithmetic stands on its own.
		res.Status = statusFor(err)
		s.log.Debug("calc helper explanation unavailable", "err", err)
		return res, nil
	}

	res.Status = StatusOK
	res.Model = resp.Model
	res.Explanation = resp.Text
	return res, nil
}

// money formats a value with its currency symbol.
func money(v float64, currency string) string {
	symbol := ""
	switch currency {
	case "INR":
		symbol = "₹"
	case "USD":
		symbol = "$"
	}
	return symbol + humanise(v)
}

// humanise adds thousands separators.
func humanise(v float64) string {
	s := fmt.Sprintf("%.2f", math.Abs(v))
	intPart, frac := s, ""
	if i := len(s) - 3; i > 0 && s[i] == '.' {
		intPart, frac = s[:i], s[i:]
	}

	var out []byte
	for i, ch := range []byte(intPart) {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	res := string(out) + frac
	if v < 0 {
		return "-" + res
	}
	return res
}
