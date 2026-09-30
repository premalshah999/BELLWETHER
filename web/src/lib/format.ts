/**
 * Time formatting.
 *
 * Two rules run through this. Times are stored in UTC and displayed in the
 * operator's zone, and a missing value renders as an em dash rather than a
 * zero — a blank is honest, a zero is a claim.
 *
 * Deliberately small. An earlier version carried a formatter for every kind of
 * number in the application; almost all of them were used once or never, and a
 * helper that exists for one caller is harder to find than the two lines it
 * replaces. Prices and percentages are formatted where they are rendered.
 */

const EM_DASH = "—";

let displayTimeZone = "America/New_York";

/** Sets the zone every timestamp is rendered in. Called once from meta. */
export function setDisplayTimeZone(tz: string): void {
  displayTimeZone = tz;
}

/** An absolute timestamp in the display zone. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return EM_DASH;
  return d.toLocaleString("en-US", {
    timeZone: displayTimeZone,
    day: "2-digit",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

/**
 * How long ago, compactly.
 *
 * Coarse on purpose: past an hour, the minute is noise, and a feed of
 * "2h ago" reads faster than one of "2h 14m ago". Future timestamps render as
 * "now" rather than a negative age, because the only thing that produces them
 * is a clock disagreement and a negative age looks like a bug in the data.
 */
export function formatAgo(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return EM_DASH;

  const seconds = Math.floor((Date.now() - then) / 1000);
  if (seconds < 0) return "now";
  if (seconds < 60) return `${seconds}s ago`;

  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;

  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;

  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;

  const months = Math.floor(days / 30);
  if (months < 12) return `${months}mo ago`;

  return `${Math.floor(months / 12)}y ago`;
}

function dayKey(d: Date): string {
  return d.toLocaleDateString("en-CA", { timeZone: displayTimeZone });
}

/** A clock time in the display zone: 14:32. */
export function formatClock(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return EM_DASH;
  return d.toLocaleTimeString("en-US", {
    timeZone: displayTimeZone,
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

/** The calendar day an instant falls on in the display zone, as a stable key. */
export function dayOf(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : dayKey(d);
}

/** "Today", "Yesterday", or "Mon, Sep 27" — for grouping a feed by day. */
export function formatDay(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return EM_DASH;
  const today = dayKey(new Date());
  const yesterday = dayKey(new Date(Date.now() - 86_400_000));
  const k = dayKey(d);
  if (k === today) return "Today";
  if (k === yesterday) return "Yesterday";
  return d.toLocaleDateString("en-US", {
    timeZone: displayTimeZone,
    weekday: "short",
    month: "short",
    day: "numeric",
  });
}

/** A span of seconds, briefly: 45s, 12m, 3h 5m, 2d. */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds)) return EM_DASH;
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  return `${Math.floor(h / 24)}d`;
}
