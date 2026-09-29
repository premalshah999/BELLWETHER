# API

All endpoints are under `/api`, JSON in and out. Protected routes require a
signed-in session: `POST /api/auth/login` with `{"key": "tsk_…"}` exchanges a
key for an `HttpOnly` cookie. Errors are always
`{"error": {"code": "...", "message": "..."}}`; validation failures add a
`fields` array so a form can show each message inline.

A representative slice — the full route table is `internal/server/server.go`:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/meta` | App info, display timezone, which features are configured |
| `GET` | `/api/health` | Per-dependency status, degraded flag, request budgets |
| `GET` | `/api/watchlist` | Watchlist with quotes and sparklines |
| `GET` | `/api/symbols/{symbol}/candles?interval=1d&limit=300` | OHLCV series |
| `GET` | `/api/symbols/{symbol}/fundamentals` | Valuation and peer comparison |
| `POST` | `/api/scan/run` | Trigger a market scan by hand |
| `GET` | `/api/events` | The classified event feed (`type`, `symbol`, `universe`, …) |
| `GET` | `/api/symbols/sectors?symbols=AAPL,MSFT` | Which sector each symbol sits in |
| `GET` | `/api/congress/filings?symbol=NVDA` | Congressional PTR disclosures |
| `GET` | `/api/eventstudy?type=EARNINGS&days=5` | Abnormal-return study for an event type |
| `GET` | `/api/algorithms`, `POST /api/algorithms/{id}/run` | Rule CRUD and manual evaluation |
| `GET` | `/api/alerts` | Alert feed |
| `POST` | `/api/symbols/{symbol}/explain` | Sourced AI explanation of today's move |
| `GET` | `/api/ai/calibration` | The model's measured forecasting track record |
| `POST` | `/api/research/ask` | Multi-source deep research, cited |
