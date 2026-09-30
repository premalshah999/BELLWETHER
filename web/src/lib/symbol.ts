/**
 * Symbols are US tickers (AAPL, BRK-B). A benchmark index carries an
 * ".INDEX" suffix (GSPC.INDEX), the only dot a symbol can hold.
 */

/** The ticker portion: GSPC.INDEX -> GSPC, AAPL -> AAPL. */
export function tickerOf(symbol: string): string {
  const i = symbol.indexOf(".");
  return i === -1 ? symbol : symbol.slice(0, i);
}
