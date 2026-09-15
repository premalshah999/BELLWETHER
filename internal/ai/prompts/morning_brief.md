Write the morning brief for {{.Date}} ({{.TZ}}).

Watchlist overnight moves:
{{range .Symbols}}- {{.Symbol}}: {{.Price}} ({{.ChangePercent}}), {{.Note}}
{{end}}
{{if .News}}
Notable news across the watchlist:
{{range .News}}- [{{.Symbol}}] {{.Title}} ({{.Source}}, {{.Age}})
{{end}}{{else}}
No news was collected for the watchlist overnight.
{{end}}
{{if .Alerts}}
Algorithms that fired since the last brief:
{{range .Alerts}}- {{.AlgorithmName}} on {{.Symbol}}: {{.Summary}}
{{end}}{{else}}
No algorithms fired since the last brief.
{{end}}
Return JSON in exactly this shape:

{
  "headline": "one sentence naming the single most important thing on this list",
  "bullets": [
    "five bullets at most, each one sentence, each about a specific symbol or theme from the data above"
  ],
  "watch_today": [
    "at most three concrete things to watch today, each naming a symbol or a level"
  ]
}

Use only the data above. If a section was empty, do not invent entries for it —
write fewer bullets instead. No recommendations, no price targets.
