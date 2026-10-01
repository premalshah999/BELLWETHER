# API

All endpoints are under `/api`, JSON in and out. Protected routes require a
signed-in session: `POST /api/auth/login` with `{"key": "tsk_…"}` exchanges a
key for an `HttpOnly` cookie. Errors are always
`{"error": {"code": "...", "message": "..."}}`; validation failures add a
`fields` array so a form can show each message inline.

A `viewer` key may read everything and run the three computations that store
nothing (a screen, an algorithm preview, a backtest); every other write needs
`operator`. Sign-in, the AI endpoints and web search are rate-limited per
caller and answer `429` with `Retry-After`.

A representative slice — the full route table is `internal/server/server.go`:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/meta` | App info and which features are configured |
| `GET` | `/api/health` | Per-dependency status, degraded flag, request budgets |
| `GET` | `/api/watchlist` | Watchlist with quotes and sparklines |
| `GET` | `/api/symbols/{symbol}/candles?interval=1d&limit=300` | OHLCV series |
| `GET` | `/api/symbols/{symbol}/fundamentals` | Valuation and peer comparison |
| `POST` | `/api/scan/run` | Trigger a market scan by hand |
| `GET` | `/api/scan/symbols/{symbol}` | One symbol's recent scanner signals |
| `GET` | `/api/events` | The classified event feed (`type`, `symbol`, `universe`, `q`, …) |
| `GET` | `/api/web/search?q=&kind=news\|web` | Headlines from the open web: SearXNG, Bing News, Google News |
| `GET` | `/api/symbols/sectors?symbols=AAPL,MSFT` | Which sector each symbol sits in |
| `GET` | `/api/congress/filings?symbol=NVDA` | Congressional PTR disclosures |
| `GET` | `/api/eventstudy?type=EARNINGS&days=5` | Abnormal-return study for an event type |
| `GET` | `/api/algorithms`, `POST /api/algorithms/backtest` | Rule CRUD and backtesting |
| `GET` | `/api/alerts` | Alert feed |
| `POST` | `/api/symbols/{symbol}/explain` | Sourced AI explanation of today's move |
| `POST` | `/api/symbols/{symbol}/debrief` | What happened to the stock over the last month, against its news |
| `POST` | `/api/symbols/{symbol}/outlook` | A probabilistic outlook, logged and later scored |
| `GET` | `/api/ai/calibration` | The model's measured forecasting track record |
| `POST` | `/api/research/ask` | Multi-source deep research, cited |
| `GET` | `/api/forecast?sort=p_beat&q=&offset=&limit=` | Every stock's forecast distribution, the engine's walk-forward record, the market's regime and the live record |
| `GET` | `/api/forecast/symbols/{symbol}` | One stock's distribution over 5, 10 and 20 sessions, with its 20-session fan |
| `GET` | `/api/smartmoney/funds`, `/api/smartmoney/funds/search?q=` | Followed funds; search any 13F filer on SEC |
| `POST` | `/api/smartmoney/funds` | Follow a 13F filer by CIK |
| `GET` | `/api/smartmoney/funds/{cik}?q=&kind=&sort=&offset=` | A fund's holdings, searched, filtered and paged |
| `GET` | `/api/eventstudy?type=EARNINGS_SURPRISE&days=5` | Reaction and drift, split by group |
| `POST` | `/api/journal` | Record a closed trade |
| `GET` | `/api/paper/wallets`, `POST /api/paper/wallets` | Paper-trading wallets |
| `POST` | `/api/paper/wallets/{id}/deposits` | Add simulated funds (idempotency key required) |
| `POST` | `/api/paper/wallets/{id}/orders` | Place a paper order |
| `GET` | `/api/paper/wallets/{id}/performance` | Returns, alpha, weekly results, skill-or-luck test |
| `POST` | `/api/paper/wallets/{id}/agents/{aid}/run` | Run a trading agent once |
