import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellOff, CheckCheck } from "lucide-react";
import { useState } from "react";
import { api, type Alert } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { Empty } from "../ui/Empty";
import { Pill } from "../ui/Pill";

/**
 * Everything that has fired.
 *
 * This was a tab in a 320px rail, which gave the evidence behind each alert —
 * the condition snapshot, the AI note, the delivery record — about two hundred
 * pixels to render in. An alert an operator cannot audit is one they will
 * eventually stop trusting, and the snapshot is the whole reason to keep it,
 * so it belongs on a page rather than in a column.
 */
export function AlertsPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const qc = useQueryClient();
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [symbol, setSymbol] = useState<string | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ["alerts", unreadOnly],
    queryFn: () => api.alerts({ limit: 200, ...(unreadOnly ? { unread: true } : {}) }),
    refetchInterval: 30_000,
  });

  const markAll = useMutation({
    mutationFn: api.markAllAlertsRead,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
  });

  const all = data?.alerts ?? [];
  const shown = symbol ? all.filter((a) => a.symbol === symbol) : all;

  // The instruments that actually fired, so the filter offers what exists
  // rather than the whole watchlist.
  const symbols = [...new Set(all.map((a) => a.symbol))].sort();

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <div className="flex h-10 shrink-0 items-center gap-3 border-b border-border-subtle px-4">
        <span className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
          Alerts
        </span>
        <div className="flex border border-border-subtle">
          <Seg on={!unreadOnly} onClick={() => setUnreadOnly(false)}>
            all {all.length > 0 && <span className="opacity-60">{all.length}</span>}
          </Seg>
          <Seg on={unreadOnly} onClick={() => setUnreadOnly(true)}>
            unread {data?.unread ? <span className="opacity-60">{data.unread}</span> : null}
          </Seg>
        </div>

        {symbols.length > 1 && (
          <select
            value={symbol ?? ""}
            onChange={(e) => setSymbol(e.target.value || null)}
            className="border border-border-subtle bg-bg-base px-2 py-0.5 font-mono text-meta text-text-secondary outline-none focus:border-brand"
          >
            <option value="">every instrument</option>
            {symbols.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        )}

        {(data?.unread ?? 0) > 0 && (
          <button
            type="button"
            onClick={() => markAll.mutate()}
            className="ml-auto flex items-center gap-1.5 font-mono text-meta text-text-muted transition-colors hover:text-brand"
          >
            <CheckCheck size={12} /> mark all read
          </button>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <p className="px-4 py-6 font-mono text-meta text-text-muted">loading…</p>
        ) : shown.length === 0 ? (
          <Empty
            icon={BellOff}
            title={unreadOnly ? "Nothing unread." : "No alerts have fired."}
            hint="Alerts appear here when an enabled algorithm's conditions all hold on a closed bar."
          />
        ) : (
          <div className="max-w-[1100px] divide-y divide-border-subtle">
            {shown.map((a) => (
              <AlertCard key={a.id} alert={a} onSelect={onSelect} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

/** Two decimals, or a dash. An indicator that could not be computed is not
 *  zero, and showing it as zero would make a failed condition look satisfied. */
function fmt(v: number | null | undefined) {
  return v == null || !Number.isFinite(v) ? "—" : v.toFixed(2);
}

function AlertCard({
  alert,
  onSelect,
}: {
  alert: Alert;
  onSelect: (symbol: string) => void;
}) {
  return (
    <article className={"px-4 py-3.5 " + (alert.read_at ? "opacity-60" : "")}>
      <div className="flex items-baseline gap-2.5">
        <button
          type="button"
          onClick={() => onSelect(alert.symbol)}
          className="font-mono text-ui text-text-primary transition-colors hover:text-brand"
        >
          {alert.symbol}
        </button>
        <span className="font-mono text-meta text-text-muted">{alert.interval}</span>
        <span className="text-meta text-text-secondary">{alert.algorithm_name}</span>
        <span className="font-mono text-meta text-text-secondary">{fmt(alert.price)}</span>
        <span className="ml-auto font-mono text-meta text-text-muted">
          {formatAgo(alert.fired_at)}
        </span>
      </div>

      {/* The condition snapshot, verbatim. Monospace because it is code, and
          on its own background because it is evidence rather than commentary. */}
      {alert.conditions?.length > 0 && (
        <pre className="mt-2 max-w-3xl overflow-x-auto border border-border-subtle bg-bg-base px-3 py-2 font-mono text-meta leading-relaxed text-text-secondary">
          {alert.conditions
            .map((c) => {
              const lhs = `${c.label} ${c.op} ${c.right_label}`;
              const actual = `${fmt(c.left_value)} ${c.op} ${fmt(c.right_value)}`;
              const mark = c.result === "true" ? "✓" : c.result === "false" ? "✗" : "?";
              return `${lhs}\n  ${actual}  ${mark}`;
            })
            .join("\n")}
        </pre>
      )}

      {alert.ai_context ? (
        <div className="mt-2 max-w-3xl">
          <Pill tone="brand">AI</Pill>
          <p className="mt-1 text-ui leading-relaxed text-text-secondary">{alert.ai_context}</p>
        </div>
      ) : alert.ai_status && alert.ai_status !== "ok" ? (
        <p className="mt-1.5 text-meta text-text-muted">
          AI context unavailable ({alert.ai_status}). The alert itself is unaffected.
        </p>
      ) : null}
    </article>
  );
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
