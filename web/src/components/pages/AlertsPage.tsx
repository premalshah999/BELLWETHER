import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellOff, Check, CheckCheck, CircleHelp, Sparkles, X } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { api, type Alert } from "../../lib/api";
import { dayOf, formatAgo, formatClock, formatDay } from "../../lib/format";
import { useUrlState } from "../../lib/url";
import { PageHeader, Segmented, Select, SkeletonRows } from "../ui/controls";

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
  const navigate = useNavigate();
  const [view, setView] = useUrlState<"all" | "unread">("show", "all");
  const [symbol, setSymbol] = useUrlState("symbol", "");
  const unreadOnly = view === "unread";

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
  const symbols = [...new Set(all.map((a) => a.symbol))].sort();
  const unread = data?.unread ?? 0;
  const open = (s: string) => {
    onSelect(s);
    navigate("/charts");
  };
  let lastDay = "";

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <PageHeader
        title="Alerts"
        subtitle={
          isLoading
            ? "Loading alerts…"
            : all.length === 0
              ? "Alerts appear here when every condition of an enabled algorithm holds on a closed bar."
              : `${unread} unread. Each alert keeps the exact values that made it fire.`
        }
        actions={
          unread > 0 && (
            <button type="button" onClick={() => markAll.mutate()} disabled={markAll.isPending} className="action-secondary">
              <CheckCheck size={15} /> {markAll.isPending ? "Marking…" : "Mark all as read"}
            </button>
          )
        }
      >
        <Segmented
          label="Show"
          value={view}
          onChange={setView}
          options={[
            { value: "all", label: "All" },
            { value: "unread", label: `Unread${unread ? ` ${unread}` : ""}` },
          ]}
        />
        {symbols.length > 1 && (
          <Select
            label="Company"
            value={symbol}
            onChange={setSymbol}
            options={[{ value: "", label: "Every company" }, ...symbols.map((s) => ({ value: s, label: s }))]}
          />
        )}
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <SkeletonRows count={7} height={72} />
        ) : shown.length === 0 ? (
          <div className="flex max-w-md flex-col items-start gap-3 px-6 py-12">
            <BellOff size={22} className="text-text-muted" />
            <p className="font-reading text-display text-text-primary">{unreadOnly ? "You’re all caught up." : "No alerts have fired yet."}</p>
            <p className="text-ui text-text-secondary">Build an algorithm and turn it on to be alerted when its conditions are met.</p>
            <button type="button" onClick={() => navigate("/algorithms/build")} className="action-secondary mt-1">
              Build an algorithm
            </button>
          </div>
        ) : (
          <ul className="pb-8">
            {shown.map((a) => {
              const day = dayOf(a.fired_at);
              const header = day !== lastDay;
              lastDay = day;
              return (
                <li key={a.id}>
                  {header && (
                    <h2 className="sticky top-0 z-10 border-b border-border-subtle bg-bg-panel/95 px-5 py-2 text-meta font-semibold text-text-secondary backdrop-blur md:px-6">
                      {formatDay(a.fired_at)}
                    </h2>
                  )}
                  <AlertRow alert={a} onOpen={open} />
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}

function fmt(v: number | null | undefined) {
  return v == null || !Number.isFinite(v) ? "—" : v.toLocaleString("en-US", { maximumFractionDigits: 2, minimumFractionDigits: 2 });
}

const OPS: Record<string, string> = {
  "<": "below",
  "<=": "at or below",
  ">": "above",
  ">=": "at or above",
  "==": "equal to",
  "!=": "not equal to",
  crosses_above: "crossed above",
  crosses_below: "crossed below",
};

function AlertRow({ alert, onOpen }: { alert: Alert; onOpen: (symbol: string) => void }) {
  const read = !!alert.read_at;
  return (
    <article className="flex gap-4 border-b border-border-subtle px-5 py-4 md:px-6 [contain-intrinsic-size:auto_110px] [content-visibility:auto]">
      <time className="w-11 shrink-0 pt-0.5 text-meta text-text-muted max-sm:hidden" title={formatAgo(alert.fired_at)}>
        {formatClock(alert.fired_at)}
      </time>
      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          {!read && <span className="h-2 w-2 shrink-0 self-center rounded-full bg-brand" aria-label="Unread" />}
          <button type="button" onClick={() => onOpen(alert.symbol)} className="text-emphasis font-semibold text-text-primary hover:text-brand">
            {alert.symbol}
          </button>
          <span className={"text-ui " + (read ? "text-text-secondary" : "text-text-primary")}>{alert.algorithm_name}</span>
          <span className="text-meta text-text-muted">
            at {fmt(alert.price)} on the {alert.interval} chart
          </span>
        </p>
        {alert.conditions?.length > 0 && (
          <ul className="mt-2 flex flex-col gap-1">
            {alert.conditions.map((c, i) => {
              const Icon = c.result === "true" ? Check : c.result === "false" ? X : CircleHelp;
              return (
                <li key={i} className="flex items-baseline gap-2 text-meta text-text-secondary">
                  <Icon
                    size={13}
                    className={"shrink-0 translate-y-0.5 " + (c.result === "true" ? "text-brand" : "text-text-muted")}
                    aria-label={c.result === "true" ? "Held" : c.result === "false" ? "Did not hold" : "Unknown"}
                  />
                  <span>
                    {c.label} {OPS[c.op] ?? c.op} {c.right_label}
                    <span className="text-text-muted">
                      {" "}
                      ({fmt(c.left_value)} vs {fmt(c.right_value)})
                    </span>
                  </span>
                </li>
              );
            })}
          </ul>
        )}
        {alert.ai_context ? (
          <p className="mt-2.5 max-w-3xl rounded-md bg-brand-muted px-3 py-2 text-ui leading-relaxed text-text-primary">
            <Sparkles size={13} className="mr-1.5 inline -translate-y-px text-brand" />
            {alert.ai_context}
          </p>
        ) : alert.ai_status && alert.ai_status !== "ok" ? (
          <p className="mt-1.5 text-meta text-text-muted">No AI context this time ({alert.ai_status}). The alert itself is unaffected.</p>
        ) : null}
      </div>
    </article>
  );
}
