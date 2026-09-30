package algo

// Template is a starting point an operator can copy and edit. Templates are
// seeded into an empty installation so the builder has something concrete to
// show rather than a blank JSON editor.
type Template struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Rationale explains what the rule is actually looking for, so an
	// operator can judge whether it suits them rather than trusting the name.
	Rationale string     `json:"rationale"`
	Algorithm *Algorithm `json:"algorithm"`
}

// defaultSymbols seed each template from the watchlist's own seed list, so a
// freshly seeded algorithm evaluates against data the operator can see.
var defaultSymbols = []string{"AAPL", "MSFT"}

// Templates returns the prebuilt algorithms. Each one is validated at startup
// by TestTemplatesAreValid, so a broken template cannot ship.
func Templates() []Template {
	f := func(v float64) *float64 { return &v }

	return []Template{
		{
			Key:         "oversold_bounce",
			Title:       "Oversold bounce watch",
			Description: "RSI oversold while the long-term trend is still up, confirmed by volume.",
			Rationale: "Looks for a pullback inside an uptrend rather than a falling knife: " +
				"RSI below 35 says short-term selling, the close above its 200-bar average says " +
				"the longer trend has not broken, and volume above 1.5x normal says the move " +
				"has participation behind it.",
			Algorithm: &Algorithm{
				Name:     "Oversold bounce watch",
				Symbols:  defaultSymbols,
				Interval: "1d",
				All: []Node{
					{Indicator: "rsi", Period: 14, Op: OpLT, Value: f(35)},
					{Indicator: "close", Op: OpGT, Compare: &Operand{Indicator: "sma", Period: 200}},
					{Indicator: "volume", Op: OpGT, Compare: &Operand{Indicator: "vol_avg", Period: 20, Mult: 1.5}},
				},
				CooldownHours: 24,
				Notify:        NotifyConfig{Telegram: true, AIContext: true},
				Enabled:       false,
			},
		},
		{
			Key:         "golden_cross",
			Title:       "Golden cross",
			Description: "The 50-bar average crosses up through the 200-bar average.",
			Rationale: "A slow trend-change signal. It uses crosses_above rather than a plain " +
				"comparison, so it fires once on the bar the crossing happens instead of every " +
				"day the averages stay in that order.",
			Algorithm: &Algorithm{
				Name:     "Golden cross",
				Symbols:  defaultSymbols,
				Interval: "1d",
				All: []Node{
					{
						Indicator: "sma", Period: 50, Op: OpCrossesAbove,
						Compare: &Operand{Indicator: "sma", Period: 200},
					},
				},
				CooldownHours: 168,
				Notify:        NotifyConfig{Telegram: true, AIContext: true},
				Enabled:       false,
			},
		},
		{
			Key:         "52w_breakout",
			Title:       "52-week breakout",
			Description: "Close pushes above the prior 52-week high on heavy volume.",
			Rationale: "The high is read with shift 1 — the 52-week high as it stood on the " +
				"previous bar. Comparing against the current bar's own 52-week high could never " +
				"trigger, because that figure already includes today's high and a close cannot " +
				"exceed it.",
			Algorithm: &Algorithm{
				Name:     "52-week breakout",
				Symbols:  defaultSymbols,
				Interval: "1d",
				All: []Node{
					{
						Indicator: "close", Op: OpGT,
						Compare: &Operand{Indicator: "high_52w", Period: 252, Shift: 1},
					},
					{
						Indicator: "volume", Op: OpGT,
						Compare: &Operand{Indicator: "vol_avg", Period: 20, Mult: 1.5},
					},
				},
				CooldownHours: 72,
				Notify:        NotifyConfig{Telegram: true, AIContext: true},
				Enabled:       false,
			},
		},
		{
			Key:         "macd_momentum_turn",
			Title:       "MACD momentum turn",
			Description: "MACD crosses up through its signal line while price holds its 50-bar average.",
			Rationale: "Pairs a momentum trigger with a trend filter. The nested group means the " +
				"confirmation can come from either the trend or a strong RSI reading, so the rule " +
				"is not blocked by a single narrow condition.",
			Algorithm: &Algorithm{
				Name:     "MACD momentum turn",
				Symbols:  defaultSymbols,
				Interval: "1d",
				All: []Node{
					{
						Indicator: "macd", Fast: 12, Slow: 26, Signal: 9, Op: OpCrossesAbove,
						Compare: &Operand{Indicator: "macd_signal", Fast: 12, Slow: 26, Signal: 9},
					},
					{
						Any: []Node{
							{Indicator: "close", Op: OpGT, Compare: &Operand{Indicator: "sma", Period: 50}},
							{Indicator: "rsi", Period: 14, Op: OpGT, Value: f(55)},
						},
					},
				},
				CooldownHours: 48,
				Notify:        NotifyConfig{Telegram: true, AIContext: false},
				Enabled:       false,
			},
		},
	}
}
