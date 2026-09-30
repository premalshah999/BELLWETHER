# Forecast and paper trading

Two features that turn Bellwether from describing what happened into testing
what will: a model that ranks stocks for the next five sessions, and paper
accounts where a strategy — yours, an algorithm, or an AI — trades real prices
with simulated money until the record says whether it works.

## The forecast model

Code: `internal/forecast`, run by `cmd/tradesys/forecast.go` after each close.

### What it reads

| Factor | Evidence |
| --- | --- |
| 12-month momentum, skipping the last month | Jegadeesh and Titman (1993) |
| 1-month return (reversal) | Jegadeesh (1990) |
| 5-day return (short-term reversal) | widely replicated |
| 20-day volatility | the low-volatility anomaly |
| Volume, 5 days against 60 | attention and unabsorbed news |
| Distance from the 52-week high | George and Hwang (2004) |
| RSI(14) | the most-watched oscillator |
| Latest earnings surprise within 90 days | post-earnings-announcement drift |
| Opportunistic insider buying, 90 days | Cohen, Malloy and Pomorski (2012) |
| Discretionary insider selling, 90 days | the weaker opposite signal |

Each is known at the close of the session it is computed for: bars that have
closed, earnings announced before that close, Form 4s filed by that evening.

### How it learns

Every session, each factor and the five-session forward return are replaced by
their rank across the universe, scaled to [-1, 1]. A ridge regression maps
factor ranks to return ranks. Ranking makes the model robust to outliers and
regimes, and a linear model on good factors is the baseline serious research
keeps returning to: Microsoft's Qlib reports rank ICs of about 0.045 for its
own LightGBM and linear models on 158 factors.

### How it is judged

Walk-forward. Each calendar year is scored by a model fit only on the three
years before it, with a gap of twice the horizon so no training label overlaps
the test. The page reports, per year and overall:

- **Rank IC** — the correlation between the ranking and what then happened.
- **t** — how far that is from chance.
- **Top minus bottom tenth** — the average return gap over five sessions.
- **Top-tenth hit rate** — how often the top tenth beat the average stock.

Every live ranking is stored, and once its five sessions pass it is scored the
same way, so the live record accumulates beside the backtest.

### What the research says to expect

- Cross-sectional models earn small edges: a rank IC of 0.02–0.05 is good.
- LLM agents' trading returns are largely market and style exposure rather
  than stock-picking skill once that is controlled for ("From Knowing to
  Doing", 2026). Paper performance therefore reports beta and alpha.
- An LLM ranking the Russell 1000 daily from live research found real alpha,
  but only in its top picks (Agentic AI Nowcasting, 2026). The model's
  strongest picks get an AI outlook each morning, scored on the AI track
  record, so the two can be compared.
- LLMs can memorise historical prices, so any backtest of an LLM is suspect.
  Bellwether's AI is only ever scored forward, on predictions logged before
  the outcome.
- Finance-specific time-series foundation models (Kronos, MIT licensed)
  forecast candles better than general ones. They need PyTorch and more memory
  than a 4 GB host spares, so Kronos is not bundled; its forecast would enter
  this model as one more factor, judged the same way.

## Paper trading

Code: `internal/paper` (rules), `internal/storage/postgres/paper.go` (ledger),
`cmd/tradesys/paper.go` (wiring and the execution loop).

### The ledger

Money is whole cents. Every movement is a ledger transaction whose postings
sum to zero across five accounts — `cash`, `securities` (at cost), `fees`,
`realized_pnl` and `external` (the outside world). Any balance can be rebuilt
from the postings; the books cannot fail to balance, because an unbalanced
transaction is refused.

Deposits and withdrawals are payments with a lifecycle: `created` →
`processing` → `succeeded` | `failed` | `canceled`. The ledger is written once,
on success, and a repeated notification changes nothing. Each request carries
an idempotency key, so a double click or a retried request moves money once.
The simulated provider approves at once; a card processor would return
`processing` and report the outcome by webhook, applied by the same code.

### Execution rules

- Market orders fill during the regular session (9:30–16:00 New York) at the
  latest price plus slippage (default 5 basis points). Placed while closed,
  they fill at the next open. Holidays are not modelled.
- Limit orders fill at the limit or better when a five-minute bar trades
  through it; stops trigger when touched and fill like market orders — at the
  open if the price gaps through.
- Sales pay the SEC Section 31 fee and FINRA's TAF; commission is configurable.
- A buy reserves its worst-case cost, so two orders cannot spend the same cash.
- No short selling or margin. Accounts under $25,000 are held to FINRA's
  pattern-day-trader rule.
- A buy may carry a stop loss and a take profit, placed on fill as
  one-cancels-other exits. Day orders expire at the close; good-till-canceled
  orders after ninety days.
- Every sale also lands in the journal, attributed to its news.

### Agents

| Kind | Decides by |
| --- | --- |
| AI | the text model, from the account, holdings and candidates (price action, headlines, scanner signals, the forecast model's rank) |
| Algorithm | a saved rule: enter when it holds on a symbol not held, exit on an exit rule or the stop and target |
| Webhook | your own code: the snapshot is POSTed as JSON, signed with HMAC-SHA256 in `X-Bellwether-Signature`; reply `{"summary": "...", "intents": [...]}` |

Every agent is bound by the wallet's limits — position size, daily loss,
drawdown (which halts it), trades a day, minimum price — and every run is
logged with what it saw, what it wanted, what was placed and what the limits
refused.

### Skill or luck

Performance is read against the S&P 500 with beta and alpha, week by week
against the wallet's goal, and against 1,000 random traders who made the same
number of trades, held as long, in random stocks from the universe over the
same period. Beating 95% of them is the bar for evidence of skill.

A weekly goal is tracked, never chased. Ten percent a week compounds to more
than 14,000% a year; no strategy does that reliably, and one that tries takes
risks that end accounts.
