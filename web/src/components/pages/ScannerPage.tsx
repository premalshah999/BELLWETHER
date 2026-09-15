import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Radar, RefreshCw } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type ScanFinding } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { Empty } from "../ui/Empty";
import { Pill } from "../ui/Pill";
import { useParams } from "react-router-dom";
import { ScreensView } from "./ScreensView";

// "all" first, and the default.
//
// "unexplained" is the more interesting subset and used to lead, but on a
// typical scan it is one finding out of thirty-nine — so the page opened
// almost blank and read as broken rather than as filtered. Everything is
// shown, the unexplained ones are marked, and the filter is one click away.
const VIEWS = ["all", "unexplained", "explained"] as const;

/** Sortable columns, and how to read each finding's value for one. */
const SORTS = {
  score: (f: ScanFinding) => f.score,
  return_1d: (f: ScanFinding) => f.return_1d,
  return_z: (f: ScanFinding) => Math.abs(f.return_z),
  volume_ratio: (f: ScanFinding) => f.volume_ratio,
  volume_z: (f: ScanFinding) => f.volume_z,
  pct_from_52w_high: (f: ScanFinding) => f.pct_from_52w_high,
} as const;

type SortKey = keyof typeof SORTS;

const SIGNAL_LABEL: Record<string, string> = {
  volume_spike: "volume",
  price_move: "price",
  gap: "gap",
  near_52w_high: "52w high",
  near_52w_low: "52w low",
  volume_without_price: "volume, no price",
};

/**
 * What the market did, before anyone wrote about it.
 *
 * Ordered around one claim: the interesting rows are the ones nothing
 * explains. Everything the archive already accounts for is real but finished,
 * so "unexplained" is the default rather than a filter to discover.
 */
