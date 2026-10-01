# Forecast and paper trading

Two features that turn Bellwether from describing what happened into testing
what will: an engine that forecasts the range of every stock's next 5, 10 and
20 sessions, and paper accounts where a strategy — yours, an algorithm, or an
AI — trades real prices with simulated money until the record says whether it
works.

## The forecast engine

Code: `internal/forecast`, run by `cmd/tradesys/forecast.go` after each close
(16:40 New York) and by hand with `-forecast`.

A forecast here is a distribution, not a number. Daily stock returns are
close to unpredictable in direction and highly predictable in size, so the
engine puts its effort where the evidence is: how wide the range is, how fat
its tails are, and how much a report inside the window widens it.

### Volatility

Two models, blended by which forecast better in earlier years (QLIKE loss,
Patton 2011):

- **HAR** (Corsi, 2009): the next 5 and 20 sessions' variance regressed on
  the stock's own variance over the last day, week, month and year, the
  market's monthly variance and the variance VIX implies. Fitted pooled across
  the universe on range-based variance (Garman and Klass, 1980, plus the
  overnight gap), in logs, with Duan's smearing correction.
- **GJR-GARCH(1,1)** with Student-t errors (Glosten, Jagannathan and Runkle,
  1993), fitted per stock by maximum likelihood, with variance targeting.

Earnings days are left out of both, so a report does not teach either model
that the stock is volatile for the month after it. The report comes back in
the simulation as a jump.

### The market

A two-state Gaussian hidden Markov model of the S&P 500's daily returns
(Hamilton, 1989), fitted by Baum-Welch and read forward only: a calm state and
a stressed one, the odds of switching, and today's probability of each.

### Expected return

27 published stock factors (momentum, reversal, volatility, beta, lottery
demand, skewness, illiquidity, volume, the 52-week high, moving averages,
earnings surprise and timing, sector momentum, seasonality, insider buying
and selling, overnight returns) are ranked across the universe each week.
A ridge regression and gradient-boosted trees (histogram method, depth
three, heavy regularisation, early stopping on a later slice of the training
window) each learn the next 5 and 20 sessions' abnormal-return ranks; the
trees also see three market-state inputs (S&P 500 volatility, VIX, its last
month), so they can learn when a factor works. The two are blended by their
out-of-sample record in earlier years.

The ranking becomes an expected return only through what such rankings
actually earned: a Fama-MacBeth slope of realised abnormal return on the
score's rank, estimated on earlier out-of-sample years, with Newey-West
errors, and shrunk to nothing when its t statistic is weak.

### Simulation

For each stock, thousands of paths through the next 20 sessions:

    stock = expected return + beta x market + own shock (+ earnings jump)

The market path moves between the two regimes; its shocks are the S&P 500's
own past ones, standardised and rescaled to today's forecast variance. Each
stock's shocks are its own past idiosyncratic ones, redrawn the same way
(filtered historical simulation, Barone-Adesi et al., 1999), so its skew and
fat tails carry through. A report inside the window adds a jump drawn from
that company's past earnings-day moves, rescaled to today's volatility, or its
sector's when it has fewer than eight on record. All stocks share the same
market paths, so the probability of beating the S&P 500 is measured against
the same simulated markets.

### Validation

Every year with three years of history before it is forecast by models
fitted only on those years, then scored:

- **CRPS** (continuous ranked probability score) of each distribution
  against a normal curve on 60-day volatility and against the stock's own
  past year of returns, with Diebold-Mariano tests on the weekly differences.
- **Coverage**: the share of outcomes inside the central 50, 80 and 90%
  ranges, and the PIT histogram (where outcomes fell inside their own
  forecast; flat is calibrated).
- **Brier scores** of the probability of rising and of beating the S&P 500,
  against always forecasting the base rate, with reliability diagrams.
- **QLIKE** of the volatility models against assuming next week looks like
  last month.
- **Rank IC** of the ranking, its parts, and momentum alone.

Ranges and probabilities are recalibrated before they are shown, from the
misses of earlier years: quantile levels move halfway toward the level that
held its share of outcomes (Kuleshov et al., 2018), and probabilities pass
through an isotonic map. Every live forecast is stored and scored the same
way once its window passes.

### What the record shows

Tested on 1,494 stocks, January 2020 to September 2026 (seven years, each
forecast by models fitted only on the years before it), with the full
distribution scored for an even sample of 400 stocks every week:

| Over | CRPS against a bell curve | against the stock's own history | 50 / 80 / 90% ranges held |
| --- | --- | --- | --- |
| 5 sessions | 2.4% better (t = 4.9) | 2.9% better (t = 4.2) | 51 / 81 / 90% |
| 10 sessions | 1.6% better (t = 2.7) | 2.6% better (t = 4.4) | 52 / 82 / 91% |
| 20 sessions | 1.1% better (t = 1.5) | 4.6% better (t = 5.3) | 52 / 82 / 92% |

The five-session distribution beat the bell curve in every one of the seven
years. The volatility forecast cut QLIKE loss by 18% (5 sessions) and 24% (20
sessions) against assuming the next weeks look like the last month; HAR beat
GARCH, and the blend leans three to one toward HAR.

Direction is another matter. The probability of rising and of beating the
S&P 500 scored no better than the base rate, and the ranking's rank IC was
indistinguishable from zero (0.004 over 5 sessions, t = 0.5), as was
momentum's alone. Of the 27 factors, two held up on their own over the seven
years, both about earnings: the latest surprise (post-earnings drift, IC
+0.016, t = 3.0) and the approach of the next report (the announcement
premium, t = 2.8). Neither held steadily enough within a three-year training
window for the models to learn it reliably. The engine therefore shrinks its
expected returns to about zero, and the page says the ranking has no proven
edge.

The things that mattered most were found by the test, not assumed:
stretches of stale, unchanged prices in older data (dropped before modelling,
because a feed carrying a quote forward reads as zero volatility); single
days beyond a 25% move outside earnings, which are almost always corporate
actions such as spin-offs (capped in the volatility inputs); a company's own
past earnings moves, used as they happened rather than rescaled by a jumpy
month; and the calibration window (two years, because ranges miss
differently after a crash than after a calm).

### Limits

- The history is today's index members traced back. Companies that were
  dropped or failed are missing, which flatters past returns (and so the
  ranking) more than volatility or ranges.
- Earnings dates inside a test window are taken as known; companies publish
  them weeks ahead.
- Direction is close to a coin toss and the page says so. A probability of
  beating the market near 50% is the honest answer for most stocks.

### The AI outlook

An outlook starts from the engine's distribution for that stock over ten
sessions. Bull and bear are fixed per stock as moves beyond one normal-sized
move (its past year's volatility), so their probabilities change with
volatility, the market's regime and reports inside the window. The AI reads
the news against the distribution and may shift it by up to half a standard
deviation or widen it between 0.7 and 1.6 times, and only when it names the
item the engine cannot see. The engine's probabilities and the published ones
are both scored against the same outcome, so the track record shows whether
the adjustments help.

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
