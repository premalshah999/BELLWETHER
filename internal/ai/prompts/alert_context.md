An algorithm named "{{.AlgorithmName}}" has just triggered on {{.Symbol}}.

What the rule saw:
{{.Summary}}

Current price: {{.Price}}
{{if .News}}
Recent news for this symbol:
{{range .News}}- {{.Title}} ({{.Source}}, {{.Age}}){{end}}
{{else}}
No recent news was found for this symbol.
{{end}}
Write exactly three sentences of context for the operator receiving this alert.

Sentence one: what the indicator reading actually means for this symbol right now.
Sentence two: whether the supplied news explains it, or that no catalyst is visible.
Sentence three: the single most useful thing to check next.

No recommendation. No price target. No preamble — start with the first sentence.
