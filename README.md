# TradeSys Dashboard

A self-hosted market **analysis and alerting** terminal for Indian and US
equities. It watches symbols, evaluates rule-based algorithms against them, and
notifies you when their conditions are met.

> **This version contains no order-placement code path of any kind.** It
> observes markets and notifies. It does not trade.
>
> Analysis tool. Not investment advice. Data may be delayed.

---

## Status

| Milestone | Scope | State |
| --- | --- | --- |
| **M1** | Skeleton, data adapters, cache, chart, health dots | **done** |
| **M2** | Indicators, algorithm DSL + evaluator, builder UI, Telegram alerts | **done** |
| **M3** | News, search, AI layer, budget tracking, calibration | **done** |
| **M4** | Design polish, empty/error states, docker-compose | **done** |

---

## Quick start

```bash
git clone <this repo> && cd Dashboard
cp .env.example .env      # then edit: at minimum set APP_PASSWORD
docker compose up -d
```

Open <http://localhost:8080> and log in with any username and the
`APP_PASSWORD` you set. That is the whole install: the image builds the
frontend, compiles a static binary with the frontend embedded, and runs it
unprivileged with the SQLite file on a named volume.

On first run the watchlist is seeded with `RELIANCE.BSE` and `AAPL`, and the
four template algorithms are installed **disabled** — a fresh install must not
start notifying anybody before they have looked at it.

```bash
docker compose logs -f     # follow
docker compose down        # stop, keeping the data volume
```

To publish on a different port, set `HOST_PORT` in `.env`.

### Running a model on the same host

Self-hosted models usually run beside the container rather than in it. The
compose file maps `host.docker.internal` to the host gateway, so:

```
LLM_BASE_URL=http://host.docker.internal:11434/v1
```

### Building without Docker

Requirements: **Go 1.22+** and **Node 20+**. No cgo, no system SQLite, no
database server.

```bash
npm --prefix web install
npm --prefix web run build     # the Go binary embeds web/dist
go build -o tradesys ./cmd/tradesys
./tradesys
```

### Backing up

Everything is one SQLite file plus its WAL siblings.

```bash
docker compose stop
docker run --rm -v tradesys-data:/data -v "$PWD:/backup" alpine \
    tar czf /backup/tradesys-backup.tar.gz -C /data .
docker compose start
```

### Frontend development

Run the Go server and Vite side by side; Vite proxies `/api` to the backend so
the app is same-origin in dev exactly as it is in production.

```bash
./tradesys &                     # backend on :8080
npm --prefix web run dev         # frontend on :5173, proxying /api
```

To iterate on the frontend without rebuilding the Go binary, point the server
at the build output on disk:

```bash
WEB_DIST_DIR=./web/dist ./tradesys
```

### Tests

```bash
go test ./...                    # backend
npm --prefix web run build       # frontend typecheck + build
```

---

## Configuration

Everything is read from the environment, loaded from `.env` at startup.
Variables already exported in the environment win over `.env`. See
[`.env.example`](.env.example) for the annotated list.

**Secrets are never written to the database and never returned by the API.**
The settings page reports only whether each dependency is configured.

The app degrades feature by feature rather than refusing to start: with no keys
at all it still runs on Yahoo Finance alone.

---

## Architecture

```
cmd/tradesys/            wiring — the only place that names a concrete adapter
internal/
  config/                environment loading and validation
  marketdata/            Provider interface, Router, Symbol, Candle, Quote
    alphavantage/        adapter + hard daily request budget
    yahoo/               adapter + isolated parsing + venue fallback
    fixture/             deterministic synthetic provider (tests, demos)
  storage/               persistence ports
    sqlite/              the only implementation today; migrations live here
  health/                dependency health aggregation for the status dots
  server/                REST API + static frontend serving
  indicators/            SMA, EMA, RSI, MACD, ATR, VWAP, 52w high/low, vol avg
  algo/                  the rule language: parse, validate, evaluate
  alerts/                evaluation scheduler -> cooldown -> notify pipeline
  notify/                Notifier interface + telegram/ adapter
  ai/                    LLM client, prompt files, budget, the five AI features
  search/                SearchProvider interface + tavily/, brave/ adapters
  news/                  RSS pollers -> normalised articles -> AI scoring
web/                     React + Vite + TypeScript + Tailwind frontend
```

