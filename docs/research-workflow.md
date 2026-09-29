# Research workflow

How the research engine retrieves, ranks, reads and cites sources, and the
limits it runs within. It is a shared workspace for a small team, not an
isolated multi-tenant service.

## How it works

- Research offers **Evidence only** (the UI default, no AI calls) and an
  optional **AI brief**. Both retrieve sources and market measurements.
- Documents are ranked by query relevance, then authority and recency.
  Synthesis uses up to 12 readable documents, at most three from a hostname,
  with up to 480 relevant words each. Discovery links remain visible but
  cannot supply citations. These limits reduce the previous 48-source input
  ceiling and 32,000-token output ceiling to 12 sources and 6,000 output tokens;
  they are bounds, not measured savings or guarantees of answer quality.
- Summary, sections, findings and company claims require citations to source
  IDs actually supplied to the model. Citation presence does not prove that a
  claim follows from the text; readers still need to inspect material claims.
- Search results are cached for two minutes per provider/query/limit. Article
  text is cached for 15 minutes, bounded to 256 documents of up to 1,800 words.
  Neither cache is durable; restarting loses it. Full article text is not
  returned to the browser or stored in conversation JSON.
- Article retrieval validates public addresses at connection time, rejects
  private redirects and non-web ports, checks robots.txt, honors crawl delays
  and Retry-After, caps transfers at 4 MiB, and bounds concurrency globally.
  Each page has a 20-second deadline including queueing. Unreadable PDFs,
  paywalls, short pages and JavaScript shells remain explicit coverage gaps.
- Discovery requests share per-host pacing and Retry-After across adapters.
  Research has two concurrent job slots and one active turn per conversation
  within a process. Provider failures cool down for a minute. Failed synthesis
  preserves the retrieved evidence; headlines alone do not trigger synthesis.
- AI budget admission accounts for requested output tokens and concurrent
  reservations within the process. Failed usage accounting blocks further
  calls until restart. This is not a distributed billing ledger or an exact
  tokenizer; deployments with several app instances need durable reservations.
- Five official feeds were fetched and parsed from this host on 2026-09-20:
  [ECB releases and speeches](https://www.ecb.europa.eu/rss/press.html),
  [ECB statistics](https://www.ecb.europa.eu/rss/statpress.html),
  [Bank of England](https://www.bankofengland.co.uk/rss/news),
  [EIA Today in Energy](https://www.eia.gov/rss/todayinenergy.xml), and
  [FTC releases](https://www.ftc.gov/feeds/press-release.xml).
  They serve both scheduled ingestion and direct research. They require no
  API key; public availability is not a promise of unlimited access or
  commercial redistribution rights. BLS and candidate BEA/DOJ endpoints
  failed the initial live probes and were not added.
- Ingestion keeps scheduling while slower providers finish, with a shared
  worker bound and per-source exclusion. The watchlist sweep runs every ten
  minutes. US/NSE universe scans run every half hour during the configured
  intraday windows, plus opening/closing scans. US close is 16:15 New York
  time, replacing the incorrect 15:45 schedule. Scans reject overlapping
  manual or scheduled runs. Weekday schedules do not model market holidays.
- Fresh persistent deployments require an access key. Set
  `ALLOW_UNAUTHENTICATED=true` only for deliberately open local development.
  Existing configured keys continue to work. No migration is needed.
- Navigation emphasizes the trading desk, news, signals, research, calendar
  and alerts. Research shows its progress, provider failures, extraction
  status, citation links, and submission errors; failed requests restore the
  draft. The selected conversation and mode survive reloads.

## Verification and rollout

Build the frontend before building or testing Go because Go embeds `web/dist`.
Do not rebuild that directory while Go checks are running.

```sh
npm --prefix web ci
npm --prefix web run build
go vet ./...
go test -race ./...
go build ./cmd/tradesys
```

Set `TEST_DATABASE_URL` to a dedicated scratch Postgres database to run the
integration tests. Never point it at the production database. The GitHub
workflow supplies an isolated Postgres service and runs these checks plus
the browser smoke test. Network-dependent feed checks are opt-in:

```sh
BELLWETHER_LIVE_SOURCES=1 go test ./internal/research -run TestAdditionalOfficialFeedsLive -v
```

Run the UI check against a local Vite server on port 5174 with
`node web/tests/research.smoke.mjs`. It intercepts API calls using test fixtures;
it does not contact the running application or spend AI tokens.

Before rollout, ensure an owner key exists with the existing CLI
`tradesys -issue-key -name owner -role owner`, verify session configuration,
take a database backup and keep the currently running image for rollback.
Deploy through the existing Compose workflow and check source health,
research completion, memory and scan durations. The additional scan cadence
increases sidecar work and needs observation on this small host.

## Validation completed in this workspace

On 2026-09-20: the full Go suite passed under the race detector; affected
packages were checked again after the final edits. `go vet`, the production
binary build, the frontend build, Compose configuration validation and
`git diff --check` passed. Postgres integration tests passed against a separate
Postgres 17 container, which was removed afterwards. Desktop/mobile browser
checks passed, and all five new feeds passed the opt-in live parser test.

The frontend still emits its existing large-bundle advisory. The GitHub
workflow was added but has not run on GitHub. The application changes on
this branch have not been deployed.

A subsequent configuration-only update on 2026-09-20 switched the running
service to DeepSeek V4.1 Flash (`deepseek-flash`) for both model tiers, with
thinking disabled. The provider accepted a minimal JSON completion (20 total
tokens). The existing application image was recreated without a build and
returned healthy with the new configuration. Its AI health entry initially
reports "not yet contacted" until an application request runs; the direct
provider probe does not establish end-to-end research quality. Credentials
are stored in the ignored, mode-0600 `.env`. The pre-existing shared monthly
limit remains 4 billion tokens and includes usage from the previous provider.

## Still required before selling a hosted SaaS

1. **Tenant isolation:** watchlists, positions, research, alerts and stored
   outputs need ownership and authorization on every read/write. API key
   roles alone do not separate customers. Add onboarding and tenant-scoped
   quotas after this boundary is designed and tested.
2. **Market-data entitlement:** free delayed prices are not a substitute for
   an exchange-entitled live feed. Define supported markets, delay labels,
   trading calendars, provider rights and customer freshness requirements.
3. **Research evaluation:** maintain representative US/India/macro questions
   and manually score relevance, readable coverage, claim support and gaps.
   Add PDF and structured filing adapters against those cases rather than
   expanding the source count without measuring useful coverage.
4. **AI quality and cost:** provider authentication and a minimal completion
   passed, but representative live briefs and research still need evaluation.
   Choose a metered-provider allowance and account for the previous provider's
   usage in the shared monthly counter. Fixture tests do not establish live
   model quality or operating costs. Evidence-only research on this branch
   is usable independently.
5. **Operational proof:** complete a backup restore drill, sustained load
   testing, source-latency monitoring, alert delivery checks and off-host
   recovery. This branch's integration tests use a new scratch database;
   they do not establish recovery of the production data.
