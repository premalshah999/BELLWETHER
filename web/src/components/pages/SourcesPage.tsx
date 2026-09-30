import { useQuery } from "@tanstack/react-query";
import { Rss, Zap } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type IngestSource, type SourceLatency } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { Empty } from "../ui/Empty";
import { PageHeader, Segmented, SkeletonRows } from "../ui/controls";
import { ShieldCheck } from "lucide-react";

/**
 * Every feed, and whether it is still delivering.
 *
 * The whole reason this view exists is that a source which quietly stopped
 * working looks exactly like a quiet news day. So failures sort to the top and
 * carry the error verbatim, rather than being folded into a count that reads
 * as fine at a glance.
 */
export function SourcesPage() {
  const [only, setOnly] = useState<"all" | "failing" | "watchlist" | "latency">("all");

  const { data, isLoading } = useQuery({
    queryKey: ["ingest-sources"],
    queryFn: api.ingestSources,
    refetchInterval: 60_000,
    enabled: only !== "latency",
  });

  const { data: latencyData, isLoading: latencyLoading } = useQuery({
    queryKey: ["source-latency"],
    queryFn: () => api.sourceLatency(30),
    enabled: only === "latency",
    staleTime: 5 * 60_000,
  });

  const sources = useMemo(() => data?.sources ?? [], [data]);
  const latency = useMemo(() => latencyData?.sources ?? [], [latencyData]);

  const shown = useMemo(() => {
    let out = sources;
    if (only === "failing") out = out.filter((s) => !s.healthy);
    if (only === "watchlist") out = out.filter((s) => s.category === "watchlist");
    // Failing first, then by how much each one actually delivers. A healthy
    // feed returning nothing is the next most interesting thing after a broken
    // one, and sorting by name buries both.
    return [...out].sort((a, b) => {
      if (a.healthy !== b.healthy) return a.healthy ? 1 : -1;
      return b.total_items - a.total_items;
    });
  }, [sources, only]);

  const failing = sources.filter((s) => !s.healthy).length;
  const items = sources.reduce((n, s) => n + s.total_items, 0);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <PageHeader
        title="Data sources"
        subtitle={
          only === "latency"
            ? "Which sources break stories first, over the last 30 days."
            : failing > 0
              ? `${sources.length} sources, ${failing} failing right now. ${items.toLocaleString()} items collected so far.`
              : `All ${sources.length} sources are delivering. ${items.toLocaleString()} items collected so far.`
        }
      >
        <Segmented
          label="Show"
          value={only}
          onChange={setOnly}
          options={[
            { value: "all", label: `All ${sources.length || ""}` },
            { value: "failing", label: `Failing ${failing}` },
            { value: "watchlist", label: "Per company" },
            { value: "latency", label: "First to report" },
          ]}
        />
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {only === "latency" ? (
          latencyLoading ? (
            <SkeletonRows count={10} height={40} />
          ) : latency.length === 0 ? (
            <Empty
              icon={Zap}
              title="Not enough multi-source stories yet."
              hint="This ranks sources by how often they broke a story before another source also carried it. It needs a source to have appeared on at least five such stories in the last 30 days before its win rate means anything."
            />
          ) : (
            <table className="w-full max-w-[900px] border-collapse">
              <thead className="sticky top-0 z-10 bg-bg-panel">
                <tr className="border-b border-border-subtle">
                  <Th className="w-[60px]">rank</Th>
                  <Th className="w-[280px]">source</Th>
                  <Th right className="w-[110px]">first</Th>
                  <Th right className="w-[110px]">seen on</Th>
                  <Th right className="w-[110px]">win rate</Th>
                  <Th right>avg. lead</Th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border-subtle">
                {latency.map((s, i) => (
                  <LatencyRow key={s.source_id} rank={i + 1} s={s} />
                ))}
              </tbody>
            </table>
          )
        ) : isLoading ? (
          <SkeletonRows count={10} height={40} />
        ) : shown.length === 0 ? (
          <Empty
            icon={Rss}
            title={only === "failing" ? "Every source is delivering." : "No sources."}
            hint="Feeds are polled on their own cadence; a failing one is retried with backoff."
          />
        ) : (
          <table className="w-full max-w-[1180px] border-collapse">
            <thead className="sticky top-0 z-10 bg-bg-panel">
              <tr className="border-b border-border-subtle">
                <Th className="w-[280px]">source</Th>
                <Th className="w-[130px]">category</Th>
                <Th className="w-[90px]">every</Th>
                <Th right className="w-[90px]">items</Th>
                <Th right className="w-[90px]">new</Th>
                <Th className="w-[120px]">last success</Th>
                <Th>state</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border-subtle">
              {shown.map((s) => (
                <Row key={s.id} s={s} />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function LatencyRow({ rank, s }: { rank: number; s: SourceLatency }) {
  return (
    <tr className="transition-colors hover:bg-bg-panel-hover">
      <td className="px-3 py-2 font-mono text-meta text-text-muted">{rank}</td>
      <td className="px-3 py-2 text-ui text-text-primary">{s.name}</td>
      <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
        {s.times_first.toLocaleString()}
      </td>
      <td className="px-3 py-2 text-right font-mono text-ui text-text-muted">
        {s.participated.toLocaleString()}
      </td>
      <td
        className={
          "px-3 py-2 text-right font-mono text-ui " +
          (s.win_rate_pct >= 50 ? "text-semantic-up" : "text-text-secondary")
        }
      >
        {s.win_rate_pct.toFixed(1)}%
      </td>
      <td className="px-3 py-2 text-right font-mono text-meta text-text-muted">
        {s.avg_lead_seconds == null ? "—" : humanLead(s.avg_lead_seconds)}
      </td>
    </tr>
  );
}

function humanLead(seconds: number) {
  if (seconds < 90) return `${Math.round(seconds)}s`;
  const m = Math.round(seconds / 60);
  if (m < 90) return `${m}m`;
  return `${Math.round(m / 60)}h`;
}

function Row({ s }: { s: IngestSource }) {
  return (
    <tr className="transition-colors hover:bg-bg-panel-hover">
      <td className="px-3 py-2">
        <span className="flex items-center gap-2">
          <span
            className={"h-2 w-2 shrink-0 rounded-full " + (s.healthy ? "bg-semantic-up" : "bg-semantic-down")}
            aria-label={s.healthy ? "Working" : "Failing"}
          />
          <span className="truncate text-ui font-medium text-text-primary">{s.name}</span>
          {s.official && (
            <ShieldCheck size={14} className="shrink-0 text-brand" aria-label="Official source" />
          )}
        </span>
      </td>
      <td className="px-3 py-2 font-mono text-meta text-text-muted first-letter:uppercase">{s.category}</td>
      <td className="px-3 py-2 font-mono text-meta text-text-muted">
        {humanEvery(s.refresh_ms)}
      </td>
      <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
        {s.total_items.toLocaleString()}
      </td>
      <td className="px-3 py-2 text-right font-mono text-ui text-text-muted">
        {s.total_new_items.toLocaleString()}
      </td>
      <td className="px-3 py-2 font-mono text-meta text-text-muted">
        {s.last_success_at ? formatAgo(s.last_success_at) : "never"}
      </td>
      <td className="px-3 py-2 text-meta">
        {s.healthy ? (
          <span className="text-text-muted">Working</span>
        ) : (
          // Verbatim, not summarised: "rate-limited" and "certificate
          // expired" need different responses and a shared word for both
          // would cost the reader the trip to the logs.
          <span
            title={s.last_error}
            className="block max-w-[420px] truncate text-semantic-down"
          >
            {s.consecutive_failures > 1 && `${s.consecutive_failures}× · `}
            {s.last_error || "failing"}
          </span>
        )}
      </td>
    </tr>
  );
}

function humanEvery(ms: number) {
  const s = Math.round(ms / 1000);
  if (s < 90) return `${s}s`;
  const m = Math.round(s / 60);
  if (m < 90) return `${m}m`;
  return `${Math.round(m / 60)}h`;
}


function Th({
  children,
  right,
  className = "",
}: {
  children: string;
  right?: boolean;
  className?: string;
}) {
  return (
    <th
      className={
        "px-3 py-2 font-mono text-meta font-medium first-letter:uppercase text-text-muted " +
        (right ? "text-right " : "text-left ") +
        className
      }
    >
      {children}
    </th>
  );
}
