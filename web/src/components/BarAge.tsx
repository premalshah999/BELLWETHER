import { useEffect, useState } from "react";
import type { Candle, Interval } from "../lib/api";
import type { Venue } from "../lib/session";
import { sessionState } from "../lib/session";

/** How long one bar of each interval covers, in minutes. */
const SPAN: Record<Interval, number> = {
  "1m": 1,
  "5m": 5,
  "15m": 15,
  "1h": 60,
  "1d": 60 * 24,
  "1wk": 60 * 24 * 7,
};

/**
 * How far behind the newest bar is.
 *
 * The chart was described as "always lagging" and it is, but not for a reason
 * anything here can fix: Yahoo delays NSE intraday data by about fifteen
 * minutes, and — measured across five sessions — it stops every one of them at
 * 15:15 IST even though the exchange trades to 15:30. The last two five-minute
 * bars of every Indian session simply never arrive, so an intraday chart's
 * final close is not the session's close and is routinely several rupees away
 * from it.
 *
 * None of that is visible on a chart, which is what made it feel broken rather
 * than delayed. This states the gap: how old the newest bar is, and — once the
 * gap is wider than the upstream's own delay — that it is the feed and not the
 * app.
 */
export function BarAge({
  bar,
  interval,
  fetchedAt,
  venue = "NSE",
}: {
  bar: Candle | null;
  interval: Interval;
  /** When the request that produced these bars completed. */
  fetchedAt: number;
  venue?: Venue;
}) {
  // Ticks so the age keeps counting between fetches; a number that only moves
  // when the data does cannot show that the data has stopped moving.
  const [, tick] = useState(0);
  useEffect(() => {
    const t = window.setInterval(() => tick((n) => n + 1), 15_000);
    return () => window.clearInterval(t);
  }, []);

  if (!bar) return null;

  const span = SPAN[interval];
  // A bar is stamped at its start, so it is not late until a full bar has
  // passed since it opened. Anything inside that is the bar still forming.
  const minutes = (Date.now() - new Date(bar.t).getTime()) / 60_000 - span;
  const daily = interval === "1d" || interval === "1wk";

  // Outside market hours the newest bar is supposed to be old, and reporting a
  // seventeen-hour lag overnight would be noise rather than information.
  // The venue follows the symbol's own exchange: a US bar at 21:00 IST is
  // mid-session, and calling it closed because the NSE has gone home would be
  // wrong in exactly the hours a US chart is most used.
  if (!sessionState(venue).open && !daily) {
    return (
      <span className="font-mono text-micro text-text-muted" title={label(fetchedAt)}>
        session closed
      </span>
    );
  }
  if (minutes < span) return null;

  // Yahoo's own delay on NSE intraday. Beyond it the feed has stalled rather
  // than merely being behind, and the two deserve different words.
  const stalled = !daily && minutes > 25;
  return (
    <span
      className={"font-mono text-micro " + (stalled ? "text-semantic-down" : "text-brand")}
      title={
        stalled
          ? `The newest bar is ${Math.round(minutes)} minutes old. The upstream feed delays NSE intraday data by about fifteen minutes and stops each session at 15:15 IST, so the last quarter hour of a session never arrives. ${label(fetchedAt)}`
          : `The upstream feed delays NSE intraday data by about fifteen minutes. ${label(fetchedAt)}`
      }
    >
      {daily ? `${Math.round(minutes / (60 * 24))}d behind` : `${Math.round(minutes)}m behind`}
    </span>
  );
}

function label(fetchedAt: number) {
  if (!fetchedAt) return "";
  const secs = Math.round((Date.now() - fetchedAt) / 1000);
  return `Fetched ${secs < 90 ? `${secs}s` : `${Math.round(secs / 60)}m`} ago.`;
}