export function ScannerPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  // The mode is the route. The tab strip that used to live here duplicated
  // the navigation and was the only way to discover that Screens existed.
  const { mode: routeMode } = useParams();
  const tab = routeMode === "screens" ? "screens" : "signals";
  const [view, setView] = useState(0);
  const [sort, setSort] = useState<SortKey>("score");
  const [desc, setDesc] = useState(true);
  const mode = VIEWS[view] ?? VIEWS[0];
  const qc = useQueryClient();

  const { data, isLoading } = useQuery({
    queryKey: ["scan-latest"],
    queryFn: () => api.scanLatest(60),
    refetchInterval: 120_000,
    placeholderData: (p) => p,
  });

  const run = useMutation({
    mutationFn: api.runScan,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["scan-latest"] }),
  });

  const findings = useMemo(() => data?.findings ?? [], [data]);
  const shown = useMemo(() => {
    let out = findings;
    if (mode === "unexplained") {
      // Absent is not "no". A finding whose archive check never ran belongs in
      // neither bucket.
      out = findings.filter((f) => f.explained === false);
    } else if (mode === "explained") {
      out = findings.filter((f) => f.explained === true);
    }
    const read = SORTS[sort];
    // Copied before sorting: the query cache hands back the same array on
    // every render, and sorting it in place mutates what React Query holds.
    return [...out].sort((a, b) => {
      const d = read(a) - read(b);
      return desc ? -d : d;
    });
  }, [findings, mode, sort, desc]);

  const counts = {
    unexplained: findings.filter((f) => f.explained === false).length,
    all: findings.length,
    explained: findings.filter((f) => f.explained === true).length,
  };

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <div className="flex h-10 shrink-0 items-center gap-3 border-b border-border-subtle px-4">
        <span className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
          {tab === "screens" ? "Screens" : "Signals"}
        </span>
        {tab === "signals" && (
        <div className="ml-auto flex items-center gap-3">
          <div className="flex border border-border-subtle">
            {VIEWS.map((label, i) => (
              <button
                key={label}
                type="button"
                onClick={() => setView(i)}
                className={
                  "border-r border-border-subtle px-2 py-0.5 font-mono text-meta last:border-r-0 " +
                  (i === view ? "bg-brand-muted text-brand" : "text-text-muted hover:text-text-primary")
                }
              >
                {label} {counts[label] > 0 && <span className="opacity-60">{counts[label]}</span>}
              </button>
            ))}
          </div>
          {findings[0] && <ScanAge at={findings[0].scanned_at} />}
          <button
            type="button"
            onClick={() => run.mutate()}
            disabled={run.isPending}
            className="flex items-center gap-1 font-mono text-meta text-text-muted transition-colors hover:text-brand disabled:opacity-50"
          >
            <RefreshCw size={10} className={run.isPending ? "animate-spin" : ""} />
            {run.isPending ? "scanning…" : "scan now"}
          </button>
        </div>
        )}
      </div>

      {tab === "screens" ? (
        <ScreensView onSelect={onSelect} />
      ) : (
      <div className="min-h-0 flex-1 overflow-y-auto">
      {run.isPending && (
        <p className="border-b border-border-subtle px-3 py-1.5 text-meta text-text-muted">
          Reading price and volume across the index universe. About two minutes; you can leave
          this page.
        </p>
      )}

      {isLoading ? (
        <p className="px-3 py-6 text-meta text-text-muted">loading…</p>
      ) : shown.length === 0 ? (
        <Empty
          icon={Radar}
          title={findings.length === 0 ? "No scan has run yet." : "Every move has a known cause."}
          hint={
            findings.length === 0
              ? "The scanner runs at the open, midday and the close on trading days."
              : "Everything the last scan flagged is already accounted for by something in the archive."
          }
        />
      ) : (
        <>
          <SignalSummary findings={findings} />
          <p className="border-b border-border-subtle px-3.5 py-2 text-meta leading-relaxed text-text-muted">
            Ranked by how far each instrument is behaving outside its own normal, volume weighted
            ahead of price: a price can move on nothing, but volume means somebody transacted.
          </p>
          {/* Capped rather than stretched. With the rails gone this page has
              1,440px to fill, and a seven-column table spread across all of it
              puts a stock's ticker and its volume a hand's width apart —
              technically full-width, unreadable in practice. */}
          {/* The header stays put. Scrolling thirty-nine rows of unlabelled
              numbers is what made this table unreadable: by the tenth row
              there was nothing on screen saying which column was volume and
              which was price. */}
          <table className="w-full max-w-[1120px] border-collapse">
            <thead className="sticky top-0 z-20 bg-bg-panel">
              <tr className="border-b border-border-subtle text-left">
                <Th className="w-[160px]">symbol</Th>
                <SortTh k="return_1d" sort={sort} desc={desc} set={setSort} flip={setDesc} w="92px">1d %</SortTh>
                <SortTh k="return_z" sort={sort} desc={desc} set={setSort} flip={setDesc} w="84px"
                  title="Today's move against this instrument's own normal range. Sorted by size, ignoring direction.">1d σ</SortTh>
                <SortTh k="volume_ratio" sort={sort} desc={desc} set={setSort} flip={setDesc} w="92px"
                  title="Today's volume as a multiple of its own recent median.">vol ×</SortTh>
                <SortTh k="volume_z" sort={sort} desc={desc} set={setSort} flip={setDesc} w="84px"
                  title="Volume surprise in log space against a median and MAD. Volume is heavily right-skewed, so an ordinary z-score would report the shape of the distribution rather than today.">vol σ</SortTh>
                <SortTh k="pct_from_52w_high" sort={sort} desc={desc} set={setSort} flip={setDesc} w="110px">from high</SortTh>
                <SortTh k="score" sort={sort} desc={desc} set={setSort} flip={setDesc} w="200px"
                  title="How far outside its own normal the instrument is behaving, volume weighted ahead of price.">signals</SortTh>
              </tr>
            </thead>
            <tbody className="divide-y divide-border-subtle">
              {shown.map((f) => (
                <Row key={f.id} f={f} showVerdict={mode === "all"} onSelect={onSelect} />
              ))}
            </tbody>
          </table>
        </>
      )}
      </div>
      )}
    </div>
  );
}

/**
 * What kind of abnormality this scan found, at a glance.
 *
 * A table of forty rows does not tell you whether the day was one of heavy
 * volume or of gaps. The bars are proportional to the whole so the shape of
 * the session is readable before any individual row is.
 */
