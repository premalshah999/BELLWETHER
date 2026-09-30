import { useQuery } from "@tanstack/react-query";
import { CalendarClock } from "lucide-react";
import { useMemo } from "react";
import { useNavigate } from "react-router-dom";
import { api, type UpcomingCatalyst } from "../../lib/api";
import { useUrlState } from "../../lib/url";
import { PageHeader, Segmented, SkeletonRows } from "../ui/controls";


/** The holding period the base rate is measured over. */
const BASE_RATE_DAYS = 5;

const KINDS = [
  { value: "all", label: "Everything" },
  { value: "earnings", label: "Earnings" },
  { value: "dividends", label: "Dividends" },
] as const;

function kindName(k: string | undefined): string {
  const s = (k ?? "scheduled").replace(/_/g, " ").toLowerCase();
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/**
 * Labelled from the calendar date alone. "Days away" is computed on the
 * server's clock and the date on the exchange's, so near midnight the two
 * disagreed and two different dates were both headed "Today".
 */
function dayHeading(iso: string | undefined): string {
  if (!iso) return "Unscheduled";
  const key = iso.slice(0, 10);
  const local = (offsetDays: number) =>
    new Date(Date.now() + offsetDays * 86_400_000).toLocaleDateString("en-CA", { timeZone: "America/New_York" });
  if (key === local(0)) return "Today";
  if (key === local(1)) return "Tomorrow";
  return new Date(`${key}T12:00:00Z`).toLocaleDateString("en-US", { weekday: "long", month: "short", day: "numeric", timeZone: "UTC" });
}

export function CalendarPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const navigate = useNavigate();
  const [horizon, setHorizon] = useUrlState<"7" | "14" | "30" | "60">("days", "14");
  const [kind, setKind] = useUrlState<(typeof KINDS)[number]["value"]>("kind", "all");
  const days = Number(horizon);

  const { data, isLoading } = useQuery({
    queryKey: ["calendar", days],
    queryFn: () => api.calendar({ days, limit: 400 }),
    staleTime: 10 * 60_000,
  });
  // A calendar without a base rate is still a calendar, so a failure here
  // renders as absence rather than as an error.
  const { data: baseRate } = useQuery({
    queryKey: ["eventstudy", "EARNINGS", BASE_RATE_DAYS],
    queryFn: () => api.eventStudy("EARNINGS", BASE_RATE_DAYS),
    staleTime: 60 * 60_000,
    retry: false,
  });

  const rows = useMemo(() => {
    const all = data?.catalysts ?? [];
    if (kind === "all") return all;
    return all.filter((c) => {
      const k = (c.next_kind ?? "").toLowerCase();
      return kind === "earnings" ? k.includes("earn") : k.includes("div");
    });
  }, [data, kind]);

  const groups = useMemo(() => {
    const out: { key: string; label: string; items: UpcomingCatalyst[] }[] = [];
    for (const c of rows) {
      const key = c.next_date?.slice(0, 10) ?? "none";
      let g = out[out.length - 1];
      if (!g || g.key !== key) {
        g = { key, label: dayHeading(c.next_date), items: [] };
        out.push(g);
      }
      g.items.push(c);
    }
    return out;
  }, [rows]);

  const earnings = (data?.catalysts ?? []).filter((c) => c.earnings_date && (c.earnings_in_days ?? 999) <= days).length;
  const open = (symbol: string) => {
    onSelect(symbol);
    navigate("/charts");
  };

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <PageHeader
        title="Catalysts"
        subtitle={
          isLoading
            ? "Loading the calendar…"
            : `${rows.length} scheduled events in the next ${days} days, ${earnings} of them earnings reports.`
        }
      >
        <Segmented
          label="Horizon"
          value={horizon}
          onChange={setHorizon}
          options={[
            { value: "7", label: "7 days" },
            { value: "14", label: "14 days" },
            { value: "30", label: "30 days" },
            { value: "60", label: "60 days" },
          ]}
        />
        <Segmented label="Kind" value={kind} onChange={setKind} options={KINDS} />
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <BaseRate result={baseRate} />
        {isLoading ? (
          <SkeletonRows count={8} height={48} />
        ) : rows.length === 0 ? (
          <div className="flex max-w-md flex-col items-start gap-3 px-6 py-12">
            <CalendarClock size={22} className="text-text-muted" />
            <p className="font-reading text-display text-text-primary">Nothing scheduled in this window.</p>
            <p className="text-ui text-text-secondary">The calendar refreshes each weekday morning. Try a longer horizon.</p>
          </div>
        ) : (
          <div className="pb-10">
            {groups.map((g) => (
              <section key={g.key}>
                <h2 className="sticky top-0 z-10 flex items-baseline justify-between border-y border-border-subtle bg-bg-panel/95 px-5 py-2 backdrop-blur md:px-6">
                  <span className="text-ui font-semibold text-text-primary">{g.label}</span>
                  <span className="text-meta text-text-muted">{g.items.length}</span>
                </h2>
                <ul>
                  {g.items.map((c) => (
                    <Row key={c.symbol + (c.next_kind ?? "")} c={c} onOpen={open} />
                  ))}
                </ul>
              </section>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function BaseRate({
  result,
}: {
  result: { samples: number; mean_abnormal_return_pct: number; hit_rate: number } | undefined;
}) {
  if (!result || result.samples === 0) return null;
  const mean = result.mean_abnormal_return_pct;
  return (
    <div className="border-b border-border-subtle px-5 py-4 md:px-6">
      <p className="font-reading max-w-3xl text-[17px] leading-snug text-text-primary">
        After past earnings reports, stocks moved{" "}
        <span className={mean > 0 ? "text-semantic-up" : mean < 0 ? "text-semantic-down" : ""}>
          {mean >= 0 ? "+" : "−"}
          {Math.abs(mean).toFixed(2)}%
        </span>{" "}
        against the market over the next {BASE_RATE_DAYS} days, and beat it {result.hit_rate.toFixed(0)}% of the time.
      </p>
      <p className="mt-1 text-meta text-text-muted">
        {result.samples.toLocaleString()} reports measured from when Bellwether learned of each one. A base rate, not a forecast.
        {result.samples < 30 ? " Too few cases to lean on yet." : ""}
      </p>
    </div>
  );
}

/**
 * The analysts' range for EPS, drawn to scale.
 *
 * The width is the information: a wide spread before a print is a
 * disagreement, and a disagreement is what makes a print worth watching.
 */
function EpsRange({ c }: { c: UpcomingCatalyst }) {
  if (c.eps_average == null) return <span className="text-text-muted">No estimate</span>;
  const lo = c.eps_low ?? c.eps_average;
  const hi = c.eps_high ?? c.eps_average;
  const span = hi - lo;
  const rel = Math.abs(c.eps_average) > 0.01 ? span / Math.abs(c.eps_average) : 0;
  // Width is the spread relative to the estimate: a range half as wide as
  // the number itself fills the track.
  const width = Math.max(4, Math.min(100, rel * 200));
  return (
    <span className="flex items-center gap-3" title={`Analysts range ${lo.toFixed(2)} to ${hi.toFixed(2)}`}>
      <span className="w-12 text-right font-medium text-text-primary">{c.eps_average.toFixed(2)}</span>
      <span className="relative h-1.5 w-20 rounded-full bg-bg-base max-sm:hidden" aria-hidden="true">
        <span
          className={"absolute inset-y-0 rounded-full " + (rel > 0.25 ? "bg-brand" : "bg-border-focus")}
          style={{ left: `${50 - width / 2}%`, width: `${width}%` }}
        />
      </span>
      <span className="text-meta text-text-muted max-md:hidden">
        {lo.toFixed(2)} to {hi.toFixed(2)}
      </span>
    </span>
  );
}

function Row({ c, onOpen }: { c: UpcomingCatalyst; onOpen: (symbol: string) => void }) {
  const earningsFirst = (c.next_kind ?? "").toLowerCase().includes("earn");
  return (
    <li>
      <button
        type="button"
        onClick={() => onOpen(c.symbol)}
        className="grid w-full grid-cols-[4.5rem_1fr_auto] items-center gap-4 border-b border-border-subtle px-5 py-3 text-left text-ui transition-colors hover:bg-bg-panel-hover md:grid-cols-[5rem_9rem_minmax(0,1fr)_auto_12rem] md:px-6"
      >
        <span className="font-semibold text-text-primary">{c.symbol}</span>
        <span className={earningsFirst ? "font-medium text-text-primary" : "text-text-secondary"}>{kindName(c.next_kind)}</span>
        <span className="min-w-0 truncate text-meta text-text-muted max-md:col-span-3 max-md:col-start-2 max-md:row-start-2">
          {!earningsFirst && c.earnings_date ? `Reports earnings in ${c.earnings_in_days} days` : ""}
        </span>
        <span className="max-md:col-start-3 max-md:row-start-1">
          <EpsRange c={c} />
        </span>
        <span className="truncate text-right text-meta text-text-muted max-md:hidden">{c.industry}</span>
      </button>
    </li>
  );
}
