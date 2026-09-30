# Operations

## Deploying

```bash
cp .env.example .env          # fill in what you have; everything is optional
docker compose up -d
docker compose exec tradesys tradesys -issue-key -name "alice" -role owner
```

The app listens on `http://127.0.0.1:8080` only. To publish it on a domain
with automatic HTTPS, point DNS at the server and set:

```
PUBLIC_HOSTNAME=bellwether.example.com
COMPOSE_PROFILES=public
```

`docker compose up -d` then also starts Caddy (`deploy/Caddyfile`), which
obtains and renews a Let's Encrypt certificate on its own. No domain? sslip.io
maps any IP-shaped name to that IP: `203-0-113-10.sslip.io`.

| Service | Role | Exposed |
| --- | --- | --- |
| `tradesys` | the app: API, ingestion, scheduler, embedded frontend | 127.0.0.1:8080 |
| `postgres` | primary database | 127.0.0.1:5433 (psql and tests only) |
| `yfinance` | price-history sidecar | compose network only |
| `searxng` | private metasearch for research | compose network only |
| `backup` | `pg_dump` every 6 hours | none |
| `caddy` | HTTPS reverse proxy (`public` profile) | 80, 443 |

### Prices

```
MARKETDATA_ORDER=yfinance,twelvedata,alphavantage
```

Keep the bundled yfinance sidecar first: it has no request budget and the
deepest history. Twelve Data and Alpha Vantage are optional, keyed fallbacks
whose daily caps are enforced before any network call.

### Web search

Research, the news search and the chart's headlines reach the open web
through the bundled SearXNG node, Bing News and Google News. None needs a key.
SearXNG is reachable only inside the compose network and signs its own
requests with `SEARXNG_SECRET`; leave it empty and one is generated at start.

### A model on the same host

```
LLM_BASE_URL=http://host.docker.internal:11434/v1
```

Compose maps `host.docker.internal` to the host gateway, so Ollama or vLLM
running beside the container needs no extra networking.

## Access keys

Keys are issued on the server only — there is no endpoint for it, so the
power to grant access stays with whoever holds the shell.

```bash
docker compose exec tradesys tradesys -issue-key -name "alice" -role owner
docker compose exec tradesys tradesys -list-keys
docker compose exec tradesys tradesys -revoke-key tsk_xxxxxxxx
```

Roles: `owner` (manages keys), `operator` (full use), `viewer` (read only). A
deployment with no keys is locked. `ALLOW_UNAUTHENTICATED=true` opens it for
local development only.

> `docker compose exec` bypasses the image's entrypoint, so the binary name
> is repeated. With `docker compose run`, do **not** repeat it — the
> entrypoint already supplies it, and a repeated name silently stops Go's flag
> parsing at the first non-flag argument.

## One-off commands

All reuse the exact code path their scheduled job runs.

| Flag | Does |
| --- | --- |
| `-sync-congress` | fetch House STOCK Act disclosures now (backfill or resync) |
| `-sync-smartmoney N` | fetch insider trades for the last N days and the followed funds' 13Fs |
| `-refresh-calendar` | rebuild the forward earnings/dividend calendar (~3 min for the universe) |
| `-merge-duplicates N` | clean headlines and merge duplicate events from the last N days |
| `-backfill-history` | store five years of daily bars for the whole universe (about 15 minutes); run once on a new install |
| `-backfill-earnings` | store every past earnings announcement with its EPS surprise (about 30 minutes) |
| `-backfill-insiders N` | load the newest N quarters of SEC's insider-transaction datasets |
| `-forecast` | validate the forecast model and store today's ranking |
| `-reprocess` | discard derived events and rebuild them from stored raw items, after a classification rule changes |
| `-rollover-news` | move aged news to the archive database now rather than at 02:30 ET |
| `-reclaim-space` | with `-rollover-news`: return freed space to the OS. Takes an exclusive lock per table; ingestion stalls while it runs |

## A new install

The event study, the forecast and the insider history need history the live
feeds cannot supply. Load it once, in this order:

```bash
docker compose exec tradesys tradesys -backfill-history
docker compose exec tradesys tradesys -backfill-earnings
docker compose exec tradesys tradesys -backfill-insiders 12
docker compose exec tradesys tradesys -forecast
```

## Backups

The `backup` service writes a compressed `pg_dump` every six hours into the
`tradesys-backups` volume. Every dump is kept for two days, then the **first**
of each day for `BACKUP_KEEP_DAYS` (default 14) — the first rather than the
last, because if corruption landed on a given day, the earliest copy is the
one most likely to predate it.

By hand:

```bash
docker compose exec postgres pg_dump -U tradesys -Fc tradesys > backup.dump
```

## Splitting news across two databases

```
DATABASE_URL              primary: recent news and everything else
NEWS_ARCHIVE_DATABASE_URL archive: aged news only (optional)
NEWS_HOT_WINDOW           how long news stays on the primary (default 720h)
```

Neon and Supabase both work as the archive. Two settings are applied
automatically, because both fail silently otherwise: `sslmode=require`
(libpq's default `prefer` falls back to plaintext without complaint), and
`default_query_exec_mode=exec` on a transaction-mode pooler (`-pooler` on
Neon, port 6543 on Supabase), where cached prepared statements otherwise fail
intermittently under load. A value you set yourself is never overridden.

Run `-rollover-news` once by hand the first time — an existing database may
have months to move. Without `-reclaim-space` a rollover stops the primary
growing rather than shrinking it, because Postgres marks freed pages reusable
without returning them.

> Compose sets `DATABASE_URL` for the `tradesys` service itself, pointing at
> the bundled Postgres, and that overrides `.env`. To move the primary
> elsewhere, change it in `docker-compose.yml`, not only in `.env`.

## The universe

`internal/news/company/data/us_listings.csv` is the S&P 1500 joined to SEC's
own exchange file for CIK and exchange. Rebuild it with:

```bash
SEC_USER_AGENT="Bellwether/1.0 (you@example.com)" \
  python3 scripts/build_us_listings.py --dry-run   # show the diff first
```

It decides far more than the scanner: the default feed admits an event only
when one of its companies is a constituent, so narrowing this list quietly
narrows the feed, the screens and every peer group.

## Scheduling and time

Times are stored in UTC. Every scheduled job runs on New York time, so the
scans, the morning brief and the alert checks track the session through
daylight-saving changes wherever the server sits. Weekday schedules do not
model market holidays.

| When (ET) | Job |
| --- | --- |
| every 2 / 10 min | event processing / news poll |
| 09:45, :15 and :45 from 10:15 to 15:45, 16:15 weekdays | market scan |
| 07:20 weekdays | forward calendar |
| 08:30, 08:45 weekdays | morning brief, outlooks for the watchlist |
| every 30 min, 06:00–23:30 weekdays | insider trades |
| 06:30, 07:20 daily | Congress filings, fund holdings |
| 05:45 daily | scoring of AI outlooks whose horizon has passed |
| 02:30 daily | news rollover to the archive |
| 02:00 Saturday | fundamentals refresh for the universe |
| hourly | event briefs, news digest, event classification |
| 16:40 weekdays | forecast model: validate and rank the universe |
| every 30 s in the session | paper trading: fill orders, run agents; equity marked every 15 min and at the close |
| 05:00 Sunday | earnings history (latest quarters) |
| 04:00 on the 3rd | SEC insider dataset for the newest quarter |
| :10 hourly | tickers for unresolved 13F holdings |

In the browser each viewer picks New York, their own zone or UTC from the
status menu; every clock time names the zone it is shown in.
