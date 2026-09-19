import { useQuery } from "@tanstack/react-query";
import { CalendarClock } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type UpcomingCatalyst } from "../../lib/api";
import { venueOf } from "../../lib/symbol";
import { Empty } from "../ui/Empty";
import { Panel } from "../ui/Panel";
import { Pill } from "../ui/Pill";

const HORIZONS = [7, 14, 30, 60] as const;

/** The holding period the base rate is measured over. */
const BASE_RATE_DAYS = 5;

function dayLabel(iso: string | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function awayLabel(days: number | undefined): string {
  if (days == null) return "";
  if (days === 0) return "today";
  if (days === 1) return "tomorrow";
  return `${days}d`;
}

/** Urgency is a property of proximity, so it is drawn rather than stated. */
function tone(days: number | undefined): "brand" | "neutral" | "muted" {
  if (days == null) return "muted";
  if (days <= 3) return "brand";
  if (days <= 10) return "neutral";
  return "muted";
}

/**
 * What is scheduled, and what that kind of event has done before.
 *
 * Every other page in this app reports what already happened. This one is
 * the other half of the same discipline: the archive knows, from
 * discovered_at and nothing else, how a given event type has moved prices
 * historically -- so a date on a calendar can carry a base rate instead of
 * only a date. A calendar alone is a commodity; a calendar that says "and
 * the last N times this happened, here is what followed" is not.
 *
 * The base rate is fetched once for the page rather than per row, because it
 * belongs to the event type and not to the symbol. See internal/eventstudy.
 */
export function CalendarPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const [days, setDays] = useState<number>(30);
  const [venue, setVenue] = useState<"all" | "US" | "NSE">("all");

  const { data, isLoading } = useQuery({
    queryKey: ["calendar", days],
    queryFn: () => api.calendar({ days, limit: 400 }),
    staleTime: 10 * 60_000,
  });

  // The historical half. Failure here is not failure of the page: a calendar
  // without a base rate is still a calendar, so this renders as absent
  // rather than as an error.
  const { data: baseRate } = useQuery({
    queryKey: ["eventstudy", "EARNINGS", BASE_RATE_DAYS],
    queryFn: () => api.eventStudy("EARNINGS", BASE_RATE_DAYS),
    staleTime: 60 * 60_000,
    retry: false,
  });

  const rows = useMemo(() => {
    const all = data?.catalysts ?? [];
    if (venue === "all") return all;
    return all.filter((c) => (venueOf(c.symbol) === "NSE" ? "NSE" : "US") === venue);
  }, [data, venue]);

  const earningsCount = rows.filter((r) => r.earnings_date).length;

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <div className="flex h-10 shrink-0 flex-wrap items-center gap-3 border-b border-border-subtle px-4">
        <span className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
          Catalyst Calendar
        </span>

        <div className="flex items-center gap-1">
          {HORIZONS.map((d) => (
            <button
              key={d}
              type="button"
              onClick={() => setDays(d)}
              className={
                "px-1.5 py-0.5 font-mono text-micro transition-colors " +
                (days === d
                  ? "bg-brand-muted text-brand"
                  : "text-text-muted hover:text-text-primary")
              }
            >
              {d}d
            </button>
          ))}
        </div>

        <div className="flex items-center gap-1">
          {(["all", "US", "NSE"] as const).map((v) => (
            <button
              key={v}
              type="button"
              onClick={() => setVenue(v)}
              className={
                "px-1.5 py-0.5 font-mono text-micro uppercase transition-colors " +
                (venue === v
                  ? "bg-brand-muted text-brand"
                  : "text-text-muted hover:text-text-primary")
              }
            >
              {v}
            </button>
          ))}
        </div>

        <span className="ml-auto font-mono text-meta text-text-muted">
          {rows.length} scheduled
        </span>
      </div>

      <BaseRate result={baseRate} count={earningsCount} horizon={days} />

      {rows.length === 0 && !isLoading ? (
        <Empty
          icon={CalendarClock}
          title="Nothing scheduled in this window."
          hint="The calendar refreshes each weekday morning. Widen the horizon, or check back after the next refresh."
        />
      ) : (
        <Panel scroll className="min-h-0 flex-1 border-0">
          {isLoading ? (
            <p className="px-4 py-6 font-mono text-meta text-text-muted">loading…</p>
          ) : (
            <ul className="divide-y divide-border-subtle">
              {rows.map((c) => (
                <Row key={c.symbol} catalyst={c} onSelect={onSelect} />
              ))}
            </ul>
          )}
        </Panel>
      )}
    </div>
  );
}

