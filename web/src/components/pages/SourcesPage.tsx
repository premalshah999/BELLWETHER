import { useQuery } from "@tanstack/react-query";
import { Rss } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type IngestSource } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { Empty } from "../ui/Empty";

/**
 * Every feed, and whether it is still delivering.
 *
 * The whole reason this view exists is that a source which quietly stopped
 * working looks exactly like a quiet news day. So failures sort to the top and
 * carry the error verbatim, rather than being folded into a count that reads
 * as fine at a glance.
 */
export function SourcesPage() {
  const [only, setOnly] = useState<"all" | "failing" | "watchlist">("all");

  const { data, isLoading } = useQuery({
    queryKey: ["ingest-sources"],
    queryFn: api.ingestSources,
    refetchInterval: 60_000,
  });

  const sources = useMemo(() => data?.sources ?? [], [data]);

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
      <div className="flex h-10 shrink-0 items-center gap-3 border-b border-border-subtle px-4">
        <span className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
          Sources
        </span>
        <div className="flex border border-border-subtle">
          <Seg on={only === "all"} onClick={() => setOnly("all")}>
            all {sources.length}
          </Seg>
          <Seg on={only === "failing"} onClick={() => setOnly("failing")}>
            failing {failing}
          </Seg>
          <Seg on={only === "watchlist"} onClick={() => setOnly("watchlist")}>
            per-instrument
          </Seg>
        </div>
        <span className="ml-auto font-mono text-meta text-text-muted">
          {items.toLocaleString()} items collected
        </span>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <p className="px-4 py-6 font-mono text-meta text-text-muted">loading…</p>
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

function Row({ s }: { s: IngestSource }) {
  return (
    <tr className="hover:bg-bg-panel-hover">
      <td className="px-3 py-2">
        <span className="flex items-center gap-2">
          <span
            className={"h-1.5 w-1.5 shrink-0 " + (s.healthy ? "bg-semantic-up" : "bg-semantic-down")}
          />
          <span className="truncate text-ui text-text-primary">{s.name}</span>
          {s.official && (
            <span
              className="shrink-0 font-mono text-micro text-text-muted"
              title="An exchange or regulator: ground truth when it disagrees with a newspaper."
            >
              official
            </span>
          )}
        </span>
      </td>
      <td className="px-3 py-2 font-mono text-meta text-text-muted">{s.category}</td>
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
          <span className="text-text-muted">ok</span>
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

function Seg({
  on,
  onClick,
  children,
}: {
  on: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "border-r border-border-subtle px-2 py-0.5 font-mono text-meta last:border-r-0 transition-colors " +
        (on ? "bg-brand-muted text-brand" : "text-text-muted hover:text-text-primary")
      }
    >
      {children}
    </button>
  );
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
        "px-3 py-2 font-mono text-micro font-medium uppercase tracking-[0.12em] text-text-muted " +
        (right ? "text-right " : "text-left ") +
        className
      }
    >
      {children}
    </th>
  );
}
