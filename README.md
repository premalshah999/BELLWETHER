# Bellwether

*(internal codename: TradeSys Dashboard)*

A self-hosted market intelligence terminal for **US and Indian equities** —
built by two operators who trade and invest their own capital, currently
running as a live personal deployment.

> **This build contains no order-placement code path of any kind.** It
> watches markets, filings and policy, and tells you what it found. It does
> not trade.
>
> Analysis tool. Not investment advice. Data may be delayed.

---

## The problem this answers

Bloomberg Terminal ($24,000/year) connects world events to positions.
Retail tools show you *what* moved but almost never *why*, and never which
of *your* holdings a policy change, a filing, or a Congressional trade
actually touches. AI finance chatbots answer confidently and cite nothing —
you cannot audit them, so you cannot trust them.

Bellwether occupies that gap at retail cost:

```
world event / filing / policy change  →  which sectors it reaches
                                       →  which of your holdings sit there
                                       →  cited, auditable evidence
```

Three things make that trustworthy rather than another chatbot wrapper:

- **Provenance discipline.** Every event carries three timestamps — when it
  happened, when it was published, and when *this system* discovered it.
  Only the last is ever used to reason about "what was knowable when," which
  is what makes look-ahead bias structurally impossible downstream (see
  [Event study](#event-study), the clearest demonstration of why this
  matters).
- **Deterministic routing of AI spend.** A statistical scanner picks the
  handful of instruments worth attention out of a universe of 1,250+; only
  those trigger a model call. The LLM never decides what to look at, and
  never places a trade.
- **Auditable synthesis.** Research cites sources, flags where they
  disagree, and says plainly what they fail to establish. A finding with no
  valid citation is dropped, not softened.

---

## What it actually does

| | |
| --- | --- |
| **Dashboard & Charts** | Real OHLCV across US and NSE venues, with indicator overlays and session-aware VWAP. |
| **Scanner** | Reads price and volume across the whole universe (2,250+ instruments: the S&P 1500 and the NSE scan list) every trading session, finds what is behaving abnormally *before any article exists about it*, and raises that instrument's search priority. |
| **News** | 80+ sources: SEC EDGAR (8-K, Form 4, 13F), NSE/BSE corporate filings, the Federal Register, Fed and Treasury releases, GDELT, and targeted Google News queries — deduplicated, timestamped, and classified into one of ~54 event types. |
| **Geopolitics & Policy** | Macro, regulatory, commodity and geopolitical events, fanned out to the GICS/NSE sectors they touch and cross-referenced against your own watchlist — "does this actually reach anything I hold?" |
| **Congressional trading** | House Clerk STOCK Act disclosures: which members traded which tickers, and how many days they took to disclose it against the 45-day statutory deadline. |
| **Event study** | For any event type, the measured abnormal return (vs. the S&P 500 or NIFTY 50) over N days after the event, with a real sample size and honest small-sample warnings — not a chart with an arrow next to a date. |
| **Catalyst calendar** | The only forward-looking record here: next earnings, ex-dividend and dividend dates across the whole universe, with the analyst EPS range — and the event study's historical base rate for that event type attached, so a date on a calendar says "and the last 1,986 times, here is what followed". |
| **Research** | Multi-source deep research over a question, with per-finding citations, disagreement flagged explicitly, and a `gaps` section naming what the sources don't establish. |
| **Algorithms & Backtest** | A JSON rule language over indicators (SMA/EMA/RSI/MACD/ATR/VWAP/52-week high-low…) with three-valued (unknown-aware) logic, evaluated on a schedule, and a leakage-free backtester with its own small-sample warning discipline. |
| **Alerts** | Rule triggers, delivered to Telegram and the in-app feed, with the full evaluation snapshot persisted for audit. |
| **AI calibration** | Every AI-generated outlook is logged with a horizon and probabilities, scored against what actually happened, and plotted as a reliability curve — no LLM call needed to keep the track record current. |

---

See [the research and production readiness notes](docs/production-readiness.md)
for the evidence-only workflow, source verification, safeguards, test commands,
and remaining requirements before hosting separate customers.

## Quick start

```bash
git clone <this repo> && cd Dashboard
cp .env.example .env       # see Configuration below
docker compose up -d
```

This starts three services: **Postgres**, the **yfinance sidecar**
(`services/yfinance`, deep US/NSE/BSE daily history with no request budget),
and **tradesys** itself — a static Go binary with the built React frontend
embedded.

A fresh deployment requires an access key before protected routes can be used:

```bash
docker compose exec tradesys tradesys -issue-key -name "you" -role owner
```

Open <http://localhost:8080> and sign in with that key. For intentionally open
local development only, opt in with `ALLOW_UNAUTHENTICATED=true`.

```bash
docker compose logs -f tradesys     # follow
docker compose down                 # stop, keeping the data volume
```

### Running a model on the same host

```
LLM_BASE_URL=http://host.docker.internal:11434/v1
```

The compose file maps `host.docker.internal` to the host gateway, so a
self-hosted model (Ollama, vLLM, …) running beside the container needs no
extra networking.

### Building without Docker

Requirements: **Go 1.27+**, **Node 20+**, a reachable **Postgres 17**.

```bash
npm --prefix web install
npm --prefix web run build     # the Go binary embeds web/dist
go build -o tradesys ./cmd/tradesys
DATABASE_URL=postgres://... ./tradesys
```

### Development

```bash
go build -o tradesys ./cmd/tradesys && ./tradesys &   # backend on :8080
npm --prefix web run dev                              # frontend on :5173, proxying /api
```

### Tests

```bash
go test ./...                                          # unit tests, no DB needed
TEST_DATABASE_URL=postgres://... go test ./...         # + Postgres integration tests
npm --prefix web run build                             # frontend typecheck + build
```

Postgres integration tests run against a **dedicated database**, never the
one an environment's `DATABASE_URL` points at — set `TEST_DATABASE_URL`
explicitly to a scratch database and nothing else. Tests that need it skip
cleanly when it is unset.

### Backing up

```bash
docker compose exec postgres pg_dump -U tradesys -Fc tradesys > backup.dump
```

The compose file also runs a scheduled dump into the `tradesys-backups`
volume on the interval set by `BACKUP_KEEP_DAYS`.

---

## Architecture

```
cmd/tradesys/             wiring — the only place that names a concrete adapter
internal/
  config/                 environment loading and validation
  marketdata/              Provider interface, Router, Symbol, Candle, Quote
    yahoo/, yfin/, twelvedata/, alphavantage/, fixture/
  storage/                 persistence ports
    postgres/               the production implementation; migrations live here
    sqlite/                 one-time importer target for pre-Postgres installs
  news/                     lane-based multi-source ingestion: fetch -> normalize -> dedupe
    company/                 the listed-instrument master (NSE + SEC-derived US universe)
  events/                   classification: type taxonomy, sector inference, SEC/NSE parsers
  scanner/                  the statistical "what's abnormal" pass over the whole universe
  congress/                 House Clerk STOCK Act disclosure parsing
  eventstudy/               abnormal-return computation against a venue benchmark
  research/                 multi-source deep research, citation-gated synthesis
  backtest/                 leakage-free strategy backtesting
  algo/                     the rule language: parse, validate, evaluate (three-valued logic)
  alerts/                   evaluation scheduler -> cooldown -> notify pipeline
  ai/                       LLM client, prompt files (as files, not Go strings), budget
  server/                   REST API + static frontend serving
  health/                   per-dependency status for the settings page's dots
web/                       React + Vite + TypeScript + Tailwind frontend
services/yfinance/         the yfinance sidecar (Python), reachable only inside the compose network
```

### Seams that matter

**Every external dependency sits behind an interface**, with `cmd/tradesys`
the only package that imports a concrete adapter. Storage, market data,
search, AI and notification are all swappable and independently fakeable in
tests.

**The market-data Router owns graceful degradation.** For every request it
serves fresh cache, falls through providers in priority order, serves stale
cache flagged `stale` before giving up entirely, and writes through on every
success. A chart never silently substitutes one instrument's data for
another's without saying so in `resolved_symbol`.

**Deterministic before AI, everywhere.** The scanner is pure statistics. The
scan universe, sector taxonomy and entity resolution are all rule-based. The
LLM is the last stage of the pipeline, never the first — it never decides
*what* to look at, only helps explain what deterministic code already found.

**Symbols are canonical and venue-qualified.** `AAPL` (bare — US is the
default venue), `RELIANCE.NSE`, `GSPC.INDEX` for a benchmark index. One
namespace across every table; no bare ticker can collide across venues (a
real risk: `ABB` and `INFY` each name a different instrument on NSE versus
the NYSE).

**Three timestamps travel with every event**: `occurred_at` (when it really
happened, if known), `published_at` (the publisher's own timestamp), and
`discovered_at` (when this system found it — the only one ever used to
reason about what was knowable at a given moment). This is what makes the
event study honest and look-ahead bias structurally impossible rather than
merely avoided by convention.

**All times are stored in UTC**, displayed in `DISPLAY_TZ`, and scheduled
per-venue (`CRON_TZ=` overrides) so a US-hours job runs on US market hours
regardless of the display timezone an operator has set.

---

## Data sources

A representative slice — the full catalogue is `internal/news/catalog.go`.

| Source | Kind | Key needed |
| --- | --- | --- |
| **SEC EDGAR** (8-K, Form 4, 13F, full-text search) | Official filings | no — needs a declared `SEC_USER_AGENT` contact string |
| **Federal Register** | Regulatory/policy, with structured agency metadata | no |
| **House Clerk** | Congressional STOCK Act disclosures | no |
| **NSE / BSE corporate announcements** | Official filings | no |
| **Federal Reserve, Treasury, White House** | Official releases | no |
| **GDELT** | Global event index | no |
| **Google News** (targeted queries per event class) | Discovery layer | no |
| **Yahoo Finance / yfinance sidecar** | Price history, both venues, 5+ years; forward earnings and dividend dates | no |
| **Twelve Data, Alpha Vantage** | Price history, budgeted fallback | yes (free tier) |
| **Tavily, Brave** | Research search | yes (free tier) |

### If you are running on a rented server

Yahoo's unauthenticated feed rate-limits datacenter IP ranges — on Hetzner,
DigitalOcean, AWS and similar it returns `429` to *every* request, not
intermittently. The app degrades correctly (falls through to the next
provider, serves stale cache), but you do not want your primary source to be
one that never works:

```
MARKETDATA_ORDER=yfinance,twelvedata,alphavantage
```

The yfinance sidecar runs inside the compose network and is unaffected.

---

## Configuration

Everything is read from the environment, loaded from `.env` at startup;
variables already exported win over `.env`. See
[`.env.example`](.env.example) for the fully annotated list.

**Secrets are never written to the database and never returned by the API.**
The settings page reports only whether each dependency is configured.

The app degrades feature by feature rather than refusing to start — with no
keys at all it still serves cached and Yahoo/yfinance prices, and every AI
feature reports itself unavailable rather than the app failing to boot.

---

## Symbol format

Canonical form is `TICKER` (US, the default venue) or `TICKER.VENUE`
(`RELIANCE.NSE`, `TCS.BSE`). A benchmark index uses `TICKER.INDEX`
(`GSPC.INDEX` → S&P 500, rendered `^GSPC` to Yahoo/yfinance). Class shares
use a hyphen the way the SEC itself does (`BRK-B`), never a dot — a dot is
reserved for the venue separator.

Every stored symbol round-trips through `marketdata.ParseSymbol` /
`.String()`; no bare ticker is ever ambiguous across venues, and no vendor
suffix (`.NS`, `.BO`) ever escapes into storage or the API.

---

## API

All endpoints are under `/api`, JSON in and out, and protected routes require a signed-in session (`POST /api/auth/login` exchanges a
key for an `HttpOnly` cookie). Errors are always
`{"error": {"code": "...", "message": "..."}}`; validation failures add a
`fields` array so a form can show each message inline.

A representative slice — the full route table is `internal/server/server.go`:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/meta` | App info, display timezone, which features are configured |
| `GET` | `/api/health` | Per-dependency status, degraded flag, request budgets |
| `GET` | `/api/watchlist` | Watchlist with quotes and sparklines |
| `GET` | `/api/symbols/{symbol}/candles?interval=1d&limit=300` | OHLCV series |
| `GET` | `/api/symbols/{symbol}/fundamentals` | Valuation and peer comparison |
| `POST` | `/api/scan/run` | Trigger a market scan by hand |
| `GET` | `/api/events` | The classified event feed (`type`, `symbol`, `universe`, …) |
| `GET` | `/api/symbols/sectors?symbols=AAPL,MSFT` | Which sector each symbol sits in |
| `GET` | `/api/congress/filings?symbol=NVDA` | Congressional PTR disclosures |
| `GET` | `/api/eventstudy?type=EARNINGS&days=5` | Abnormal-return study for an event type |
| `GET` | `/api/algorithms`, `POST /api/algorithms/{id}/run` | Rule CRUD and manual evaluation |
| `GET` | `/api/alerts` | Alert feed |
| `POST` | `/api/symbols/{symbol}/explain` | Sourced AI explanation of today's move |
| `GET` | `/api/ai/calibration` | The model's measured forecasting track record |
| `POST` | `/api/research/ask` | Multi-source deep research, cited |

---

## The algorithm language

An algorithm is a JSON document:

```json
{
  "name": "Momentum watch",
  "symbols": ["AAPL", "MSFT"],
  "interval": "1d",
  "all": [
    {"indicator": "rsi", "period": 14, "op": "<", "value": 35},
    {"indicator": "close", "op": ">", "compare": {"indicator": "sma", "period": 200}},
    {"indicator": "volume", "op": ">", "compare": {"indicator": "vol_avg", "period": 20, "mult": 1.5}}
  ],
  "cooldown_hours": 24,
  "notify": {"telegram": true, "ai_context": true}
}
```

**Indicators:** `close`, `open`, `high`, `low`, `volume`, `sma`, `ema`, `rsi`,
`atr`, `vol_avg`, `vwap`, `session_vwap`, `macd`, `macd_signal`, `macd_hist`,
`high_52w`, `low_52w`. **Operators:** `<` `<=` `>` `>=` `==` `!=`
`crosses_above` `crosses_below`. **Groups:** `all` (AND) or `any` (OR),
nestable one level deep. **Operand modifiers:** `mult` scales a value, `offset`
adds to it, `shift` reads it *n* bars back.

### Three rules the evaluator will not bend

**Missing data is never zero.** An indicator with no value yet makes its
condition *unknown*, and unknown never fires an alert.

**Unknown is a third truth value, combined under Kleene logic.** A definite
`false` settles an AND regardless of unknown siblings; a definite `true`
settles an OR.

**A crossover is not a comparison.** `crosses_above` requires the previous
bar at or below and the current bar strictly above — conflating it with `>`
turns a one-off signal into a daily one.

---

## The AI layer

Points at any OpenAI-compatible chat-completions endpoint, assuming
**neither function calling nor JSON-schema enforcement**. Structured output
is prompted and parsed defensively: fences and surrounding prose stripped,
one corrective retry on a parse failure, then the feature degrades rather
than looping.

**No AI feature is load-bearing.** Prices render, algorithms evaluate, and
alerts fire whether or not the model is reachable.

**The token budget is enforced before the call and recorded after.** A
request whose prompt plus requested output allowance would overshoot `LLM_MONTHLY_TOKEN_BUDGET` is
refused before it reaches the network; concurrent calls reserve their allowance within the process; when the usage counter cannot be
read or recorded, the client fails closed rather than spending against an unknown
balance.

**Model output is never presented as fact.** Every AI response sits inside
one shared panel component that renders a badge, a timestamp and the model
name. A citation pointing at a source that was not actually supplied is
stripped from the text.

**Prompts are files, not Go strings** (`internal/ai/prompts/*.md`) — a
prompt is the specification of a feature's behaviour, and burying one in
source makes it invisible to anyone not reading the code. Every prompt is
parsed at startup, so a malformed one panics immediately rather than at
08:30 inside a cron job, and every prompt is exercised by
`TestEveryPromptParsesAndRenders`.

### Calibration

Every outlook is logged with its horizon and stated probabilities. Once the
horizon passes, the realised move scores it with a multi-category Brier
score against a uniform-guess baseline (0.667) — *of the forecasts where the
model said 70%, about 70% should have come true.* Scoring runs daily and
needs no LLM call, so the track record stays current even when the token
budget is spent — exactly when an operator most wants to know how far to
trust the model.

---

## Event study

The feature that closes the loop: does an event type actually move the
stocks it names, historically, or does it only look that way on a chart?

For a chosen type, every event's symbol is compared against its own venue
benchmark (S&P 500 or NIFTY 50) over N trading days *after the event was
discovered* — never after it occurred or was published, because that is the
only anchor a downstream decision could actually have acted on. The result
reports mean, median and hit-rate abnormal return, a real sample size, and
an explicit warning both for small samples and for events excluded by a
price-history coverage gap, rather than silently dropping them.

Price history for the computation comes largely for free: the scanner
already fetches a full year of daily bars for the whole scan universe on
every pass to compute its own statistics. That series is now persisted
(`internal/scanner`'s `Result.Series` → `SaveCandles`) instead of being
discarded the moment its metrics are derived — the event study's whole price
data prerequisite, at effectively no extra cost.

---

## Design

Dark, dense, calm. Near-black ground, one amber accent, red/green reserved
strictly for price movement. Tabular numerals throughout; Inter for prose,
JetBrains Mono for every ticker and figure. Fonts are bundled, not fetched
from a CDN.

**Empty, loading and error states are first-class.** Loading skeletons match
the exact height of the rows they stand in for. Every empty state says what
would fill it and how.

**Provenance is always visible.** Cached data says `cached`. A substituted
listing says `via RELIANCE.NSE`. Model output always carries its badge,
timestamp and model name.

The three-column terminal layout holds at 1280px and above; narrower, the
rails stack beneath the main pane.

---

## Operational notes

- **Migrations** apply automatically at startup and are recorded in
  `schema_migrations`, SQL and Go migrations interleaved by name. Never edit
  a shipped migration; append a new one. A numbered Go migration not
  registered in code is a hard startup failure, not a silent skip.
- **Access is by API key**, issued on the server only — there is no
  endpoint for it, so the ability to grant access is tied to shell access:
  ```
  docker compose exec tradesys tradesys -issue-key -name "you" -role owner
  docker compose exec tradesys tradesys -list-keys
  docker compose exec tradesys tradesys -revoke-key tsk_xxxxxxxx
  ```
  Roles: `owner` (may manage keys), `operator` (full use), `viewer`
  (read-only). A deployment with **no keys issued is locked** until an owner issues one.
  `ALLOW_UNAUTHENTICATED=true` explicitly enables open local development.
- **`-sync-congress`** runs the daily congressional-filings fetch on demand
  (backfill or resync), reusing the exact path the cron job calls.
- **News storage can span two Postgres servers.** The news graph — events,
  their evidence, the companies they name and the raw items behind all of it —
  is one joined structure, and Postgres cannot join across servers, so it
  cannot be divided by table. It is divided by time instead, which is the one
  cut that leaves each piece whole: an event and everything hanging off it
  always live on the same server, both servers carry the identical schema, and
  a read spanning the cutoff is two identical queries merged rather than a
  distributed join.

  ```
  DATABASE_URL              the primary: recent news, and everything else
  NEWS_ARCHIVE_DATABASE_URL the archive: aged news only (optional)
  NEWS_HOT_WINDOW           how long news stays on the primary (default 720h)
  ```

  Both Neon and Supabase are ordinary Postgres and work as either target. Two
  adjustments are applied automatically, because both are silent failures
  otherwise: `sslmode=require` (libpq's default is `prefer`, which falls back
  to plaintext without complaining), and `default_query_exec_mode=exec` on a
  transaction-mode pooler (`-pooler` on Neon, port 6543 on Supabase), where
  pgx's cached prepared statements otherwise fail intermittently under load
  with "prepared statement does not exist". A value you set yourself is never
  overridden.

  Leaving `NEWS_ARCHIVE_DATABASE_URL` empty keeps everything on one database,
  which is the deployment this app had before and still supports. An archive
  that is unreachable degrades to the primary and logs it, rather than failing
  the feed: the primary holds everything recent, which is what nearly every
  read asks for.
- **`-rollover-news`** moves aged news to the archive now rather than at
  02:30. Worth running by hand the first time: an existing database has months
  to move. Add **`-reclaim-space`** to return the freed space to the
  filesystem — it takes an exclusive lock per table, so ingestion stalls and
  the feed errors while it runs. Without it the rollover stops the primary
  growing rather than shrinking it, because Postgres marks freed pages
  reusable without returning them.
- **`-refresh-calendar`** rebuilds the forward catalyst calendar for the
  whole universe on demand (about three minutes for 2,254 symbols), reusing
  the path the weekday-morning cron calls.
- **`-reprocess`** discards all derived events and rebuilds them from stored
  raw items — for when a classification rule changes and history should get
  the correction too.
- **The scan universe is generated, not hand-maintained.**
  `internal/news/company/data/us_listings.csv` is the S&P 1500 joined to
  SEC's own exchange file for CIK and exchange; rebuild it with
  `python3 scripts/build_us_listings.py` (`--dry-run` to see the diff
  first). It decides far more than the scanner: the default market feed
  admits an event only when one of its companies is a constituent, so
  narrowing this list quietly narrows the feed.
- **Backups** run every six hours into the `tradesys-backups` volume:
  every dump kept for two days, then the first of each day for
  `BACKUP_KEEP_DAYS` (default 14). The first rather than the last on
  purpose — if corruption landed on a given day, the earliest copy is the
  one most likely to predate it.
