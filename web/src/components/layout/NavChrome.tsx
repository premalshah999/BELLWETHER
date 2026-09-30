import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LogOut, Monitor, Moon, Search, Sun } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api } from "../../lib/api";
import { humanMinutes, sessionState } from "../../lib/session";
import type { StreamState } from "../../lib/stream";
import { useTheme, type ThemeChoice } from "../../lib/theme";

/** The bell of the bellwether: the one drawn mark in the interface. */
export function BrandMark({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true" className="shrink-0">
      <rect width="32" height="32" rx="8" className="fill-brand" />
      <path d="M16 7.5c-3.8 0-6.2 2.9-6.2 6.7v4.6l-2 3.2h16.4l-2-3.2v-4.6c0-3.8-2.4-6.7-6.2-6.7z" className="fill-brand-ink" />
      <circle cx="16" cy="24.8" r="1.9" className="fill-brand-ink" />
    </svg>
  );
}

export function SearchButton({ onClick, compact }: { onClick: () => void; compact?: boolean }) {
  if (compact) {
    return (
      <button
        type="button"
        onClick={onClick}
        aria-label="Search (⌘K)"
        title="Search (⌘K)"
        className="flex h-9 w-9 items-center justify-center rounded-md text-text-secondary transition-colors hover:bg-bg-panel-hover hover:text-text-primary"
      >
        <Search size={17} />
      </button>
    );
  }
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex h-9 w-full items-center gap-2 rounded-md border border-border-subtle bg-bg-base px-2.5 text-left text-text-muted transition-colors hover:border-border-focus hover:text-text-secondary"
    >
      <Search size={15} className="shrink-0" />
      <span className="min-w-0 flex-1 truncate text-ui">Search</span>
      <kbd className="shrink-0 rounded border border-border-subtle px-1.5 text-micro text-text-muted">⌘K</kbd>
    </button>
  );
}

const THEMES: { value: ThemeChoice; label: string; icon: typeof Sun }[] = [
  { value: "system", label: "Match system", icon: Monitor },
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
];

export function ThemeSwitch() {
  const [theme, setTheme] = useTheme();
  return (
    <div role="radiogroup" aria-label="Theme" className="flex rounded-md bg-bg-base p-0.5">
      {THEMES.map((t) => (
        <button
          key={t.value}
          type="button"
          role="radio"
          aria-checked={theme === t.value}
          aria-label={t.label}
          title={t.label}
          onClick={() => setTheme(t.value)}
          className={
            "flex h-7 w-8 items-center justify-center rounded transition-colors " +
            (theme === t.value
              ? "bg-bg-panel text-text-primary shadow-sm"
              : "text-text-muted hover:text-text-primary")
          }
        >
          <t.icon size={14} />
        </button>
      ))}
    </div>
  );
}

/**
 * Health, the market clock, and who is signed in.
 *
 * Folded into one strip at the foot of the navigation, because none of it is
 * read on a schedule — but the dot still changes colour on its own, which is
 * the only part of it that is ever urgent.
 */
export function useSystemStatus(stream: StreamState) {
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
  const providers = health?.providers ?? [];
  // A provider with no credentials was never set up, so it is not down.
  const down = providers.filter(
    (p) => p.status !== "ok" && p.status !== "degraded" && p.status !== "unconfigured",
  );
  const degraded = providers.filter((p) => p.status === "degraded");
  const budget = ai?.budget;
  const us = sessionState("US");
  // Connected is not the same as receiving data: when the market is shut
  // nothing trades, so nothing streams.
  const live = stream.connected && us.open;
  const tone: "ok" | "warn" | "bad" =
    down.length > 0 ? "bad" : degraded.length > 0 || budget?.exhausted ? "warn" : "ok";
  const summary =
    down.length > 0
      ? `${down.length} ${down.length === 1 ? "service" : "services"} down`
      : degraded.length > 0
        ? `${degraded.length} degraded`
        : budget?.exhausted
          ? "AI budget spent"
          : "All systems normal";
  const market = us.open
    ? `Market open, closes in ${humanMinutes(us.closesInMinutes ?? 0)}`
    : `Market closed, opens in ${humanMinutes(us.opensInMinutes ?? 0)}`;
  return { providers, budget, live, tone, summary, market, marketOpen: us.open };
}

const DOT = { ok: "bg-semantic-up", warn: "bg-brand", bad: "bg-semantic-down" } as const;

export function StatusDot({ tone, pulse }: { tone: keyof typeof DOT; pulse?: boolean }) {
  return (
    <span className="relative flex h-2 w-2 shrink-0">
      {pulse && <span className={"absolute inset-0 animate-ping rounded-full opacity-60 " + DOT[tone]} />}
      <span className={"relative h-2 w-2 rounded-full " + DOT[tone]} />
    </span>
  );
}

