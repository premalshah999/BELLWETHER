Explain today's price move in {{.Symbol}}.

Price data:
- Current: {{.Price}}
- Change: {{.Change}} ({{.ChangePercent}})
- Day range: {{.DayLow}} to {{.DayHigh}}
- Volume: {{.Volume}}{{if .VolumeNote}} ({{.VolumeNote}}){{end}}
- Recent trend: {{.TrendNote}}
{{if .Measured}}
Measured against this instrument's own history — arithmetic from the price
series, not a claim from any source. Use it to say whether today is actually
unusual for this stock rather than treating any move as notable:
- {{.Measured}}
{{end}}

{{if .Sources}}Sources retrieved for this symbol:
{{range .Sources}}
[{{.Index}}] {{.Title}}
    {{.URL}}
    {{.Snippet}}
{{end}}{{else}}
No sources were retrieved for this symbol.
{{end}}

Return JSON in exactly this shape:

{
  "explanation": "two to four sentences explaining the move, citing sources as [1], [2] where they support a claim",
  "confidence": "high | medium | low",
  "cited": [1, 2],
  "caveat": "one sentence on what would change this reading, or what the sources do not cover"
}

Rules:

- Cite only the numbered sources above, and only where they genuinely support
  the claim. An uncited claim must be one the price data alone supports.
- If the sources do not explain the move, say so and set confidence to "low".
  A candid "no clear catalyst" is more useful than a confident guess.
- Never invent a source, a headline, or a number.
