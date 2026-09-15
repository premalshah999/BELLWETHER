/**
 * Reading a canonical symbol apart -- ticker and venue.
 *
 * A canonical symbol is what the API returns and expects everywhere:
 * RELIANCE.NSE, AAPL. Before this existed, three components each grew their
 * own `symbol.split(".")[0]`, and three others each grew their own
 * `${symbol}.NSE` to qualify a bare ticker the API had in fact already
 * qualified -- producing RELIANCE.NSE.NSE the moment the backend's own
 * output stopped being bare. One rule, defined once, is what keeps that from
 * happening again.
 */

import type { Venue } from "./session";

/** The ticker portion: RELIANCE.NSE -> RELIANCE, AAPL -> AAPL. */
export function tickerOf(symbol: string): string {
  const i = symbol.indexOf(".");
  return i === -1 ? symbol : symbol.slice(0, i);
}

/**
 * The venue a canonical symbol trades on. Indian venues (NSE, BSE) are both
 * read as "NSE" -- the rest of the app already treats the two as one venue
 * for display purposes (session hours, currency, locale). Anything without
 * that suffix, including a bare US ticker, is US.
 */
export function venueOf(symbol: string): Venue {
  return symbol.endsWith(".NSE") || symbol.endsWith(".BSE") ? "NSE" : "US";
}

/** IANA timezone for a canonical symbol's own venue. */
export function timeZoneOf(symbol: string): string {
  return venueOf(symbol) === "NSE" ? "Asia/Kolkata" : "America/New_York";
}

/**
 * Qualifies a bare ticker the API returned unqualified in some older
 * responses, or leaves an already-canonical symbol untouched. Prefer using
 * the symbol the API gave you directly; this exists for the places that
 * historically appended a suffix blindly and need a safe replacement.
 */
export function ensureQualified(symbol: string, venue: Venue = "NSE"): string {
  if (symbol.includes(".")) return symbol;
  return venue === "NSE" ? `${symbol}.NSE` : symbol;
}
