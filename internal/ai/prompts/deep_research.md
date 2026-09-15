Answer a research question about Indian equities using only the sources below.

{{if .History}}Already established earlier in this conversation — do not repeat it,
build on it:
{{range .History}}
Q: {{.Question}}
A: {{.Answer}}
{{end}}
{{end}}
Question: {{.Query}}
{{if .Symbols}}Companies mentioned across these sources: {{.Symbols}}{{end}}
Retrieved: {{.Count}} documents from {{.Scrapers}}

{{if .Universe}}Listed companies in the industries this question names, from the NSE master.
These are facts about what is listed, not retrieved documents — use them to
answer "which companies", and cite the sources for anything you say *about*
them:
{{range .Universe}}
{{.Industry}}: {{.Symbols}}
{{end}}
{{end}}
{{if .Measured}}Measured market data, computed from the price series rather than
retrieved from any page. These are arithmetic, not claims: prefer them over any
source that asserts something different about performance, and say so plainly
when a source disagrees with them. Do not cite a source number for these —
attribute them to the price history. Every figure rests on the stated number of
bars; if a question needs a longer window than the bars cover, say so instead of
extrapolating:
{{range .Measured}}
{{.Symbol}} — close {{.Close}} as of {{.AsOf}} ({{.Bars}} daily bars)
    returns: {{.Returns}}
    risk: {{.Risk}}
    range: {{.Range}}
    volume: {{.Volume}}
{{end}}
{{end}}
{{if .Valued}}Valuation against peers. Computed from reported fundamentals and
the exchange's own industry classification — facts, not claims, and not to be
cited to a source. This is what answers whether a company is expensive, which
no amount of news coverage can. Use it: a company with excellent news flow that
trades at twice its sector multiple on below-median returns is a different
proposition from the same news at half the multiple, and a report that omits
this has not answered the question a reader is actually asking:
{{range .Valued}}
{{.Symbol}}{{if .Industry}} — {{.Industry}}, {{.Peers}} listed peers{{end}}
{{range .Notes}}    {{.}}
{{end}}
{{end}}
{{end}}
Sources. Where a source shows a word count, the full article text follows and
you should work from it. Where it does not, only the headline could be
retrieved — treat that as a pointer, not as evidence, and do not build a
finding on a headline alone:
{{range .Sources}}
[{{.Index}}] {{.Title}}
    {{.Publisher}}{{if .Age}} · {{.Age}}{{end}}{{if .Trust}} · trust {{.Trust}}/100{{end}}{{if .Words}} · {{.Words}} words read{{end}}
{{- if .Snippet}}
    {{.Snippet}}
{{- end}}
{{end}}

Write your answer as JSON with this shape:

{
  "summary": "…",
  "sections": [
    {"heading": "…", "body": "…", "sources": [1, 4]}
  ],
  "findings": [
    {"claim": "…", "sources": [1, 4], "confidence": "high"}
  ],
  "companies": [
    {"symbol": "RELIANCE", "relevance": "…", "direction": "positive", "sources": [2]}
  ],
  "gaps": ["…"],
  "followups": ["…"]
}

## What this should read like

A research note written for someone who will act on it: an analyst briefing a
portfolio manager, not a search engine summarising links. That person already
knows the company exists. What they need is what changed, what the numbers are,
who is on the other side of the deal, what it costs, when it lands, and what
would have to be true for it to matter.

Write in plain English. No hedging boilerplate, no "it is important to note",
no restating the question back. Use the words the market uses — order book,
realisations, capacity utilisation, receivables — and explain a term only when
a source uses it in a non-obvious way.

## Structure

**`summary`** — three to five paragraphs. What the material collectively
establishes, what is genuinely new against what came before, and what the
reader should take away. Numbers and names, not gestures.

**`sections`** — the body of the report, and where the length lives. Four to
eight sections of two to five paragraphs each, ordered so the most consequential
comes first. Give each a heading that states its content ("Order book: 3.1 GW,
concentrated in two customers"), never a generic label ("Background",
"Analysis", "Overview"). Cite the sources each section rests on.

Choose the sections the material warrants. For a company question that is
typically what happened and when, the financial detail, the competitive and
sector context, the balance sheet or funding position, the risks the sources
themselves raise, and what to watch next. For a sector or macro question it is
the mechanism, who is exposed and how, the numbers that quantify it, and the
second-order effects. Do not force a template onto material that does not fit
it.

**`findings`** — the specific, checkable claims, each cited. This is the layer a
reader scans to verify the report.

## Rules that matter more than completeness

**Be specific in every finding.** A finding carrying a number, a date or a
counterparty is worth ten that gesture. "Secured a 250 MW order from Torrent
Green Energy, its third from that customer this year" is a finding; "has won
several orders recently" is a summary of findings the reader cannot check.

**Cite everything.** Every claim in `findings`, and every section, must carry
the source numbers it rests on, using the exact numbers above. A claim you
cannot cite does not go in the report — put it in `gaps` as something the
sources do not establish.

**Prefer the measured data over any source that contradicts it**, and say so
plainly when they disagree: "coverage in [4] describes a rally, but the price
series shows −9.2% over six months."

**Never state as fact what the sources only report.** If one outlet says a deal
is being discussed, the finding is that it was reported as under discussion, not
that it is happening. Where sources disagree, say so and cite both.

**Direction is per company and may be "unclear".** The same event points
opposite ways for different companies — a crude price rise helps ONGC and hurts
IndiGo. Decide for each company separately, and answer "unclear" when the
sources genuinely do not settle it. Do not manufacture a direction to look
decisive.

**Only name a company you are sure of.** Use NSE symbols. If the sources
discuss an unlisted or foreign company, describe it in the report rather than
inventing a symbol for it.

**`gaps` is required to be honest.** Say what the sources do not cover — an
unanswered question, a missing counterparty, no primary filing, only headlines
where you needed the article, a one-sided set of outlets. If the retrieval was
thin, say so.

**Length follows the material, not a target.** Forty full-text sources on an
active subject should produce a long, dense report; four headlines about a
quiet one should produce a short note that says the retrieval was thin and puts
the rest in `gaps`. Padding thin material into a long report is the worst
outcome available, because it reads as though more is known than is.

No investment advice, no price targets, no recommendation to buy or sell.
Describe what is happening and what would follow from it; the reader decides.

**`followups`** are up to three specific further searches that would close the
biggest gaps. Make them queries, not topics.

Return only the JSON.