function SignalSummary({ findings }: { findings: ScanFinding[] }) {
  const counts = new Map<string, number>();
  for (const f of findings) {
    for (const s of f.signals) counts.set(s, (counts.get(s) ?? 0) + 1);
  }
  const rows = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  if (rows.length === 0) return null;
  const max = Math.max(...rows.map(([, n]) => n));
  const unexplained = findings.filter((f) => f.explained === false).length;

  return (
    <div className="flex flex-wrap items-start gap-x-8 gap-y-3 border-b border-border-subtle px-3.5 py-3">
      <div>
        <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          flagged
        </p>
        <p className="mt-0.5 font-mono text-hero leading-none text-text-primary">
          {findings.length}
        </p>
        <p className="mt-1 font-mono text-meta text-text-muted">
          <span className="text-semantic-down">{unexplained}</span> unexplained
        </p>
      </div>

      <div className="min-w-56 flex-1">
        <p className="mb-1 font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          by signal
        </p>
        <div className="space-y-1">
          {rows.map(([sig, n]) => (
            <div key={sig} className="flex items-center gap-2">
              <span className="w-[110px] shrink-0 font-mono text-meta text-text-secondary">
                {SIGNAL_LABEL[sig] ?? sig}
              </span>
              <span className="h-1.5 max-w-64 flex-1 bg-bg-base">
                <span
                  className={
                    "block h-1.5 " + (sig === "volume_without_price" ? "bg-brand" : "bg-border-focus")
                  }
                  style={{ width: `${(n / max) * 100}%` }}
                />
              </span>
              <span className="w-6 shrink-0 text-right font-mono text-meta text-text-muted">
                {n}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

/**
 * A column header that sorts.
 *
 * Clicking a new column sorts it descending, because every column here is
 * "how much" and the interesting end is always the large one. Clicking the
 * current column reverses it.
 */
/**
 * How old the scan is, and whether that matters.
 *
 * The scanner runs three times a trading day, so anything past a few hours is
 * either out of session or a job that did not run. Saying so in amber costs
 * nothing and stops a day-old table being read as this morning's.
 */
function ScanAge({ at }: { at: string }) {
  const hours = (Date.now() - new Date(at).getTime()) / 3_600_000;
  const stale = hours > 8;
  return (
    <span
      title={stale ? "The scanner runs at the open, midday and the close on trading days." : undefined}
      className={"font-mono text-meta " + (stale ? "text-brand" : "text-text-muted")}
    >
      {formatAgo(at)}
      {stale ? " · stale" : ""}
    </span>
  );
}

function SortTh({
  k,
  sort,
  desc,
  set,
  flip,
  w,
  title,
  children,
}: {
  k: SortKey;
  sort: SortKey;
  desc: boolean;
  set: (k: SortKey) => void;
  flip: (d: boolean) => void;
  w: string;
  title?: string;
  children: string;
}) {
  const on = sort === k;
  return (
    <th
      title={title}
      style={{ width: w }}
      onClick={() => {
        if (on) flip(!desc);
        else {
          set(k);
          flip(true);
        }
      }}
      className={
        "cursor-pointer select-none px-2 py-1.5 text-right font-mono text-micro font-medium uppercase tracking-[0.12em] transition-colors " +
        (on ? "text-brand" : "text-text-muted hover:text-text-primary")
      }
    >
      {children}
      <span className={"ml-1 " + (on ? "opacity-100" : "opacity-0")}>{desc ? "\u2193" : "\u2191"}</span>
    </th>
  );
}

function Th({
  children,
  right,
  className = "",
  title,
}: {
  children: string;
  right?: boolean;
  className?: string;
  title?: string;
}) {
  return (
    <th
      title={title}
      className={`px-2 py-1.5 font-mono text-micro font-medium uppercase tracking-[0.12em] text-text-muted ${
        right ? "text-right" : "text-left"
      } ${className}`}
    >
      {children}
    </th>
  );
}

function Row({
  f,
  showVerdict,
  onSelect,
}: {
  f: ScanFinding;
  showVerdict: boolean;
  onSelect: (s: string) => void;
}) {
  return (
    <tr
      onClick={() => onSelect(f.symbol)}
      className="cursor-pointer hover:bg-bg-panel-hover"
    >
      <td className="px-2.5 py-2 font-mono text-meta text-text-primary">{f.symbol}</td>
      <td
        className={
          "px-2 py-1.5 text-right font-mono text-meta " +
          (f.return_1d >= 0 ? "text-semantic-up" : "text-semantic-down")
        }
      >
        {f.return_1d >= 0 ? "+" : ""}
        {f.return_1d.toFixed(2)}
      </td>
      <td className="px-2 py-1.5 text-right font-mono text-meta text-text-secondary">
        {f.return_z.toFixed(1)}
      </td>
      <td className="px-2 py-1.5 text-right font-mono text-meta text-text-secondary">
        {f.volume_ratio >= 10 ? f.volume_ratio.toFixed(0) : f.volume_ratio.toFixed(1)}×
      </td>
      <td className="px-2 py-1.5 text-right font-mono text-meta text-text-primary">
        {f.volume_z.toFixed(1)}
      </td>
      <td className="px-2 py-1.5 text-right font-mono text-meta text-text-muted">
        {f.pct_from_52w_high > -0.5 ? "at high" : `${f.pct_from_52w_high.toFixed(0)}%`}
      </td>
      <td className="px-2 py-1.5">
        <span className="flex flex-wrap items-center gap-1">
          {/* The score, which the table is sorted by. Leaving it out meant the
              default ordering was by a number nowhere on screen. */}
          <span
            className="mr-0.5 font-mono text-meta text-text-muted"
            title="How far outside its own normal this instrument is behaving, volume weighted ahead of price."
          >
            {f.score.toFixed(1)}
          </span>
          {f.signals.map((s) => (
            <Pill key={s} tone={s === "volume_without_price" ? "brand" : "muted"}>
              {SIGNAL_LABEL[s] ?? s}
            </Pill>
          ))}
          {showVerdict && f.explained === false && (
            <Pill tone="down" title="Nothing in the archive accounts for this move">
              unexplained
            </Pill>
          )}
        </span>
      </td>
    </tr>
  );
}
