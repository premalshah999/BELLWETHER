# Research workflow

How the research engine retrieves, ranks, reads and cites sources, and the
limits it runs within. It is a shared workspace for a small team, not an
isolated multi-tenant service.

## Where evidence comes from

Every question fans out, in parallel, to:

- **the archive** — classified events and their evidence already in Postgres;
- **official sources** — SEC full-text search, the Federal Register, and the
  keyless agency and central-bank feeds in `internal/research/us_official.go`
  and `feeds.go`;
- **measured history** — prices, abnormal moves and the event study's base
  rates for the symbols the question names;
- **the open web** — a private SearXNG node, Bing News RSS, Google News RSS
  (the whole index, plus queries scoped to a dozen named publishers) and
  GDELT. None needs a key. Bing's click-tracking links are unwrapped to the
  publisher's own URL; a Google News link stays a headline and a link,
  because it cannot be read as an article.

The three general search sources also back `GET /api/web/search`, which the news search, the
chart's context panel and the scanner use to show headlines the archive does
not hold. A source that fails or times out is reported by name in the answer
rather than silently dropped.

## How it works

- Research offers **Evidence only** (the UI default, no AI calls) and an
  optional **AI brief**. Both retrieve sources and market measurements.
- Documents are ranked by query relevance, then authority and recency.
  Synthesis uses up to 12 readable documents, at most three from a hostname,
  with up to 480 relevant words each, and at most 6,000 output tokens.
  Discovery links remain visible but cannot supply citations.
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
- Public availability of a feed is not a promise of unlimited access or
  commercial redistribution rights.

## Checking it

```sh
npm --prefix web ci && npm --prefix web run build   # Go embeds web/dist
go vet ./... && go test -race ./...
```

Network-dependent feed checks are opt-in:

```sh
BELLWETHER_LIVE_SOURCES=1 go test ./internal/research -run TestAdditionalOfficialFeedsLive -v
```

`node web/tests/research.smoke.mjs` drives the research page against a local
Vite server on port 5174. It intercepts API calls with fixtures; it does not
contact the running application or spend AI tokens.

## Still required before selling a hosted SaaS

1. **Tenant isolation:** watchlists, positions, research, alerts and stored
   outputs need ownership and authorization on every read/write. API key
   roles alone do not separate customers.
2. **Market-data entitlement:** free delayed prices are not a substitute for
   an exchange-entitled live feed. Define delay labels, trading calendars,
   provider rights and customer freshness requirements.
3. **Research evaluation:** maintain representative company, sector and macro
   questions and manually score relevance, readable coverage, claim support
   and gaps. Add PDF and structured filing adapters against those cases
   rather than expanding the source count without measuring useful coverage.
4. **AI quality and cost:** fixture tests do not establish live model quality
   or operating costs. Representative live briefs and research need
   evaluation against a metered-provider allowance.
5. **Operational proof:** complete a backup restore drill, sustained load
   testing, source-latency monitoring, alert delivery checks and off-host
   recovery.
