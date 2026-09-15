Rewrite a follow-up question into a standalone web search query.

The conversation so far:
{{range .History}}
Q: {{.Question}}
A: {{.Answer}}
{{end}}

The new question: {{.Question}}
{{if .Symbols}}Companies discussed so far: {{.Symbols}}{{end}}

The new question was written by someone who has just read the answers above, so
it leaves out everything those made obvious. "What about their debt?" means
nothing to a search engine. Your job is to put back exactly what was left out
and nothing else.

Rules:

**Resolve every reference.** "They", "it", "the company", "that deal" — replace
each with the thing it refers to, taking it from the conversation above.

**Keep the user's intent narrow.** If they asked about debt, search for debt.
Do not broaden it into a general query about the company because that would
return more; it would return more of the wrong thing.

**Add only what disambiguates.** A company name, a sector, a period. Do not add
words meant to improve ranking — search operators, "latest", "news", "2026" —
unless the question itself is about recency.

**If the question is already standalone, return it unchanged.** Rewriting a
question that needed no rewriting is how a good query becomes a worse one.

Return JSON and nothing else:

{
  "query": "the standalone search query",
  "reasoning": "one short sentence on what you resolved, or why you left it alone"
}