function BaseRate({
  result,
  count,
  horizon,
}: {
  result: { samples: number; mean_abnormal_return_pct: number; hit_rate: number; warnings?: string[] } | undefined;
  count: number;
  horizon: number;
}) {
  if (!result || result.samples === 0) {
    return (
      <div className="border-b border-border-subtle px-4 py-3">
        <p className="text-meta leading-relaxed text-text-muted">
          {count} earnings date{count === 1 ? "" : "s"} in the next {horizon} days. No
          historical base rate yet — the event study needs daily bars covering past
          events of this type, which accumulate as the scanner runs.
        </p>
      </div>
    );
  }

  const mean = result.mean_abnormal_return_pct;
  const thin = result.samples < 30;

  return (
    <div className="border-b border-border-subtle px-4 py-3">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          Earnings, historically
        </span>
        <span
          className={
            "font-mono text-ui tabular-nums " +
            (mean > 0 ? "text-semantic-up" : mean < 0 ? "text-semantic-down" : "text-text-primary")
          }
        >
          {mean >= 0 ? "+" : ""}
          {mean.toFixed(2)}%
        </span>
        <span className="font-mono text-meta text-text-muted">
          mean abnormal return over {BASE_RATE_DAYS} days
        </span>
        <span className="font-mono text-meta text-text-muted">
          · {result.hit_rate.toFixed(0)}% positive
        </span>
        <span className="font-mono text-meta text-text-muted">· n={result.samples}</span>
        {thin && <Pill tone="muted">thin sample</Pill>}
      </div>
      <p className="mt-1 max-w-[80ch] text-meta leading-relaxed text-text-muted">
        Measured against the benchmark from each event&rsquo;s discovery time, never its
        publication time, so nothing here could have been known later than it was.
        This is what the type has done before — not a forecast for the dates below.
      </p>
    </div>
  );
}

function Row({
  catalyst,
  onSelect,
}: {
  catalyst: UpcomingCatalyst;
  onSelect: (symbol: string) => void;
}) {
  const c = catalyst;
  const spread =
    c.eps_low != null && c.eps_high != null ? c.eps_high - c.eps_low : null;

  return (
    <li className="px-4 py-2.5">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <button
          type="button"
          onClick={() => onSelect(c.symbol)}
          className="text-ui text-text-primary hover:text-brand"
        >
          {c.symbol}
        </button>

        <Pill tone={tone(c.next_in_days)}>
          {c.next_kind ?? "scheduled"} {dayLabel(c.next_date)}
          {c.next_in_days != null && ` · ${awayLabel(c.next_in_days)}`}
        </Pill>

        {/* Shown only when it is not already the headline date, so a row
            never repeats itself. */}
        {c.earnings_date && c.next_kind !== "earnings" && (
          <span className="font-mono text-meta text-text-muted">
            earnings {dayLabel(c.earnings_date)}
            {c.earnings_in_days != null && ` (${awayLabel(c.earnings_in_days)})`}
          </span>
        )}

        {c.eps_average != null && (
          <span className="font-mono text-meta text-text-muted">
            est. EPS {c.eps_average.toFixed(2)}
            {/* The width of the analyst range is the information: a wide
                spread before a print is a disagreement, and a disagreement
                is what makes the print worth watching. */}
            {spread != null && spread > 0 && (
              <span className="text-text-muted">
                {" "}
                ({c.eps_low?.toFixed(2)}–{c.eps_high?.toFixed(2)})
              </span>
            )}
          </span>
        )}

        {c.industry && (
          <span className="ml-auto font-mono text-meta text-text-muted">{c.industry}</span>
        )}
      </div>
    </li>
  );
}
