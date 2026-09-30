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
 * How far behind the newest bar is, and whether that is the feed or the app.
 *
 * Free intraday data runs about fifteen minutes behind the market. A chart
 * that silently lags reads as broken; one that says how old its newest bar
 * is reads as delayed, which is the truth.
 */
export function BarAge({
  bar,
  interval,
  fetchedAt,
  venue = "US",
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
  if (!sessionState(venue).open && !daily) {
    return (
      <span className="font-mono text-micro text-text-muted" title={label(fetchedAt)}>
        Market closed
      </span>
    );
  }
  if (minutes < span) return null;

  // The feed's usual delay. Beyond it the feed has stalled rather than
  // merely being behind, and the two deserve different words.
  const stalled = !daily && minutes > 25;
  return (
    <span
      className={"font-mono text-micro " + (stalled ? "text-semantic-down" : "text-brand")}
      title={
        stalled
          ? `The newest bar is ${Math.round(minutes)} minutes old, longer than the feed’s usual fifteen-minute delay. ${label(fetchedAt)}`
          : `Intraday prices arrive about fifteen minutes behind the market. ${label(fetchedAt)}`
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
