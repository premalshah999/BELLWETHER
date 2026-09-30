# Contributing to Bellwether

Thanks for considering it. Bug reports, data sources, fixes and docs are all
welcome.

## Before you start

- **Small fix?** Open a pull request directly.
- **New feature or a change in behaviour?** Open an issue first, so we can
  agree on the shape before you spend a weekend on it.
- **Security issue?** Don't open an issue — see [SECURITY.md](SECURITY.md).

## Setting up

```bash
cp .env.example .env
make dev-db      # Postgres on 127.0.0.1:5433
make run         # backend on :8080
make web-dev     # frontend on :5173 with hot reload
```

Issue yourself a key with `./tradesys -issue-key -name you -role owner`, or
set `ALLOW_UNAUTHENTICATED=true` in `.env` while developing.

## Before you open a pull request

```bash
make test        # frontend build, go vet, go test
gofmt -l .       # should print nothing
```

CI also runs the race detector and two Playwright smoke tests
(`web/tests/*.smoke.mjs`). Integration tests need `TEST_DATABASE_URL` pointing
at a **scratch** database; they skip cleanly without it.

## The rules this codebase keeps

These aren't style preferences — each one exists because breaking it produced
a real, silent bug.

1. **Only `discovered_at` is knowledge time.** Anything that asks "what was
   known at time *t*" — the event study, the backtester, alerts — anchors on
   when this system discovered an item, never when it says it was published.
   Publishers backdate. See [docs/architecture.md](docs/architecture.md#provenance-three-timestamps).
2. **Deterministic before AI.** Statistics and rules decide what to look at.
   A model may classify or explain what they found; it never selects it.
3. **Missing is not zero.** An indicator with no value is *unknown*, and
   unknown never fires an alert or a trade.
4. **Symbols are canonical.** Store and pass `marketdata.Symbol.String()`.
   A vendor's spelling (`^GSPC`) lives inside that vendor's adapter and
   never reaches storage.
5. **Every network adapter behind an interface.** Only `cmd/tradesys`
   imports a concrete provider.
6. **One clock for the market.** Server-side schedules and session logic use
   `marketdata.Market`; the browser shows a clock time in the viewer's
   chosen zone and always names it.
7. **Never edit a shipped migration.** Add a new one in
   `internal/storage/postgres/migrations/`.
8. **Prompts are files.** Model prompts live in `internal/ai/prompts/*.md`,
   not in Go strings.
9. **Degrade, don't fail.** A missing key or an unreachable dependency turns
   one feature off and says so; it never stops the app booting.

## Good first contributions

**A new data source.** Most sources are one entry in
`internal/news/catalog.go` or `official_catalog.go`: an ID, a URL, a trust
tier and a refresh interval. Please:

- fetch it from a real host first and note in a comment how many items it
  returned — several feeds that look fine in a browser return 403 or zero
  items to a server;
- pick the trust tier honestly (`internal/news/source.go`);
- respect the publisher's access policy. SEC, for instance, requires a
  declared contact and caps clients at 10 requests a second.

**Event-type keyword rules** (`internal/events/`), **indicators**
(`internal/indicators/`), and **docs** are also well contained.

## Commit messages

Say what changed and **why** in the body — what was wrong, how you know,
what you measured. "Fix feed" tells a reviewer nothing; "The FTC feed had
403'd 282 times in a row because it rejects Go's default User-Agent" tells
them everything.

## License

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE).
