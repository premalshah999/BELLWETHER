/**
 * Reading a canonical symbol apart.
 *
 * Symbols are US tickers (AAPL, BRK-B). A benchmark index carries an
 * ".INDEX" suffix (GSPC.INDEX), which is the only dot a symbol can hold.
 */

import type { Venue } from "./session";

/** The ticker portion: GSPC.INDEX -> GSPC, AAPL -> AAPL. */
export function tickerOf(symbol: string): string {
  const i = symbol.indexOf(".");
  return i === -1 ? symbol : symbol.slice(0, i);
}

/** The venue a symbol trades on. Every symbol here is a US listing. */
export function venueOf(_symbol: string): Venue {
  return "US";
}

/** IANA timezone for a symbol's market. */
export function timeZoneOf(_symbol: string): string {
  return "America/New_York";
}
