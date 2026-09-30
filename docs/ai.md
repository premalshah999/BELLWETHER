# The AI layer

Two model providers, split by what each is for. Neither is load-bearing:
prices render, algorithms evaluate and alerts fire with both switched off.

| | Jev (TypeSafe AI) | Text model (any OpenAI-compatible) |
| --- | --- | --- |
| **Job** | decisions | prose |
| **Examples** | event type, importance, "is this an event at all?", direction per company, article relevance and sentiment | morning brief, event briefs, research answers, move explanations, monthly debriefs, outlooks |
| **Output** | a typed choice, score or yes/no, with a calibrated confidence | text, with citations checked against supplied sources |
| **Config** | `TYPESAFE_API_KEY`, `JEV_MODEL` | `LLM_BASE_URL`, `LLM_MODEL`, `LLM_API_KEY` |
| **Guard** | per-call cost recorded | daily USD cap + monthly token cap |

A text model is a slow, expensive way to make thousands of small decisions a
day, and a decision model cannot write a paragraph. Each does the half it is
good at. With no Jev key, decisions fall back to the text model.

## Jev: gated by confidence

Code: `internal/jev` (client), `internal/ai/jev_classify.go`,
`internal/ai/jev_types.go`, `internal/ai/digest.go`.

The client speaks TypeSafe System One's three primitives — **choice** (up to
255 options), **score** (2–10 ordered levels) and **noul** (a yes/no) — and
retries only what TypeSafe says to retry (429, 529) with exponential backoff.
A 401 or 422 is never retried.

Every answer is gated by what a wrong answer costs:

| Decision | Gate | Below the gate |
| --- | --- | --- |
| Event type | confidence ≥ 0.5 | the deterministic keyword rules' type stands |
| Direction per company | confidence ≥ 0.5 | recorded as "unclear" |
| Is this an event? | P(event) < 0.2 | importance set to 0: dropped from the default feed, never deleted |

## The text model: parsed defensively

Points at any OpenAI-compatible chat-completions endpoint and assumes
**neither function calling nor JSON-schema enforcement**. Structured output is
prompted and parsed defensively — fences and surrounding prose stripped, one
corrective retry on a parse failure, then the feature degrades rather than
looping.

**Model output is never presented as fact.** Every AI response renders inside
one shared panel with a badge, a timestamp and the model name. A citation
pointing at a source that was not actually supplied is stripped. A research
finding with no valid citation is dropped, not softened.

**Prompts are files, not Go strings** (`internal/ai/prompts/*.md`). A prompt
is the specification of a feature's behaviour; burying it in source hides it
from anyone not reading the code. Every prompt is parsed at startup, so a
malformed one fails the boot, not the 08:30 brief — and
`TestEveryPromptParsesAndRenders` exercises all of them.

## Spend guards

Two independent ceilings, both enforced **before** the call:

- **`LLM_DAILY_USD_CAP`** (default `1.00`). Each call is checked against its
  worst case — peak-hour rate, every input token uncached, the full output
  allowance spent — and that worst case is *reserved under the same lock as
  the check*, so twenty concurrent calls cannot each pass a $1 cap and spend
  $2.40 together (there is a test for exactly that). Actual cost, split into
  cache hits and misses, is recorded afterwards. Resets at 00:00 UTC. An
  unknown model is priced at the dearest known rate.
- **`LLM_MONTHLY_TOKEN_BUDGET`**. Prompt plus requested output allowance; `0`
  disables the text model.

When the usage counter cannot be read or written, the client fails closed
rather than spending against an unknown balance.

Every call is recorded with its cost: `llm_usage` for the text model,
`jev_usage` for Jev.

## Calibration

An outlook is a forecast with a horizon and stated probabilities for up, flat
and down. One is written for a few watchlist names each weekday at 08:45 ET,
and for any symbol on request from its chart. Every one is logged. Once the
horizon passes, the realised move scores it with a multi-category Brier score
against a uniform-guess baseline (0.667) — of the forecasts where the model
said 70%, about 70% should have come true — and the result is plotted as a
reliability curve on the **AI** page. Scoring needs no model call, so the
track record stays current even when the budget is spent, which is exactly
when an operator most wants to know how far to trust the model.
