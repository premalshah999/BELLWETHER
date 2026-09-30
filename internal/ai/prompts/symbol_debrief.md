Write a debrief on {{.Symbol}}{{if .Company}} ({{.Company}}){{end}} from everything
this system has collected about it.

Period covered: {{.Period}}
Events: {{.Count}}{{if .Official}} · {{.Official}} confirmed by an exchange or regulator{{end}}
{{if .Industry}}Industry: {{.Industry}}{{end}}

{{range .Events}}
[{{.ID}}] {{.When}} · {{.Type}}{{if .Official}} · OFFICIAL{{end}}{{if .Importance}} · importance {{.Importance}}/10{{end}}
  {{.Headline}}
{{- if .Summary}}
  {{.Summary}}
{{- end}}
{{- if .Facts}}
  stated: {{.Facts}}
{{- end}}
{{end}}

Write for someone who holds or is considering this position and has not been
watching it. Cover, in this order and only where there is something to say:

**What happened** — the events that matter, in the order they happened, with
the figures the filings actually stated. A dividend is "$0.26 per share, record
date 18 September", not "declared a dividend". Group repeats: three order wins
is one paragraph naming all three, not three paragraphs.

**What it adds up to** — the pattern across the period. Is the order book
building or thinning? Are insiders buying or selling? Are the disclosures
routine or is something being worked through? Say when there is no pattern;
a quarter of routine filings is a real and useful finding.

**What is unresolved** — board meetings called but not yet reported, record
dates ahead, litigation pending, exchange queries awaiting a response. These
are the things that will produce the next move, and they are knowable now.

**What we cannot see** — where the collected record is thin or one-sided. If
everything came from the company's own filings and no independent coverage,
say that. If the period is short, say that.

Rules:

Cite every factual claim with the event ids in brackets, like [4] or [4, 11].
An id that does not appear above must not appear in your answer.

Never state a number the events did not state. If an order's value was not
disclosed, the debrief says the value was not disclosed — it does not estimate.

Distinguish what an exchange confirmed from what was reported. Filings are the
company on the record; anything else is coverage.

No recommendation, no price target, no view on whether to buy or sell. Describe
what happened and what is outstanding; the reader decides.

Return JSON in exactly this shape and nothing else:

{
  "headline": "one sentence, at most twenty words, on the period as a whole",
  "sections": [
    {"title": "What happened", "body": "…", "events": [4, 11]}
  ],
  "outstanding": [
    {"item": "…", "expected": "when it is due, if stated", "events": [7]}
  ],
  "blind_spots": ["…"],
  "event_count": 0
}

Omit any section that would be empty rather than filling it with filler.
