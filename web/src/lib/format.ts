import { useSyncExternalStore } from "react";

/**
 * Time formatting. Instants are stored in UTC and shown in the zone the
 * viewer chose, and every clock time says which zone that is. A missing
 * value renders as an em dash: a blank is honest, a zero is a claim.
 */

const EM_DASH = "—";
const KEY = "bellwether.tz";

/** Where clock times are shown: New York (the market), the viewer's zone, or UTC. */
export type ZoneChoice = "market" | "local" | "utc";

export const MARKET_ZONE = "America/New_York";

function read(): ZoneChoice {
  try {
    const v = localStorage.getItem(KEY);
    return v === "local" || v === "utc" ? v : "market";
  } catch {
    return "market";
  }
}

let choice: ZoneChoice = read();
const listeners = new Set<() => void>();

/** The zone choice, and a setter that re-renders everything reading it. */
export function useTimeZone(): [ZoneChoice, (next: ZoneChoice) => void] {
  const value = useSyncExternalStore(
    (notify) => {
      listeners.add(notify);
      return () => listeners.delete(notify);
    },
    () => choice,
  );
  return [value, setTimeZone];
}

function setTimeZone(next: ZoneChoice) {
  choice = next;
  try {
    if (next === "market") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, next);
  } catch {
    /* private mode: lasts for this tab */
  }
  listeners.forEach((notify) => notify());
}

/** The IANA zone instants are shown in. */
export function zoneName(): string {
  if (choice === "utc") return "UTC";
  if (choice === "local") return Intl.DateTimeFormat().resolvedOptions().timeZone;
  return MARKET_ZONE;
}

/** The zone's short name at an instant: ET, UTC, or the viewer's own (PDT, IST, GMT+2). */
export function zoneAbbr(at: Date = new Date()): string {
  if (choice === "market") return "ET";
  if (choice === "utc") return "UTC";
  const part = new Intl.DateTimeFormat(undefined, { timeZone: zoneName(), timeZoneName: "short" })
    .formatToParts(at)
    .find((p) => p.type === "timeZoneName");
  return part?.value ?? "local";
}

function parse(iso: string | null | undefined): Date | null {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

function clock(d: Date): string {
  return d.toLocaleTimeString("en-US", { timeZone: zoneName(), hour: "2-digit", minute: "2-digit", hour12: false });
}

/** A clock time with its zone: 14:32 ET. */
export function formatClock(iso: string | null | undefined): string {
  const d = parse(iso);
  return d ? `${clock(d)} ${zoneAbbr(d)}` : EM_DASH;
}

/** A full timestamp with its zone: Sep 30, 2026, 14:32 ET. */
export function formatDateTime(iso: string | null | undefined): string {
  const d = parse(iso);
  if (!d) return EM_DASH;
  const date = d.toLocaleDateString("en-US", { timeZone: zoneName(), day: "numeric", month: "short", year: "numeric" });
  return `${date}, ${clock(d)} ${zoneAbbr(d)}`;
}

/**
 * A calendar date with no time of day (a trade date, a filing date, an
 * earnings date). It belongs to no zone, so it is never shifted into one.
 */
export function formatDate(
  iso: string | null | undefined,
  opts: Intl.DateTimeFormatOptions = { month: "short", day: "numeric", year: "numeric" },
): string {
  const d = parse(iso && iso.length === 10 ? `${iso}T12:00:00Z` : iso);
  return d ? d.toLocaleDateString("en-US", { ...opts, timeZone: "UTC" }) : EM_DASH;
}

/**
 * How long ago, compactly. Coarse on purpose: past an hour the minute is
 * noise. A future timestamp reads "now", because only a clock disagreement
 * produces one.
 */
export function formatAgo(iso: string | null | undefined): string {
  const d = parse(iso);
  if (!d) return EM_DASH;
  const seconds = Math.floor((Date.now() - d.getTime()) / 1000);
  if (seconds < 0) return "now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  const months = Math.floor(days / 30);
  return months < 12 ? `${months}mo ago` : `${Math.floor(months / 12)}y ago`;
}

function dayKey(d: Date): string {
  return d.toLocaleDateString("en-CA", { timeZone: zoneName() });
}

/** The calendar day an instant falls on in the display zone, as a stable key. */
export function dayOf(iso: string | null | undefined): string {
  const d = parse(iso);
  return d ? dayKey(d) : "";
}

/** "Today", "Yesterday", or "Mon, Sep 27": for grouping a feed by day. */
export function formatDay(iso: string | null | undefined): string {
  const d = parse(iso);
  if (!d) return EM_DASH;
  const k = dayKey(d);
  if (k === dayKey(new Date())) return "Today";
  if (k === dayKey(new Date(Date.now() - 86_400_000))) return "Yesterday";
  return d.toLocaleDateString("en-US", { timeZone: zoneName(), weekday: "short", month: "short", day: "numeric" });
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