### Seams that matter

**Every external service sits behind an interface with a fake implementation.**
Nothing imports a concrete adapter except `cmd/tradesys/main.go`. This is what
makes the whole pipeline testable without a network.

**The Router is where graceful degradation lives.** For every request it:

1. serves the cache if the data is fresh *and* goes back far enough;
2. otherwise tries each provider in priority order, writing through to cache;
3. otherwise serves stale cache, flagged `stale` so the UI says so;
4. and only fails when there is nothing cached at all.

**Provenance is never hidden.** Every series carries the provider that served
it, whether it came from cache, and — when a provider substituted a sibling
listing — which one. A BSE chart quietly drawn from NSE prices would be a lie,
so the UI labels it `via RELIANCE.NSE`.

**All times are stored in UTC and displayed in `DISPLAY_TZ`** (IST by default).

---

## Data providers

| Provider | Key needed | Notes |
| --- | --- | --- |
| **Yahoo Finance** | no | No request limit, so it leads by default. Unofficial and undocumented; parsing is isolated in `yahoo/parse.go` and fails soft. **Blocks datacenter IPs** — see below. |
| **Twelve Data** | yes | Free tier ~800 requests/day. Carries NSE and BSE directly, and answers normally from datacenter IPs. The practical primary source on a rented server. |
| **Alpha Vantage** | yes | Free tier ~25 requests/day. The budget is enforced **before** any network call and persisted, so restarting cannot reset it. |
| **synthetic** | no | Generated, non-market data for evaluating the app without keys. Opt in with `ENABLE_SYNTHETIC_FALLBACK=true`. Always labelled `synthetic` in the UI. |

### If you are running on a rented server

Yahoo rate-limits datacenter IP ranges. On Hetzner, DigitalOcean, AWS and
similar it will return `429` to **every** request — not intermittently, but
always. The app degrades correctly when that happens (it falls through to the
next provider, and serves cached prices if none answer), but you do not want
your primary source to be one that never works.

Drop it and lead with Twelve Data:

```
MARKETDATA_ORDER=twelvedata,alphavantage
```

Both budgets are enforced before any network call and shown on the Settings
page, so you can see exactly how much of each allowance a day actually costs.

### Symbol format

Canonical symbols use the Alpha Vantage spelling — `RELIANCE.BSE`, `TCS.NSE`,
`AAPL` (US needs no suffix). Adapters translate into their own dialect;
provider-specific spellings never escape into storage or the API. Yahoo wants
`RELIANCE.BO`; Twelve Data wants `symbol=RELIANCE&exchange=BSE`; Alpha Vantage
takes the canonical form as-is.

Yahoo's coverage of some Indian listings is patchy — its `RELIANCE.BO` feed
returns a single bar. When the requested venue yields too little history to
chart, the adapter tries the sibling venue and **reports the substitution** in
`resolved_symbol` rather than passing it off as the venue you asked for.

---

## API

