# Architecture

Bellwether is one Go binary with the React frontend embedded, one Postgres
database, and a small Python sidecar for price history. Everything else is an
optional adapter.

```
                ┌──────────────────────── sources ────────────────────────┐
                │ SEC EDGAR · Federal Register · Fed · Treasury · BLS ·   │
                │ White House · DOJ/FTC · House Clerk · GDELT · RSS ·     │
                │ Google News discovery · per-symbol watch queries        │
                └───────────────────────────┬─────────────────────────────┘
                                            │ lanes (per-host rate limits)
                                            ▼
   ┌───────────┐   fetch → normalize → dedupe → resolve entities → classify
   │  scanner  │──────────────┐                      │
   │ (stats)   │  raises the  │                      ▼
   └─────┬─────┘  priority of │           ┌─────────────────────┐
         │        abnormal    └──────────▶│ events + evidence   │◀── Jev decides
         │        symbols                 │ (3 timestamps each) │    type / importance /
         ▼                                └──────────┬──────────┘    direction
   ┌───────────┐                                     │
   │ candles   │◀──── yfinance sidecar                ├──▶ sectors → holdings
   └─────┬─────┘                                     ├──▶ event study
         │                                           ├──▶ alerts → Telegram
         └──────────────▶ algorithms · backtests     └──▶ research (cited, text model)
```

## Layout

```
cmd/tradesys/          wiring: the only package that names a concrete adapter
internal/
  config/              environment loading and validation
  marketdata/          Provider interface, Router, Symbol, Candle, Quote
    yfin/ twelvedata/ alphavantage/ fixture/
  storage/             persistence ports
    postgres/          production store; migrations embedded here
  news/                lane-based ingestion: fetch → normalize → dedupe
    company/           the listed-instrument master (SEC-derived US universe)
  events/              type taxonomy, sector inference, SEC / Federal Register parsers
  scanner/             statistical "what's abnormal" pass over the universe
  congress/            House Clerk STOCK Act disclosures
  eventstudy/          abnormal returns against a venue benchmark
  fundamentals/        valuation snapshots, statements, forward catalyst dates
  indicators/          SMA/EMA/RSI/MACD/ATR/VWAP, session-aware
  screens/             saved multi-factor screens over the universe
  research/            multi-source research with citation-gated synthesis
  backtest/            leakage-free strategy backtesting
  algo/                the rule language: parse, validate, evaluate
  alerts/              evaluation scheduler → cooldown → notify
  notify/              Telegram delivery
  search/              Tavily and Brave search adapters (SearXNG lives in research/)
  auth/                API keys, roles, session cookies
  stream/              server-sent quote stream
  ai/                  text-model client, prompts as files, spend guards
  jev/                 TypeSafe System One client: choice / score / noul
  server/              REST API, SSE stream, static frontend
  health/              per-dependency status
web/                   React + Vite + TypeScript + Tailwind
services/yfinance/     Python sidecar, reachable only inside the compose network
deploy/                Caddyfile (public HTTPS) and SearXNG settings
scripts/               data generators (the S&P 1500 universe)
```

## Seams that matter

**Every external dependency sits behind an interface.** `cmd/tradesys` is the
only package that imports a concrete adapter. Storage, market data, search,
both model providers and notifications are swappable and fakeable in tests.

**The market-data Router owns graceful degradation.** Per request it serves
fresh cache, falls through providers in priority order, serves stale cache
flagged `stale` before giving up, and writes through on success. A chart
never silently substitutes one instrument's data for another's without saying
so in `resolved_symbol`.

**Deterministic before AI, everywhere.** The scanner is pure statistics. The
universe, sector taxonomy and entity resolution are rule-based. Models are
the last stage of the pipeline, never the first: they never decide *what* to
look at.

**Decisions and prose are different jobs.** Thousands of small classification
decisions a day go to Jev, a typed decision model that returns a choice, a
score or a yes/no with a calibrated confidence. Prose — briefs, research
answers — goes to an OpenAI-compatible text model. See [ai.md](ai.md).

## Provenance: three timestamps

Every event carries:

| Field | Meaning |
| --- | --- |
| `occurred_at` | when it really happened, if known |
| `published_at` | the publisher's own timestamp |
| `discovered_at` | when *this system* first saw it |

Only `discovered_at` is ever used to reason about what was knowable at a given
moment (`KnowledgeTime()`). A publisher can backdate; a feed can arrive late;
the discovery clock cannot lie about what this system knew. That makes
look-ahead bias structurally impossible in the event study and backtester
rather than avoided by convention.

Each event links to its evidence — every raw item from every source that
reported it — with a per-source trust tier. The feed shows the most
trustworthy reporter, and `+N` for the independent sources behind it.

## Symbols

Canonical form is `TICKER` for US listings and `TICKER.VENUE` otherwise
(`GSPC.INDEX` is the S&P 500, rendered `^GSPC` to Yahoo). Class shares use a
hyphen the way the SEC does (`BRK-B`); a dot is reserved for the venue
separator, and `BRK.B` typed by a user is rewritten to `BRK-B`.

Every stored symbol round-trips through `marketdata.ParseSymbol` /
`.String()`. No vendor suffix (`.NS`, `.BO`) can reach storage — `CHECK`
constraints reject it.

The universe is the S&P 1500 joined to SEC's own exchange file for CIK
(`internal/news/company/data/us_listings.csv`, rebuilt by
`scripts/build_us_listings.py`). It gates more than the scanner: the default
feed admits an event only when one of its companies is a constituent.

## News storage across two databases

The news graph — events, evidence, entities, raw items — is one joined
structure, and Postgres cannot join across servers, so it cannot be split by
table. It is split by **time** instead: news older than `NEWS_HOT_WINDOW`
moves to `NEWS_ARCHIVE_DATABASE_URL`. An event and everything hanging off it
always live on the same server, both servers carry the identical schema, and
a read spanning the cutoff is two identical queries merged. An unreachable
archive degrades to the primary rather than failing the feed.

## Migrations

Embedded, applied at startup under an advisory lock, recorded in
`schema_migrations`. SQL and Go migrations interleave by name; a numbered Go
migration that is not registered in code is a hard startup failure. Never
edit a shipped migration — append a new one.
