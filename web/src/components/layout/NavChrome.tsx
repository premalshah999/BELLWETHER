import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LogOut, PanelLeftClose, PanelLeftOpen, Search } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api } from "../../lib/api";
import { sessionState } from "../../lib/session";
import type { StreamState } from "../../lib/stream";

/**
 * The brand, the collapse control, and search.
 *
 * All of this used to sit in a 56px bar across the top, which is a whole row
 * of the screen spent on things that are pressed rarely and read almost never.
 * Folding it into the navigation returns that row to the chart and puts every
 * global control in one column.
 */
export function NavHeader({
  shut,
  onCollapse,
  onExpand,
  onCommand,
}: {
  shut?: boolean;
  onCollapse?: () => void;
  onExpand?: () => void;
  onCommand: () => void;
}) {
  if (shut) {
    return (
      <div className="flex w-full shrink-0 flex-col items-center gap-1 border-b border-border-subtle py-2">
        {/* The one glow in the entire interface, and it is two pixels wide. */}
        <span className="h-4 w-4 bg-brand shadow-[0_0_8px_rgba(0,229,255,0.55)]" />
        <button
          type="button"
          onClick={onExpand}
          title="Expand the navigation"
          className="mt-1 flex h-7 w-7 items-center justify-center text-text-muted transition-colors hover:text-brand"
        >
          <PanelLeftOpen size={14} />
        </button>
        <button
          type="button"
          onClick={onCommand}
          title="Search companies and tickers  (⌘K)"
          className="flex h-7 w-7 items-center justify-center text-text-muted transition-colors hover:text-brand"
        >
          <Search size={14} />
        </button>
      </div>
    );
  }

  return (
    <div className="shrink-0 border-b border-border-subtle">
      <div className="flex h-11 items-center gap-2 px-3">
        <span className="h-4 w-4 shrink-0 bg-brand shadow-[0_0_8px_rgba(0,229,255,0.55)]" />
        <span className="min-w-0 flex-1 truncate font-mono text-ui font-semibold tracking-[0.14em] text-text-primary">
          TRADESYS
        </span>
        <button
          type="button"
          onClick={onCollapse}
          title="Collapse the navigation"
          className="shrink-0 text-text-muted transition-colors hover:text-brand"
        >
          <PanelLeftClose size={13} />
        </button>
      </div>

      <button
        type="button"
        onClick={onCommand}
        className="mx-3 mb-2.5 flex h-7 w-[calc(100%-1.5rem)] items-center gap-2 border border-border-subtle bg-bg-base px-2 text-left text-text-muted transition-colors hover:border-border-focus"
      >
        <Search size={11} className="shrink-0" />
        <span className="min-w-0 flex-1 truncate text-meta">Search…</span>
        <kbd className="shrink-0 border border-border-subtle px-1 font-mono text-micro text-text-muted">
          ⌘K
        </kbd>
      </button>
    </div>
  );
}

/**
 * Who is signed in, and whether anything upstream is broken.
 *
 * At the foot because that is where an application puts its account controls
 * and because neither is read on a schedule — but the health dot still turns
 * red on its own, which is the only part that is ever urgent.
 */
