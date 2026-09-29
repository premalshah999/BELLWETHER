# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for a security problem.

Report it privately through GitHub's
[private vulnerability reporting](../../security/advisories/new) on this
repository. Include what you found, how to reproduce it, and what an attacker
could do with it. You should get an acknowledgement within a few days.

## Scope

Bellwether is self-hosted, and most of its security rests on how it is
deployed. Things we especially want to hear about:

- authentication or role bypass (API keys, session cookies, `viewer` → write);
- server-side request forgery through research article fetching
  (`internal/research/network.go` is meant to prevent it);
- secrets reaching the API, the logs, the database or the frontend bundle;
- SQL injection or path traversal anywhere.

## Deployment guidance

- Keep `.env` out of version control — it is gitignored, along with every
  `.env.*` except `.env.example`.
- Set `SESSION_SECRET` and a real `POSTGRES_PASSWORD`.
- Leave `ALLOW_UNAUTHENTICATED=false` on anything reachable from a network.
- Postgres and the app bind to `127.0.0.1` by default; publish only through
  the Caddy proxy (`COMPOSE_PROFILES=public`), which terminates TLS.
- API keys are shown once at issue time and stored hashed. Revoke unused ones
  with `tradesys -revoke-key`.
