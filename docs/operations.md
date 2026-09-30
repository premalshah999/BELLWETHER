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

### On a rented server

Yahoo's unauthenticated endpoint returns `429` to every request from
datacenter ranges (Hetzner, DigitalOcean, AWS…). The yfinance sidecar is
unaffected, so keep it first:

```
MARKETDATA_ORDER=yfinance,twelvedata,alphavantage
```

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
| `-refresh-calendar` | rebuild the forward earnings/dividend calendar (~3 min for the universe) |
| `-reprocess` | discard derived events and rebuild them from stored raw items, after a classification rule changes |
| `-rollover-news` | move aged news to the archive database now rather than at 02:30 ET |
| `-reclaim-space` | with `-rollover-news`: return freed space to the OS. Takes an exclusive lock per table; ingestion stalls while it runs |

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

All times are stored in UTC and shown in `DISPLAY_TZ`. Market jobs carry
their own `CRON_TZ`, so a US-hours scan runs on New York time whatever the
display zone is.
