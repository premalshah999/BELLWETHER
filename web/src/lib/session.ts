import { MARKET_ZONE } from "./format";

/**
 * Whether the US market is open, and when it next is. The interface once
 * said "LIVE" at all hours over a frozen screen, and a reader cannot tell a
 * closed market from a broken feed. Mirrors the server: regular hours,
 * 09:30-16:00 ET on weekdays. Holidays are not modelled; one reads as
 * open-with-no-ticks.
 */
const OPEN = 9 * 60 + 30;
const CLOSE = 16 * 60;

export interface SessionState {
  open: boolean;
  /** Minutes until the next open, when closed. */
  opensInMinutes?: number;
  /** Minutes until close, when open. */
  closesInMinutes?: number;
  /** The New York wall clock, HH:MM. */
  localTime: string;
}

export function sessionState(at: Date = new Date()): SessionState {
  const got: Record<string, string> = {};
  for (const p of new Intl.DateTimeFormat("en-GB", {
    timeZone: MARKET_ZONE,
    hour: "2-digit",
    minute: "2-digit",
    weekday: "short",
    hour12: false,
  }).formatToParts(at))
    got[p.type] = p.value;
  const minutes = Number(got.hour) * 60 + Number(got.minute);
  const weekday = got.weekday ?? "";
  const localTime = `${got.hour}:${got.minute}`;
  const weekend = weekday === "Sat" || weekday === "Sun";

  if (!weekend && minutes >= OPEN && minutes < CLOSE) {
    return { open: true, closesInMinutes: CLOSE - minutes, localTime };
  }
  // The next open: later today, or the next weekday morning.
  const days = weekend ? (weekday === "Sat" ? 2 : 1) : minutes < OPEN ? 0 : weekday === "Fri" ? 3 : 1;
  return { open: false, opensInMinutes: days * 24 * 60 - minutes + OPEN, localTime };
}

/** "2h 14m", "38m", "3d 1h": coarse enough to read at a glance. */
export function humanMinutes(total: number): string {
  if (total < 60) return `${total}m`;
  const hours = Math.floor(total / 60);
  if (hours < 24) return total % 60 ? `${hours}h ${total % 60}m` : `${hours}h`;
  const days = Math.floor(hours / 24);
  return hours % 24 ? `${days}d ${hours % 24}h` : `${days}d`;
}
