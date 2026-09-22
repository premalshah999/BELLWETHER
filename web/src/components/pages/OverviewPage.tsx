import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  Bell,
  CalendarClock,
  Newspaper,
  Radar,
  RefreshCw,
  Search,
  Sparkles,
} from "lucide-react";
import { Link, useNavigate } from "react-router-dom";
import {
  api,
  type Alert,
  type MarketEvent,
  type MorningBrief,
  type ScanFinding,
  type UpcomingCatalyst,
} from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { Empty } from "../ui/Empty";
import { Pill } from "../ui/Pill";

type Attention = {
  id: string;
  kind: "alert" | "move" | "catalyst";
  title: string;
  detail: string;
  symbol?: string;
  priority: number;
};

/** The opening screen: a short queue of what deserves attention now. */
export function OverviewPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const navigate = useNavigate();
  const qc = useQueryClient();

  const alerts = useQuery({
    queryKey: ["alerts", "overview"],
    queryFn: () => api.alerts({ limit: 20, unread: true }),
    refetchInterval: 30_000,
  });
  const scan = useQuery({
    queryKey: ["scan-latest", "overview"],
    queryFn: () => api.scanLatest(30),
    refetchInterval: 120_000,
  });
  const calendar = useQuery({
    queryKey: ["calendar", "overview"],
    queryFn: () => api.calendar({ days: 14, limit: 30 }),
    staleTime: 10 * 60_000,
  });
  const news = useQuery({
    queryKey: ["events", "overview"],
    queryFn: () => api.events({ hours: 48, minImportance: 50, limit: 16 }),
    refetchInterval: 60_000,
  });
  const brief = useQuery({
    queryKey: ["morning-brief"],
    queryFn: api.morningBrief,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const generateBrief = useMutation({
    mutationFn: api.generateMorningBrief,
    onSuccess: (data) => qc.setQueryData(["morning-brief"], data),
  });

  const findings = scan.data?.findings ?? [];
  const unread = alerts.data?.alerts ?? [];
  const catalysts = calendar.data?.catalysts ?? [];
  const events = news.data?.events ?? [];
  const unresolved = findings.filter((finding) => finding.explained !== true);
  const urgentCatalysts = catalysts.filter((catalyst) => (catalyst.next_in_days ?? 99) <= 7);
  const attention: Attention[] = [
    // Cap each source before merging so a large scan cannot crowd every
    // alert or catalyst out of the one queue meant to combine them.
    ...unread.slice(0, 3).map(alertAttention),
    ...unresolved.slice(0, 4).map(moveAttention),
    ...urgentCatalysts.slice(0, 3).map(catalystAttention),
  ]
    .sort((a, b) => b.priority - a.priority)
    .slice(0, 10);

  const openSymbol = (symbol: string) => {
    onSelect(symbol);
    navigate("/charts");
  };
  const loading = alerts.isLoading || scan.isLoading || calendar.isLoading || news.isLoading;
  const date = new Intl.DateTimeFormat(undefined, {
    weekday: "long",
    month: "long",
    day: "numeric",
  }).format(new Date());

  return (
    <div className="min-h-0 flex-1 overflow-y-auto bg-bg-base">
      <header className="border-b border-border-subtle bg-bg-panel px-5 py-5 lg:px-7">
        <div className="mx-auto flex max-w-[1500px] flex-wrap items-end justify-between gap-4">
          <div>
            <p className="font-mono text-micro uppercase tracking-[0.14em] text-brand">Today</p>
            <h1 className="mt-1 text-hero font-semibold tracking-tight text-text-primary">
              Your market, distilled
            </h1>
            <p className="mt-1 text-ui text-text-muted">{date} · Start with what changed.</p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Link to="/research" className="action-secondary">
              <Search size={13} /> Research a question
            </Link>
            <Link to="/charts" className="action-primary">
              Open charts <ArrowRight size={13} />
            </Link>
          </div>
        </div>
      </header>

      <div className="mx-auto max-w-[1500px] p-4 lg:p-6">
        <section className="grid grid-cols-2 border border-border-subtle bg-bg-panel md:grid-cols-4">
          <Metric label="Unread alerts" value={alerts.data?.unread ?? unread.length} hint={unread.length ? "rules fired" : "nothing waiting"} tone={unread.length ? "brand" : "quiet"} to="/alerts" />
          <Metric label="Moves to review" value={unresolved.length} hint="unexplained or unchecked" tone={unresolved.length ? "brand" : "quiet"} to="/scanner/signals" />
          <Metric label="Catalysts · 7d" value={urgentCatalysts.length} hint="earnings and dividends" tone={urgentCatalysts.length ? "normal" : "quiet"} to="/calendar" />
          <Metric label="Material news · 48h" value={events.length} hint="importance 50+" tone={events.length ? "normal" : "quiet"} to="/news" />
        </section>

        <div className="mt-4 grid min-w-0 gap-4 xl:grid-cols-[minmax(0,1.55fr)_minmax(320px,0.8fr)]">
          <section className="border border-border-subtle bg-bg-panel">
            <SectionHeader icon={Bell} title="Needs attention" subtitle="Alerts, unexplained moves, and near-term catalysts" to="/alerts" />
            {loading && attention.length === 0 ? (
              <LoadingRows />
            ) : attention.length === 0 ? (
              <Empty icon={Bell} title="Nothing needs action right now." hint="New rule triggers, unusual moves, and catalysts inside seven days will appear here." />
            ) : (
              <div className="divide-y divide-border-subtle">
                {attention.map((item) => (
                  <button
                    key={item.id}
                    type="button"
                    onClick={() => {
                      if (item.kind === "move" && item.symbol) openSymbol(item.symbol);
                      else navigate(item.kind === "catalyst" ? "/calendar" : "/alerts");
                    }}
                    className="group flex w-full items-start gap-3 px-4 py-3 text-left transition-colors hover:bg-bg-panel-hover"
                  >
                    <AttentionMark kind={item.kind} />
                    <span className="min-w-0 flex-1">
                      <span className="block text-ui text-text-primary">{item.title}</span>
                      <span className="mt-0.5 block text-meta text-text-muted">{item.detail}</span>
                    </span>
                    {item.symbol && <span className="shrink-0 font-mono text-meta text-text-secondary">{item.symbol}</span>}
                    <ArrowRight size={12} className="mt-1 shrink-0 text-text-muted opacity-0 transition-opacity group-hover:opacity-100" />
                  </button>
                ))}
              </div>
            )}
          </section>

          <BriefCard
            data={brief.data?.brief ?? null}
            stale={brief.data?.stale ?? false}
            note={brief.data?.note}
            loading={brief.isLoading || generateBrief.isPending}
            error={(brief.error ?? generateBrief.error) as Error | null}
            onGenerate={() => generateBrief.mutate()}
          />
        </div>

        <div className="mt-4 grid min-w-0 gap-4 xl:grid-cols-2">
          <section className="min-w-0 border border-border-subtle bg-bg-panel">
            <SectionHeader icon={Radar} title="Largest market moves" subtitle="Statistical signals from the latest scan" to="/scanner/signals" />
            {findings.length === 0 && !scan.isLoading ? (
              <Empty icon={Radar} title="No scan findings yet." hint="The scheduled scanner will populate this list." />
            ) : (
              <div className="divide-y divide-border-subtle">
                {findings.slice(0, 8).map((finding) => (
                  <button
                    key={finding.id}
                    type="button"
                    onClick={() => openSymbol(finding.symbol)}
                    className="grid w-full grid-cols-[minmax(90px,1fr)_90px_90px] items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-bg-panel-hover sm:grid-cols-[minmax(100px,1fr)_minmax(120px,1.4fr)_90px_90px]"
                  >
                    <span className="font-mono text-ui text-text-primary">{finding.symbol}</span>
                    <span className="hidden truncate text-meta text-text-muted sm:block">{signalLabel(finding)}</span>
                    <span className={"text-right font-mono text-ui " + returnTone(finding.return_1d)}>{signed(finding.return_1d)}%</span>
                    <span className="text-right font-mono text-meta text-text-muted">{finding.volume_ratio.toFixed(1)}× vol</span>
                  </button>
                ))}
              </div>
            )}
          </section>

          <section className="min-w-0 border border-border-subtle bg-bg-panel">
            <SectionHeader icon={Newspaper} title="Material news" subtitle="Recent high-importance events" to="/news" />
            {events.length === 0 && !news.isLoading ? (
              <Empty icon={Newspaper} title="No material events in this window." />
            ) : (
              <div className="divide-y divide-border-subtle">
                {events.slice(0, 8).map((event) => <NewsRow key={event.id} event={event} />)}
              </div>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}

function Metric({ label, value, hint, tone, to }: { label: string; value: number; hint: string; tone: "brand" | "normal" | "quiet"; to: string }) {
  const color = tone === "brand" ? "text-brand" : tone === "quiet" ? "text-text-muted" : "text-text-primary";
  return (
    <Link to={to} className="group min-w-0 border-b border-r border-border-subtle px-4 py-4 transition-colors hover:bg-bg-panel-hover md:border-b-0">
      <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">{label}</span>
      <span className={"mt-1 block font-mono text-hero " + color}>{value}</span>
      <span className="mt-0.5 flex items-center gap-1 text-meta text-text-muted">{hint} <ArrowRight size={10} className="opacity-0 transition-opacity group-hover:opacity-100" /></span>
    </Link>
  );
}

function SectionHeader({ icon: Icon, title, subtitle, to }: { icon: typeof Bell; title: string; subtitle: string; to: string }) {
  return (
    <header className="flex min-h-12 items-center gap-3 border-b border-border-subtle px-4 py-2.5">
      <Icon size={14} className="shrink-0 text-text-muted" />
      <div className="min-w-0 flex-1">
        <h2 className="text-ui font-medium text-text-primary">{title}</h2>
        <p className="truncate text-meta text-text-muted">{subtitle}</p>
      </div>
      <Link to={to} className="flex shrink-0 items-center gap-1 font-mono text-meta text-text-muted hover:text-brand">View all <ArrowRight size={10} /></Link>
    </header>
  );
}

function BriefCard({ data, stale, note, loading, error, onGenerate }: { data: MorningBrief | null; stale: boolean; note?: string; loading: boolean; error: Error | null; onGenerate: () => void }) {
  return (
    <section className="border border-border-subtle bg-bg-panel">
      <header className="flex min-h-12 items-center gap-3 border-b border-border-subtle px-4 py-2.5">
        <Sparkles size={14} className="text-brand" />
        <div className="min-w-0 flex-1">
          <h2 className="text-ui font-medium text-text-primary">Morning brief</h2>
          <p className="text-meta text-text-muted">{data ? `${formatAgo(data.generated_at)}${stale ? " · stale" : ""}` : "Your watchlist and market context"}</p>
        </div>
        <button type="button" onClick={onGenerate} disabled={loading} className="flex items-center gap-1.5 font-mono text-meta text-text-muted hover:text-brand disabled:opacity-50">
          <RefreshCw size={11} className={loading ? "animate-spin" : ""} /> {data ? "refresh" : "generate"}
        </button>
      </header>
      {data ? (
        <div className="px-4 py-4">
          <h3 className="text-emphasis font-medium leading-snug text-text-primary">{data.headline}</h3>
          <ul className="mt-3 space-y-2">
            {(data.bullets ?? []).slice(0, 5).map((bullet, index) => (
              <li key={index} className="flex gap-2 text-ui leading-relaxed text-text-secondary"><span className="mt-[0.55em] h-1 w-1 shrink-0 bg-brand" /><span>{bullet}</span></li>
            ))}
          </ul>
          {(data.watch_today ?? []).length > 0 && (
            <div className="mt-4 border-t border-border-subtle pt-3">
              <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">Watch today</p>
              <ul className="mt-2 space-y-1.5">
                {data.watch_today.slice(0, 5).map((item) => (
                  <li key={item} className="flex gap-2 text-meta leading-relaxed text-text-secondary">
                    <span className="mt-[0.55em] h-1 w-1 shrink-0 bg-brand" />
                    <span className="min-w-0 break-words">{item}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      ) : (
        <div className="px-4 py-7">
          <p className="max-w-[48ch] text-ui leading-relaxed text-text-secondary">{error ? error.message : note ?? "Generate a concise briefing from your alerts, positions, and recent news."}</p>
          <button type="button" onClick={onGenerate} disabled={loading} className="action-primary mt-4"><Sparkles size={13} /> {loading ? "Building brief…" : "Generate today’s brief"}</button>
        </div>
      )}
    </section>
  );
}

function NewsRow({ event }: { event: MarketEvent }) {
  const body = <><span className="block text-ui leading-snug text-text-primary">{event.headline}</span><span className="mt-1 flex flex-wrap items-center gap-2 text-meta text-text-muted"><span>{event.event_type.toLowerCase().replace(/_/g, " ")}</span>{event.official && <Pill tone="neutral">official</Pill>}<span>{formatAgo(event.published_at ?? event.discovered_at)}</span>{event.source_count > 1 && <span>{event.source_count} sources</span>}</span></>;
  return event.primary_url ? <a href={event.primary_url} target="_blank" rel="noreferrer noopener" className="block px-4 py-3 hover:bg-bg-panel-hover">{body}</a> : <div className="px-4 py-3">{body}</div>;
}

function AttentionMark({ kind }: { kind: Attention["kind"] }) {
  const Icon = kind === "alert" ? Bell : kind === "move" ? Radar : CalendarClock;
  return <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center border border-border-subtle text-text-muted"><Icon size={12} /></span>;
}

function LoadingRows() {
  return <div className="divide-y divide-border-subtle" aria-label="Loading overview">{[0, 1, 2, 3].map((n) => <div key={n} className="flex items-center gap-3 px-4 py-3"><span className="h-6 w-6 animate-pulse bg-border-subtle" /><span className="h-3 w-2/3 animate-pulse bg-border-subtle" /></div>)}</div>;
}

function alertAttention(alert: Alert): Attention {
  return { id: `alert-${alert.id}`, kind: "alert", title: alert.summary || `${alert.algorithm_name} triggered`, detail: `${alert.algorithm_name} · ${formatAgo(alert.fired_at)}`, symbol: alert.symbol, priority: 300 + new Date(alert.fired_at).getTime() / 1e13 };
}

function moveAttention(finding: ScanFinding): Attention {
  const state = finding.explained === false ? "No matching news found" : "Explanation not checked";
  return { id: `move-${finding.id}`, kind: "move", title: `${signed(finding.return_1d)}% move · ${finding.volume_ratio.toFixed(1)}× volume`, detail: `${state} · ${signalLabel(finding)}`, symbol: finding.symbol, priority: 200 + finding.score };
}

function catalystAttention(catalyst: UpcomingCatalyst): Attention {
  const days = catalyst.next_in_days ?? 99;
  return { id: `catalyst-${catalyst.symbol}-${catalyst.next_kind}`, kind: "catalyst", title: `${catalyst.next_kind ?? "Catalyst"} ${days === 0 ? "today" : days === 1 ? "tomorrow" : `in ${days} days`}`, detail: catalyst.industry || "Scheduled company event", symbol: catalyst.symbol, priority: 150 - days };
}

function signalLabel(finding: ScanFinding) {
  return finding.signals.length ? finding.signals.slice(0, 2).join(" · ").replace(/_/g, " ") : `score ${finding.score.toFixed(1)}`;
}

function signed(value: number) { return `${value >= 0 ? "+" : ""}${value.toFixed(2)}`; }
function returnTone(value: number) { return value > 0 ? "text-semantic-up" : value < 0 ? "text-semantic-down" : "text-text-secondary"; }
