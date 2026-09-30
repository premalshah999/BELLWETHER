import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Radar, RefreshCw } from "lucide-react";
import { useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api, type ScanFinding } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { signalName, signalSentence, signed, toneOf } from "../../lib/signals";
import { useUrlState } from "../../lib/url";
import { PageHeader, Segmented, SkeletonRows } from "../ui/controls";
import { ScreensView } from "./ScreensView";

const VIEWS = ["all", "unexplained", "explained"] as const;
type View = (typeof VIEWS)[number];

/** Sortable columns, and how to read each finding's value for one. */
const SORTS = {
  score: (f: ScanFinding) => f.score,
  return_1d: (f: ScanFinding) => f.return_1d,
  return_z: (f: ScanFinding) => Math.abs(f.return_z),
  volume_ratio: (f: ScanFinding) => f.volume_ratio,
  pct_from_52w_high: (f: ScanFinding) => f.pct_from_52w_high,
} as const;

type SortKey = keyof typeof SORTS;

export function ScannerPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const { mode: routeMode } = useParams();
  if (routeMode === "screens") {
    return (
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <PageHeader title="Screens" subtitle="Filter the S&P 1500 by valuation, growth, momentum and quality, and save what you use." />
        <ScreensView onSelect={onSelect} />
      </div>
    );
  }
  return <Signals onSelect={onSelect} />;
}

/**
 * What the market did, before anyone wrote about it.
 *
 * Every row is a stock trading outside its own normal range. The ones no
 * news explains are the reason the page exists, so they are marked, and one
 * click filters to them.
 */
