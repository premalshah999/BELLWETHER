# The algorithm language

An algorithm is a JSON document:

```json
{
  "name": "Momentum watch",
  "symbols": ["AAPL", "MSFT"],
  "interval": "1d",
  "all": [
    {"indicator": "rsi", "period": 14, "op": "<", "value": 35},
    {"indicator": "close", "op": ">", "compare": {"indicator": "sma", "period": 200}},
    {"indicator": "volume", "op": ">", "compare": {"indicator": "vol_avg", "period": 20, "mult": 1.5}}
  ],
  "cooldown_hours": 24,
  "notify": {"telegram": true, "ai_context": true}
}
```

**Indicators:** `close`, `open`, `high`, `low`, `volume`, `sma`, `ema`, `rsi`,
`atr`, `vol_avg`, `vwap`, `session_vwap`, `macd`, `macd_signal`, `macd_hist`,
`high_52w`, `low_52w`. **Operators:** `<` `<=` `>` `>=` `==` `!=`
`crosses_above` `crosses_below`. **Groups:** `all` (AND) or `any` (OR),
nestable one level deep. **Operand modifiers:** `mult` scales a value, `offset`
adds to it, `shift` reads it *n* bars back.

## Three rules the evaluator will not bend

**Missing data is never zero.** An indicator with no value yet makes its
condition *unknown*, and unknown never fires an alert.

**Unknown is a third truth value, combined under Kleene logic.** A definite
`false` settles an AND regardless of unknown siblings; a definite `true`
settles an OR.

**A crossover is not a comparison.** `crosses_above` requires the previous
bar at or below and the current bar strictly above — conflating it with `>`
turns a one-off signal into a daily one.

## Backtesting

Any algorithm can be backtested over stored daily history.

- **Signals fill at the next bar's open.** An entry or exit rule reads a
  bar's close, and the close is not knowable until the bar has closed.
- **Stops and targets fill inside the bar**, checked against its high and
  low, because a stop is an order resting in the market. A bar that touches
  both is assumed to hit the stop and is flagged ambiguous, since daily OHLC
  cannot say which came first.
- **Costs default to 3 bps a side** for US equities (commission is usually
  zero; this is slippage, deliberately unkind).
- **Results carry warnings** — too few trades, too few bars — so eleven
  trades are described as eleven trades, not as a Sharpe ratio.
