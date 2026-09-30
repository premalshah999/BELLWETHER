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
