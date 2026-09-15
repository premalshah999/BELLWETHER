/**
 * Whether a market is open, and when it next is.
 *
 * The interface previously said "LIVE" at all hours and showed a frozen
 * screen outside them, which is the single most misleading thing it could
 * do: a reader cannot tell a closed market from a broken feed, and both look
 * like nothing changing.
 *
 * Mirrors the server's session model rather than inventing a second one —
 * NSE 09:15–15:30 IST, US 09:30–16:00 ET, weekdays. Holidays are not
 * modelled here or on the server; a holiday reads as open-with-no-ticks,
 * which is a smaller error than pretending to know every exchange calendar.
 */

export type Venue = "NSE" | "US";

interface Window {
  tz: string;
  open: [number, number];
  close: [number, number];
}

const WINDOWS: Record<Venue, Window> = {
  NSE: { tz: "Asia/Kolkata", open: [9, 15], close: [15, 30] },
  US: { tz: "America/New_York", open: [9, 30], close: [16, 0] },
};

export interface SessionState {
  venue: Venue;
  open: boolean;
  /** Minutes until the next open, when closed. */
  opensInMinutes?: number;
  /** Minutes until close, when open. */
  closesInMinutes?: number;
  localTime: string;
}

/** Wall-clock minutes and weekday in a given zone. */
function parts(tz: string, at: Date) {
  const f = new Intl.DateTimeFormat("en-GB", {
    timeZone: tz,
    hour: "2-digit",
    minute: "2-digit",
    weekday: "short",
    hour12: false,
  });
  const got: Record<string, string> = {};
  for (const p of f.formatToParts(at)) got[p.type] = p.value;
  const hour = Number(got.hour);
  const minute = Number(got.minute);
  return {
    minutes: hour * 60 + minute,
    weekday: got.weekday ?? "",
    clock: `${got.hour}:${got.minute}`,
  };
}

export function sessionState(venue: Venue, at: Date = new Date()): SessionState {
  const w = WINDOWS[venue];
  const { minutes, weekday, clock } = parts(w.tz, at);
  const openAt = w.open[0] * 60 + w.open[1];
  const closeAt = w.close[0] * 60 + w.close[1];
  const weekend = weekday === "Sat" || weekday === "Sun";

  if (!weekend && minutes >= openAt && minutes < closeAt) {
    return { venue, open: true, closesInMinutes: closeAt - minutes, localTime: clock };
  }

  // Minutes until the next open. Past today's close, or on a weekend, that is
  // the following weekday morning.
  let wait: number;
  if (weekend) {
    const daysToMonday = weekday === "Sat" ? 2 : 1;
    wait = daysToMonday * 24 * 60 - minutes + openAt;
  } else if (minutes < openAt) {
    wait = openAt - minutes;
  } else {
    // Friday evening waits until Monday.
    const days = weekday === "Fri" ? 3 : 1;
    wait = days * 24 * 60 - minutes + openAt;
  }
  return { venue, open: false, opensInMinutes: wait, localTime: clock };
}

/** "2h 14m", "38m", "3d 1h" — coarse enough to read at a glance. */
export function humanMinutes(total: number): string {
  if (total < 60) return `${total}m`;
  const hours = Math.floor(total / 60);
  if (hours < 24) {
    const m = total % 60;
    return m ? `${hours}h ${m}m` : `${hours}h`;
  }
  const days = Math.floor(hours / 24);
  const h = hours % 24;
  return h ? `${days}d ${h}h` : `${days}d`;
}
