Review the quantitative forecast for {{.Symbol}} over the next {{.HorizonDays}} trading days, and adjust it only where the news gives a reason the model cannot see.

The model's forecast (the prior):
- Source: {{.Source}}
- Price: {{.Price}}
- Return distribution over {{.HorizonDays}} sessions: 5% {{.Q05}}, 25% {{.Q25}}, median {{.Q50}}, 75% {{.Q75}}, 95% {{.Q95}}
- Probability of rising: {{.PUp}}{{if .PBeat}}; of beating the S&P 500: {{.PBeat}}{{end}}
- Scenarios, each a move beyond {{.Threshold}} (one normal-sized move for this stock): bull {{.Bull}} (average {{.BullMove}}), base {{.Base}} (average {{.BaseMove}}), bear {{.Bear}} (average {{.BearMove}})
- Volatility: {{.Vol}} annualised now against {{.NormalVol}} over the past year
{{if .Earnings}}- Earnings: {{.Earnings}}
{{end}}{{if .Drivers}}- What drives the model's lean: {{.Drivers}}
{{end}}{{if .Regime}}- Market: {{.Regime}}
{{end}}{{if .Record}}- The model's own record out of sample: {{.Record}}
{{end}}
What the model already accounts for, so you must not adjust for it again:
price momentum and reversal at every horizon, volatility and its trend, the
stock's beta, how often it makes large moves, distance from highs and moving
averages, the last earnings surprise and the date and typical size of the
next report, insider buying and selling, its sector's trend, the market's
volatility, VIX and the odds of a stressed market.

Recent news and filings (newest first):
{{if .News}}{{range .News}}- {{.Title}} ({{.Source}}, {{.Age}})
{{end}}{{else}}None collected.
{{end}}
Return JSON in exactly this shape:

{
  "shift_sigma": 0.0,
  "vol_scale": 1.0,
  "evidence": ["the specific news item behind any adjustment"],
  "reasoning": "two or three sentences: what the model sees, and why you kept or changed it",
  "bull_reasoning": "what would have to happen for the bull case",
  "base_reasoning": "what the base case assumes",
  "bear_reasoning": "what would have to happen for the bear case",
  "key_risk": "the single assumption most likely to be wrong"
}

Rules:

- shift_sigma moves the whole distribution by that many standard
  deviations, between -0.5 and 0.5. vol_scale widens (above 1) or narrows
  (below 1) it, between 0.7 and 1.6.
- Leave both at 0 and 1 unless a specific item above gives the model new
  information: a guidance change, a deal, a ruling, a recall, a downgrade
  with a reason, a pending decision with a date. Name it in evidence.
  General market commentary, price-move stories and routine 13F position
  reports are not new information.
- Most outlooks should not adjust. The model is scored, and so is every
  adjustment you make: an adjustment that does not improve on the model
  shows up on the track record.
- Explain the scenarios in terms of this stock's actual situation, not
  generic chart language.
