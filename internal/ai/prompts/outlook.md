Produce a scenario outlook for {{.Symbol}} over the next {{.HorizonDays}} trading days.

Current state:
- Price: {{.Price}}
- {{.HorizonDays}}-day realised volatility: {{.Volatility}}
- Trend: {{.TrendNote}}
- RSI(14): {{.RSI}}
- Distance from 52-week high: {{.FromHigh}}
- Distance from 52-week low: {{.FromLow}}

{{if .News}}Recent news:
{{range .News}}- {{.Title}} ({{.Source}}, {{.Age}})
{{end}}{{else}}No recent news was collected for this symbol.
{{end}}

Return JSON in exactly this shape:

{
  "base": {"probability": 0.0, "move_percent": 0.0, "reasoning": "one or two sentences"},
  "bull": {"probability": 0.0, "move_percent": 0.0, "reasoning": "one or two sentences"},
  "bear": {"probability": 0.0, "move_percent": 0.0, "reasoning": "one or two sentences"},
  "key_risk": "the single assumption most likely to be wrong"
}

Rules:

- The three probabilities must sum to 1.0.
- move_percent is the percentage change from the current price that defines
  each scenario over the horizon. Bull must be above base; bear must be below.
- Be calibrated, not dramatic. Every outlook you produce is recorded and scored
  against what actually happened, and a habit of confident extremes will show
  up as poor calibration. If the honest answer is "mostly unchanged with wide
  error bars", say that.
- This is a scenario sketch, not a prediction and not advice.
