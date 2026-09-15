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

let displayTimeZone = "Asia/Kolkata";

/** Sets the zone every timestamp is rendered in. Called once from meta. */
export function setDisplayTimeZone(tz: string): void {
  displayTimeZone = tz;
}

/** An absolute timestamp in the display zone. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return EM_DASH;
  return d.toLocaleString("en-GB", {
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