export function NavFooter({ shut, stream }: { shut?: boolean; stream: StreamState }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  const { data: auth } = useQuery({
    queryKey: ["auth-status"],
    queryFn: api.authStatus,
    staleTime: 60_000,
  });
  const { data: health } = useQuery({
    queryKey: ["health"],
    queryFn: api.health,
    refetchInterval: 30_000,
  });
  const { data: ai } = useQuery({
    queryKey: ["ai-status"],
    queryFn: api.aiStatus,
    refetchInterval: 120_000,
  });

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open]);

  const providers = health?.providers ?? [];
  // A provider with no credentials was never set up, so it is not down. The
  // footer read "2 DOWN" permanently because two optional search providers
  // have no keys, which trains the eye to ignore the one indicator whose
  // entire job is to be noticed when it changes.
  const down = providers.filter(
    (p) => p.status !== "ok" && p.status !== "degraded" && p.status !== "unconfigured",
  ).length;
  const degraded = providers.filter((p) => p.status === "degraded").length;
  const unconfigured = providers.filter((p) => p.status === "unconfigured").length;
  const budget = ai?.budget;
  const used = budget && budget.limit > 0 ? Math.min(1, budget.used / budget.limit) : 0;

  const tone =
    down > 0 ? "bg-semantic-down" : degraded > 0 || budget?.exhausted ? "bg-brand" : "bg-semantic-up";

  // Being connected is not the same as receiving data. When every venue is
  // shut nothing trades, so nothing streams, and a badge reading LIVE over a
  // frozen screen is indistinguishable from a broken feed.
  const anyOpen = sessionState("NSE").open || sessionState("US").open;
  const live = stream.connected && anyOpen;

  if (shut) {
    return (
      <div className="flex w-full shrink-0 flex-col items-center gap-2 border-t border-border-subtle py-2">
        <span
          className={"h-1.5 w-1.5 " + tone}
          title={
            down > 0
              ? `${down} upstream down`
              : degraded > 0
                ? `${degraded} degraded`
                : "All configured upstreams ok"
          }
        />
        <span
          className={"h-1.5 w-1.5 " + (live ? "bg-brand" : "bg-text-muted")}
          title={live ? "Streaming" : anyOpen ? "Not connected" : "Every venue is closed"}
        />
        {auth?.required && (
          <button
            type="button"
            title="Sign out"
            onClick={async () => {
              await api.logout();
              qc.invalidateQueries();
            }}
            className="text-text-muted transition-colors hover:text-brand"
          >
            <LogOut size={13} />
          </button>
        )}
      </div>
    );
  }

  return (
    <div ref={box} className="relative shrink-0 border-t border-border-subtle">
      {open && (
        <div className="absolute bottom-full left-0 right-0 mb-px border-t border-border-subtle bg-bg-panel">
          <div className="max-h-64 divide-y divide-border-subtle overflow-y-auto">
            {providers.map((p) => (
              <div key={p.provider} className="flex items-center gap-2 px-3 py-1.5">
                <span
                  className={
                    "h-1.5 w-1.5 shrink-0 " +
                    (p.status === "ok"
                      ? "bg-semantic-up"
                      : p.status === "degraded"
                        ? "bg-brand"
                        : p.status === "unconfigured"
                          ? "bg-border-focus"
                          : "bg-semantic-down")
                  }
                />
                <span className="min-w-0 flex-1 truncate font-mono text-meta text-text-secondary">
                  {p.provider}
                </span>
                <span className="shrink-0 font-mono text-micro text-text-muted">{p.status}</span>
              </div>
            ))}
            {providers.length === 0 && (
              <p className="px-3 py-2 text-meta text-text-muted">No upstreams reporting.</p>
            )}
          </div>
          {budget && budget.limit > 0 && (
            <div className="border-t border-border-subtle px-3 py-2">
              <div className="mb-1 flex items-baseline justify-between">
                <span className="font-mono text-micro uppercase tracking-wider text-text-muted">
                  LLM budget
                </span>
                <span className="font-mono text-micro text-text-muted">
                  {budget.used.toLocaleString()} / {budget.limit.toLocaleString()}
                </span>
              </div>
              <span className="block h-1 w-full bg-border-subtle">
                <span
                  className={"block h-1 " + (budget.exhausted ? "bg-semantic-down" : "bg-brand")}
                  style={{ width: `${Math.max(2, used * 100)}%` }}
                />
              </span>
            </div>
          )}
        </div>
      )}

      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-bg-panel-hover"
      >
        <span className={"h-1.5 w-1.5 shrink-0 " + tone} />
        <span
          className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted"
          title={unconfigured > 0 ? `${unconfigured} optional provider(s) have no credentials configured.` : undefined}
        >
          {down > 0 ? `${down} down` : degraded > 0 ? `${degraded} degraded` : "systems"}
        </span>
        <span
          className={"ml-auto shrink-0 font-mono text-micro " + (live ? "text-brand" : "text-text-muted")}
          title={live ? "Streaming" : anyOpen ? "Not connected" : "Every venue is closed"}
        >
          {live ? "LIVE" : anyOpen ? "OFFLINE" : "CLOSED"}
        </span>
      </button>

      {/* A key per person is only worth having if the interface says which one
          is in use — otherwise the audit trail exists in the database and
          nowhere a person will look. */}
      <div className="flex items-center gap-2 border-t border-border-subtle px-3 py-2">
        <span className="min-w-0 flex-1 truncate font-mono text-meta text-text-secondary">
          {auth?.profile?.name ?? "—"}
        </span>
        {auth?.profile && auth.profile.role !== "operator" && (
          <span
            className={
              "shrink-0 border px-1 font-mono text-micro uppercase tracking-wider " +
              (auth.profile.role === "viewer"
                ? "border-border-focus text-text-muted"
                : "border-brand/40 text-brand")
            }
            title={`key ${auth.profile.prefix}…`}
          >
            {auth.profile.role}
          </span>
        )}
        {auth?.required && (
          <button
            type="button"
            title="Sign out"
            onClick={async () => {
              await api.logout();
              qc.invalidateQueries();
            }}
            className="shrink-0 text-text-muted transition-colors hover:text-brand"
          >
            <LogOut size={13} />
          </button>
        )}
      </div>
    </div>
  );
}