All endpoints are under `/api` and protected by HTTP basic auth (any username,
`APP_PASSWORD` as the password).

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/meta` | App info, display timezone, which features are configured |
| `GET` | `/api/health` | Per-dependency status, degraded flag, request budgets |
| `GET` | `/api/watchlist` | Watchlist with quotes and sparklines |
| `POST` | `/api/watchlist` | `{"symbol": "RELIANCE.BSE", "note": ""}` |
| `DELETE` | `/api/watchlist/{symbol}` | Remove a symbol |
| `GET` | `/api/symbols/{symbol}/candles?interval=1d&limit=300` | OHLCV series |
| `GET` | `/api/symbols/{symbol}/quote` | Latest quote |
| `GET` | `/api/algorithms` | List algorithms |
| `POST` | `/api/algorithms` | Create one |
| `GET`/`PUT`/`DELETE` | `/api/algorithms/{id}` | Read, replace, remove |
| `POST` | `/api/algorithms/{id}/run` | Evaluate now, delivering any alerts |
| `GET` | `/api/algorithms/{id}/evaluations` | Last outcome per symbol |
| `POST` | `/api/algorithms/validate` | Check a draft without saving |
| `POST` | `/api/algorithms/preview` | Evaluate a draft against live data, sending nothing |
| `GET` | `/api/algorithms/templates` | The four prebuilt templates |
| `GET` | `/api/algorithms/vocabulary` | Indicators, operators, intervals the engine supports |
| `GET` | `/api/alerts` | Alert feed (`limit`, `unread`, `symbol`, `algorithm_id`) |
| `POST` | `/api/alerts/{id}/read` | Acknowledge one alert |
| `POST` | `/api/alerts/read-all` | Acknowledge everything unread |
| `GET` | `/api/ai/status` | AI availability and the month's token budget |
| `GET`/`POST` | `/api/ai/brief`, `/api/ai/brief/generate` | Morning brief |
| `GET` | `/api/ai/briefs` | Brief archive |
| `POST` | `/api/symbols/{symbol}/explain` | Sourced explanation of today's move |
| `POST` | `/api/symbols/{symbol}/outlook` | Scenario outlook, logged for scoring |
| `GET` | `/api/ai/outlooks` | Every logged outlook and its resolution |
| `POST` | `/api/ai/outlooks/resolve` | Score due outlooks (no AI call) |
| `GET` | `/api/ai/calibration` | The model's measured track record |
| `POST` | `/api/ai/calc` | Position sizing (arithmetic in Go) |
| `GET`/`POST` | `/api/news`, `/api/news/poll` | Collected articles; collect now |
| `POST` | `/api/ai/digest/run` | Score collected articles now |

Errors are always `{"error": {"code": "...", "message": "..."}}`. Validation
failures add a `fields` array of `{field, message}` so the builder can show
each message inline against the input that caused it.

---

## The algorithm language

An algorithm is a JSON document:

```json
{
  "name": "Momentum watch",
  "symbols": ["RELIANCE.BSE", "TCS.BSE"],
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
`high_52w`, `low_52w`.

**Operators:** `<` `<=` `>` `>=` `==` `!=` `crosses_above` `crosses_below`.

**Groups:** `all` (AND) or `any` (OR), nestable one level deep.

**Operand modifiers:** `mult` scales a value (`1.5x` its average), `offset`
adds to it, and `shift` reads it *n* bars back — which is what makes a breakout
rule expressible, since today's 52-week high already contains today's high.

### Three rules the evaluator will not bend

**Missing data is never zero.** An indicator with no value yet makes its
condition *unknown*, and unknown never fires an alert. A 200-day average on 60
bars of history has no value; reading it as 0 would make "close above SMA200"
trivially true and alert on every symbol from day one.

**Unknown is a third truth value, combined under Kleene logic.** A definite
`false` settles an AND regardless of unknown siblings, and a definite `true`
settles an OR. So an `any` group still fires when one branch is satisfied and
another lacks data.

**A crossover is not a comparison.** `crosses_above` requires the previous bar
to be at or below and the current bar to be strictly above. `>` asks only about
the current bar. Conflating them turns a one-off signal into a daily one.

### Cooldowns

`cooldown_hours` suppresses repeat alerts per *(algorithm, symbol)*. The clock
lives in SQLite, so restarting the process cannot reset it. If the cooldown
state cannot be read, the engine **fails closed** and stays quiet: alert spam
destroys trust in every future alert.

### Evaluation schedule

Each interval has a cron expression, interpreted in `DISPLAY_TZ`. Daily rules
are checked several times through the Indian and US sessions rather than once,
so a trigger reaches the operator the same day; cooldowns keep that from
becoming repetition.

---

## Alert pipeline

```
evaluate -> cooldown check -> enrich -> optional AI context -> deliver -> persist
```

Persisting happens last but **unconditionally**. A Telegram outage does not
cost the operators the record that their rule fired; the alert lands in the
in-app feed with a failed delivery record attached.

Every alert stores its complete evaluation snapshot — each condition's label,
operator, both values, and result — so a surprising notification can be audited
months later against the numbers that actually produced it.

Non-firing evaluations are recorded too. Without them, a rule that silently
reports insufficient data every morning looks identical to one whose conditions
are simply not being met. The Algorithms page shows the last outcome per
symbol, which is what answers "why didn't this alert?".

Telegram messages are plain text, never markdown: an indicator label such as
`MACD(12,26,9)` or a value containing an underscore would otherwise be mangled
or rejected as malformed entities.

---

## Extending

### Add a market data provider

1. Create `internal/marketdata/<name>/`, implementing `marketdata.Provider`:
   `Name()`, `Candles(...) (Bars, error)`, `Quote(...) (Quote, error)`.
2. Keep **all** knowledge of the upstream's wire format in a pure `parse.go`
   that operates on `[]byte`, and test it against recorded payloads in
   `testdata/`. Adapters must return `ErrNotSupported`, `ErrBudgetExhausted`,
   or `ErrNoData` for expected non-answers so the Router treats them as
   "try someone else" rather than as faults.
3. Never return a zero price for missing data — drop the bar and log it.
4. Register it in `buildProviders` in `cmd/tradesys/main.go` and add it to the
   `MARKETDATA_ORDER` allow-list in `internal/config`.

### Add an indicator

1. Write the maths in `internal/indicators/` as a pure function returning a
   `Series` **aligned index-for-index with its input**, with NaN — never zero —
   wherever the indicator is not yet defined. That alignment is what lets the
   evaluator tell "no value yet" from "the value is zero".
2. Add a table-driven test verifying it against published known-good values.
   RSI and EMA are checked against Wilder's own dataset; if your indicator has
   a canonical worked example, use it.
3. Register it in `registry` in `internal/algo/registry.go` with its parameter
   kind, a `Describe` function for alert text, and a `MinBars` function so the
   scheduler fetches enough history. Nothing else needs to change — the builder
   UI reads its dropdowns from `/api/algorithms/vocabulary`.

### Add a notification channel

Implement `alerts.Notifier` (`Channel`, `Configured`, `Send`) in
`internal/notify/<name>/`, wrap it in `notify.WithHealth` so it lights a status
dot, and add it to the notifier list in `cmd/tradesys/main.go`. An unconfigured
channel must report `Configured() == false` rather than failing sends: the
pipeline skips it silently instead of recording a delivery failure.

### Add an AI feature

1. Write the prompt as a **file** in `internal/ai/prompts/`. Prompts are the
   specification of a feature's behaviour; burying one in Go source makes it
   invisible to everyone not reading the code. Templates are parsed at startup,
   so a malformed one panics immediately rather than at 08:30 inside a cron job.
2. Add a `Feature*` constant so the token spend is attributed on the settings
   page.
3. Build the request in a method on `ai.Service`. Call `s.guard(ctx)` first: it
   turns an unavailable AI layer into a `Status` the UI can render rather than
   an error that looks like a bug.
4. For structured output use `CompleteJSON`, which appends the JSON
   instruction, extracts the value from whatever wrapping the model adds, and
   retries once with the parse error before giving up.
5. Add the prompt to `TestEveryPromptParsesAndRenders`, including a case with
   empty collections — "no sources were retrieved" is the common path.

**No AI feature may be load-bearing.** Alerts fire, charts render, and
algorithms evaluate whether or not the model is reachable.

---

## The AI layer

Points at any OpenAI-compatible chat-completions endpoint. It assumes **neither
function calling nor JSON-schema enforcement**, because the model this was
built for supports neither.

### Five features

| Feature | When | Model tier |
| --- | --- | --- |
| **Morning brief** | 08:30 on weekdays, in `DISPLAY_TZ` | main |
| **News digest** | hourly, scoring collected articles per symbol | cheap |
| **Explain this move** | on demand, per symbol, with sources | main |
| **Outlook** | on demand — scenarios with probabilities, logged for scoring | main |
| **Position sizing** | on demand — **arithmetic in Go**, model only explains | main |

Alerts additionally carry an optional three-sentence context brief.

### Three rules

**Structured output is prompted and parsed defensively.** The model is asked
for JSON; the response is stripped of markdown fences and surrounding prose,
and if it still does not parse, the model is shown its own output and the error
and asked once to correct it. A second failure degrades the feature. There is
no third attempt: a model that has failed twice will not comply on the third,
and the tokens are real.

**The token budget is enforced before the call and recorded after.** Set
`LLM_MONTHLY_TOKEN_BUDGET`. A request whose prompt alone would overshoot is
refused before it reaches the network. If the usage counter cannot be read the
client **fails closed**, because spending against an unknown balance risks an
unbounded bill. When the budget is spent, AI features report "budget reached"
and everything else carries on.

**Model output is never data.** Every AI response in the UI sits inside one
shared panel component that renders a visible badge, a timestamp, and the model
name — so the rule cannot be forgotten in one place. Citations pointing at
sources that were not supplied are **stripped from the text**, because a `[1]`
with no source behind it is an assertion of evidence that never existed.

### Calibration

Every outlook is logged with its horizon (10 trading days) and its three stated
probabilities. Once the horizon passes, the realised move decides which
scenario happened, and the forecast is scored with a multi-category Brier
score. The calibration page plots stated probability against observed
frequency: **of the forecasts where the model said 70%, about 70% should have
come true.**

The mean Brier is shown beside the score a uniform guess would achieve
(0.667), because a Brier number without that reference means nothing to a
reader. Scoring runs daily and **needs no LLM call**, so the track record stays
current even when the budget is spent — which is exactly when an operator most
wants to know how far to trust the model.

---

## News and search

**News** polls Google News RSS per watchlist symbol, hourly. Feed parsing is
tolerant by necessity: real feeds embed HTML entities that strict XML rejects,
and a single malformed item is skipped rather than discarding the feed. An
unparseable date stays empty rather than defaulting to "now". Articles are
stored per `(symbol, url)` — the same story reached through two symbols is
scored twice, because relevance is a per-symbol judgement.

**Search** sits behind `search.Provider`, with Tavily and Brave adapters. The
configured preference is tried first and the other is the fallback. Results are
cached for an hour, and stale cache is served when both providers fail: search
only decorates an answer, so old sources beat none.

---

## Acceptance walkthrough

The path this was built against, and which is verified end to end:

1. **Fresh clone, fill `.env`, `docker compose up`** → app on `:8080`, healthy,
   seeded, all schedules registered.
2. **`RELIANCE.BSE` in the watchlist** → the chart renders real daily bars with
   SMA overlays. Yahoo's BSE feed for this name is broken, so the adapter falls
   back to NSE and the header says `via RELIANCE.NSE`.
3. **Create an algorithm with a threshold you know will trigger, run it** →
   a Telegram message arrives within one evaluation cycle carrying the
   indicator snapshot and the AI context.
4. **Break the Alpha Vantage key** → the settings page marks it degraded, the
   dashboard shows a banner naming it, and prices keep coming from Yahoo and
   cache.
5. **Ask "explain this move"** → a sourced answer with an AI badge, a
   timestamp, and citation links, with any citation to a source that was not
   supplied stripped out.
6. **Exhaust the LLM budget** (set `LLM_MONTHLY_TOKEN_BUDGET=1000`) → AI
   features report "budget reached", and alerts keep firing carrying
   `AI: AI context unavailable`.

---

## Design

Dark, dense, calm. Near-black ground, one amber accent, and red/green reserved
strictly for price movement — so a green number always means "up" and never
"success". Tabular numerals throughout; Inter for prose, JetBrains Mono for
every ticker and figure. Fonts are bundled, not fetched from a CDN: a
self-hosted dashboard should not phone home to render.

**Empty, loading, and error states are treated as first-class**, not as
afterthoughts. Loading skeletons match the exact height of the rows they stand
in for, so nothing shifts under the cursor when data lands. Every empty state
says what would fill it and how to make that happen.

**Provenance is always visible.** Cached data says `cached`. A substituted
listing says `via RELIANCE.NSE`. Model output sits inside one shared panel
component that renders a badge, a timestamp, and the model name — so the rule
cannot be forgotten in one place.

The three-column terminal layout holds at 1280px and above. Narrower than that
the rails stack beneath the chart, because squeezing three fixed columns into a
laptop width collapses the quote header onto itself.

---

## Operational notes

- **SQLite** runs in WAL mode. The database is a single file; back it up by
  copying `DB_PATH` and its `-wal`/`-shm` siblings.
- **Migrations** apply automatically at startup and are recorded in
  `schema_migrations`. Never edit a shipped migration; append a new one.
- **`APP_PASSWORD` empty disables authentication.** That is intended for local
  development only, and the server warns loudly at startup.