export function StatusDetails({ stream }: { stream: StreamState }) {
  const s = useSystemStatus(stream);
  const used = s.budget && s.budget.limit > 0 ? Math.min(1, s.budget.used / s.budget.limit) : 0;
  return (
    <div className="flex flex-col">
      <div className="max-h-72 overflow-y-auto py-1">
        {s.providers.map((p) => (
          <div key={p.provider} className="flex items-center gap-2.5 px-3 py-1.5">
            <span
              className={
                "h-1.5 w-1.5 shrink-0 rounded-full " +
                (p.status === "ok"
                  ? "bg-semantic-up"
                  : p.status === "degraded"
                    ? "bg-brand"
                    : p.status === "unconfigured"
                      ? "bg-border-focus"
                      : "bg-semantic-down")
              }
            />
            <span className="min-w-0 flex-1 truncate text-meta text-text-secondary">{p.provider}</span>
            <span className="shrink-0 text-micro text-text-muted">
              {p.status === "unconfigured" ? "not set up" : p.status}
            </span>
          </div>
        ))}
        {s.providers.length === 0 && (
          <p className="px-3 py-2 text-meta text-text-muted">No services reporting yet.</p>
        )}
      </div>
      {s.budget && s.budget.limit > 0 && (
        <div className="border-t border-border-subtle px-3 py-2.5">
          <div className="mb-1.5 flex items-baseline justify-between text-micro text-text-muted">
            <span>Text-model tokens this month</span>
            <span>{Math.round(used * 100)}%</span>
          </div>
          <span className="block h-1 w-full overflow-hidden rounded-full bg-border-subtle">
            <span
              className={"block h-1 rounded-full " + (s.budget.exhausted ? "bg-semantic-down" : "bg-brand")}
              style={{ width: `${Math.max(2, used * 100)}%` }}
            />
          </span>
        </div>
      )}
    </div>
  );
}

export function NavFooter({ shut, stream }: { shut?: boolean; stream: StreamState }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const s = useSystemStatus(stream);
  const { data: auth } = useQuery({
    queryKey: ["auth-status"],
    queryFn: api.authStatus,
    staleTime: 60_000,
  });

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", away);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  const signOut = async () => {
    await api.logout();
    qc.invalidateQueries();
  };

  return (
    <div ref={box} className="relative shrink-0 border-t border-border-subtle p-2">
      {open && (
        <div
          className={
            "absolute bottom-full z-30 mb-2 overflow-hidden rounded-lg border border-border-subtle bg-bg-raised shadow-pop [animation:rise-in_160ms_var(--ease-out)] " +
            (shut ? "left-2 w-72" : "inset-x-2")
          }
        >
          <p className="border-b border-border-subtle px-3 py-2 text-meta text-text-secondary">{s.market}</p>
          <StatusDetails stream={stream} />
          <div className="flex items-center justify-between border-t border-border-subtle px-3 py-2">
            <span className="text-meta text-text-muted">Theme</span>
            <ThemeSwitch />
          </div>
        </div>
      )}

      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        title={shut ? `${s.summary}. ${s.market}.` : undefined}
        className={
          "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left transition-colors hover:bg-bg-panel-hover " +
          (shut ? "justify-center px-0" : "")
        }
      >
        <StatusDot tone={s.tone} pulse={s.live} />
        {!shut && (
          <span className="min-w-0 flex-1">
            <span className="block truncate text-meta text-text-secondary">{s.summary}</span>
            <span className="block truncate text-micro text-text-muted">
              {s.live ? "Streaming live prices" : s.market}
            </span>
          </span>
        )}
      </button>

      {!shut && auth?.profile && (
        <div className="mt-1 flex items-center gap-2 px-2.5 py-1.5">
          <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-brand-muted text-micro font-semibold text-brand">
            {(auth.profile.name || "?").slice(0, 1).toUpperCase()}
          </span>
          <span className="min-w-0 flex-1 truncate text-meta text-text-secondary" title={`Key ${auth.profile.prefix}…`}>
            {auth.profile.name}
            {auth.profile.role !== "operator" && (
              <span className="text-text-muted"> ({auth.profile.role})</span>
            )}
          </span>
          {auth.required && (
            <button
              type="button"
              aria-label="Sign out"
              title="Sign out"
              onClick={signOut}
              className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition-colors hover:bg-bg-panel-hover hover:text-text-primary"
            >
              <LogOut size={14} />
            </button>
          )}
        </div>
      )}
    </div>
  );
}
