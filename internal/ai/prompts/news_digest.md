Score each article below for its relevance to the symbol {{.Symbol}}{{if .Company}} ({{.Company}}){{end}}.

Articles:
{{range .Articles}}
[{{.ID}}] {{.Title}}
    source: {{.Source}}, published: {{.Age}}
{{end}}

For each article return:

- relevance: 0 to 1. How much this article bears on {{.Symbol}} specifically.
  A story about the whole index scores low. A story about this company scores high.
  A story that merely mentions the ticker in a list scores very low.
- sentiment: -1 to 1, for this symbol only. Negative is bad news for the
  company, positive is good news. Use 0 when the article is factual or unclear.
- one_line: at most fifteen words on why it matters. No filler.

Return JSON in exactly this shape:

{
  "scores": [
    {"id": "the article id", "relevance": 0.0, "sentiment": 0.0, "one_line": "…"}
  ]
}

Score every article listed, using its exact id. Do not add articles.