function Signals({ onSelect }: { onSelect: (symbol: string) => void }) {
  const navigate = useNavigate();
  const [view, setView] = useUrlState<View>("view", "all");
  const [sort, setSort] = useState<SortKey>("score");
  const [desc, setDesc] = useState(true);
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
    // Absent is not "no": a finding whose archive check never ran belongs in
    // neither bucket.
    const out =
      view === "unexplained"
        ? findings.filter((f) => f.explained === false)
        : view === "explained"
          ? findings.filter((f) => f.explained === true)
          : findings;
    const read = SORTS[sort];
    return [...out].sort((a, b) => (desc ? read(b) - read(a) : read(a) - read(b)));
  }, [findings, view, sort, desc]);

  const unexplained = findings.filter((f) => f.explained === false).length;
  const explained = findings.filter((f) => f.explained === true).length;
  const maxScore = Math.max(1, ...findings.map((f) => f.score));
  const at = findings[0]?.scanned_at;
  const stale = at ? (Date.now() - new Date(at).getTime()) / 3_600_000 > 8 : false;

  const breakdown = useMemo(() => {
    const m = new Map<string, number>();
    for (const f of findings) for (const s of f.signals) m.set(s, (m.get(s) ?? 0) + 1);
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [findings]);

  const open = (symbol: string) => {
    onSelect(symbol);
    navigate("/charts");
  };
  const sortBy = (k: SortKey) => {
    if (k === sort) setDesc(!desc);
    else {
      setSort(k);
      setDesc(true);
    }
  };

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="Signals"
        subtitle={
          isLoading ? (
            "Loading the latest scan…"
          ) : findings.length === 0 ? (
            "No scan has run yet."
          ) : (
            <>
              {findings.length} stocks are trading outside their normal range
              {unexplained > 0 && (
                <>
                  , and <span className="font-medium text-text-primary">{unexplained} have no news to explain them</span>
                </>
              )}
              .{" "}
              <span className={stale ? "text-brand" : "text-text-muted"}>
                Scanned {at ? formatAgo(at) : ""}
                {stale ? ", outside market hours" : ""}.
              </span>
            </>
          )
        }
        actions={
          <button type="button" onClick={() => run.mutate()} disabled={run.isPending} className="action-secondary">
            <RefreshCw size={14} className={run.isPending ? "animate-spin" : ""} />
            {run.isPending ? "Scanning…" : "Scan now"}
          </button>
        }
      >
        <Segmented
          label="Show"
          value={view}
          onChange={setView}
          options={[
            { value: "all", label: `All ${findings.length || ""}` },
            { value: "unexplained", label: `No explanation ${unexplained || ""}` },
            { value: "explained", label: `Explained ${explained || ""}` },
          ]}
        />
        {breakdown.length > 0 && (
          <p className="flex flex-wrap gap-x-4 gap-y-1 pl-2 text-meta text-text-muted max-md:hidden">
            {breakdown.map(([sig, n]) => (
              <span key={sig}>
                <span className="font-semibold text-text-secondary">{n}</span> {signalName(sig).toLowerCase()}
              </span>
            ))}
          </p>
        )}
      </PageHeader>

      {run.isPending && (
        <p className="border-b border-border-subtle bg-brand-muted px-6 py-2 text-meta text-text-secondary">
          Reading price and volume across the S&P 1500. It takes about two minutes, and you can leave this page.
        </p>
      )}

      <div className="min-h-0 flex-1 overflow-auto">
        {isLoading ? (
          <SkeletonRows count={10} height={44} />
        ) : shown.length === 0 ? (
          <div className="flex max-w-md flex-col items-start gap-3 px-6 py-12">
            <Radar size={22} className="text-text-muted" />
            <p className="font-reading text-display text-text-primary">
              {findings.length === 0 ? "No scan has run yet." : "Every flagged move has a known cause."}
            </p>
            <p className="text-ui text-text-secondary">
              {findings.length === 0
                ? "The scanner runs through every trading session. Start one now to see today’s outliers."
                : "Everything the last scan flagged is accounted for by news already in the archive."}
            </p>
          </div>
        ) : (
          <div className="mx-5 mb-8 overflow-x-auto rounded-xl border border-border-subtle bg-bg-card md:mx-8 md:overflow-x-clip">
          <table className="w-full min-w-[760px] border-collapse text-ui">
            <thead className="sticky top-0 z-10 bg-bg-card shadow-[0_1px_0_var(--line)]">
              <tr className="text-meta text-text-muted">
                <th className="px-6 py-2.5 text-left font-medium">Company</th>
                <SortTh k="return_1d" sort={sort} desc={desc} onSort={sortBy} title="Today's price change.">
                  Day
                </SortTh>
                <SortTh k="return_z" sort={sort} desc={desc} onSort={sortBy} title="Today's move against this stock's own normal day, in standard deviations.">
                  Unusualness
                </SortTh>
                <SortTh k="volume_ratio" sort={sort} desc={desc} onSort={sortBy} title="Today's volume as a multiple of its recent median.">
                  Volume
                </SortTh>
                <SortTh k="pct_from_52w_high" sort={sort} desc={desc} onSort={sortBy} title="How far below its 52-week high it trades.">
                  From 52w high
                </SortTh>
                <th className="px-3 py-2.5 text-left font-medium">Why it was flagged</th>
                <SortTh k="score" sort={sort} desc={desc} onSort={sortBy} title="How far outside its own normal the stock is behaving, volume weighted ahead of price." last>
                  Strength
                </SortTh>
              </tr>
            </thead>
            <tbody>
              {shown.map((f) => (
                <tr
                  key={f.id}
                  onClick={() => open(f.symbol)}
                  className="group cursor-pointer border-t border-border-subtle transition-colors hover:bg-bg-panel-hover"
                >
                  <td className="px-6 py-3">
                    <button type="button" onClick={() => open(f.symbol)} className="inline-flex h-6 items-center font-semibold text-text-primary group-hover:text-brand">
                      {f.symbol}
                    </button>
                  </td>
                  <td className={"px-3 py-3 text-right font-medium " + toneOf(f.return_1d)}>{signed(f.return_1d)}%</td>
                  <td className="px-3 py-3 text-right text-text-secondary">{Math.abs(f.return_z).toFixed(1)}σ</td>
                  <td className="px-3 py-3 text-right text-text-secondary">
                    {f.volume_ratio >= 10 ? f.volume_ratio.toFixed(0) : f.volume_ratio.toFixed(1)}×
                  </td>
                  <td className="px-3 py-3 text-right text-text-secondary">
                    {f.pct_from_52w_high > -0.5 ? "At high" : `${f.pct_from_52w_high.toFixed(0)}%`}
                  </td>
                  <td className="px-3 py-3 text-text-secondary">
                    <span>{signalSentence(f.signals)}</span>
                    {f.explained === false && (
                      <span className="ml-2 whitespace-nowrap rounded-sm bg-brand-muted px-1.5 py-px text-micro font-medium text-brand">
                        No explanation
                      </span>
                    )}
                  </td>
                  <td className="px-6 py-3">
                    <span className="ml-auto flex w-28 items-center gap-2">
                      <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-bg-base">
                        <span className="block h-full rounded-full bg-text-muted" style={{ width: `${(f.score / maxScore) * 100}%` }} />
                      </span>
                      <span className="w-7 text-right text-meta text-text-muted">{f.score.toFixed(0)}</span>
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
      </div>
    </div>
  );
}

function SortTh({
  k,
  sort,
  desc,
  onSort,
  title,
  children,
  last,
}: {
  k: SortKey;
  sort: SortKey;
  desc: boolean;
  onSort: (k: SortKey) => void;
  title?: string;
  children: string;
  last?: boolean;
}) {
  const on = sort === k;
  const Arrow = desc ? ArrowDown : ArrowUp;
  return (
    <th
      title={title}
      aria-sort={on ? (desc ? "descending" : "ascending") : "none"}
      className={"py-2.5 text-right font-medium " + (last ? "px-6" : "px-3")}
    >
      <button
        type="button"
        onClick={() => onSort(k)}
        className={"inline-flex items-center gap-1 transition-colors " + (on ? "text-text-primary" : "hover:text-text-primary")}
      >
        {children}
        <Arrow size={12} className={on ? "opacity-100" : "opacity-0"} />
      </button>
    </th>
  );
}
