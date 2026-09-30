import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, CalendarClock, ChevronRight, Radar, RefreshCw, ShieldCheck, Sparkles } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, type Alert, type MarketEvent, type MorningBrief, type ScanFinding, type UpcomingCatalyst } from "../../lib/api";
import { formatAgo, formatClock } from "../../lib/format";
import { humanMinutes, sessionState } from "../../lib/session";
import { signalSentence, signed, toneOf } from "../../lib/signals";
import { EventDrawer, importanceLabel, typeLabel } from "../EventDrawer";

type Attention = {
  id: string;
  kind: "alert" | "move" | "catalyst";
  symbol?: string;
  title: ReactNode;
  detail: string;
  priority: number;
  go: () => void;
};

/**
 * The first screen of the day.
 *
 * It opens with a sentence rather than a row of counters: "11 moves need a
 * look and 3 companies report this week" is read in a second, where four
 * big numbers under four small labels have to be decoded one at a time.
 */
export function OverviewPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [openEvent, setOpenEvent] = useState<MarketEvent | null>(null);

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
    queryFn: () => api.calendar({ days: 7, limit: 60 }),
    staleTime: 10 * 60_000,
  });
  // Importance runs 0-10. This asked for 50+ and so was always empty.
  const news = useQuery({
    queryKey: ["events", "overview"],
    queryFn: () => api.events({ hours: 48, minImportance: 6, limit: 12 }),
    refetchInterval: 60_000,
  });
  const brief = useQuery({ queryKey: ["morning-brief"], queryFn: api.morningBrief, retry: false, staleTime: 5 * 60_000 });
  const generateBrief = useMutation({
    mutationFn: api.generateMorningBrief,
    onSuccess: (data) => qc.setQueryData(["morning-brief"], data),
  });

  const toChart = (symbol: string) => {
    onSelect(symbol);
    navigate("/charts");
  };

  const findings = scan.data?.findings ?? [];
  const unread = alerts.data?.alerts ?? [];
  const unreadCount = alerts.data?.unread ?? unread.length;
  const catalysts = calendar.data?.catalysts ?? [];
  const events = news.data?.events ?? [];
  const unexplained = findings.filter((f) => f.explained !== true);
  const earningsSoon = catalysts.filter((c) => c.next_kind?.toLowerCase().includes("earn"));

  const attention: Attention[] = [
    ...unread.slice(0, 3).map((a) => alertItem(a, () => navigate("/alerts"))),
    ...unexplained.slice(0, 5).map((f) => moveItem(f, () => toChart(f.symbol))),
    ...catalysts
      .filter((c) => (c.next_in_days ?? 99) <= 2)
      .slice(0, 3)
      .map((c) => catalystItem(c, () => navigate("/calendar"))),
  ]
    .sort((a, b) => b.priority - a.priority)
    .slice(0, 8);

  const loading = alerts.isLoading || scan.isLoading || calendar.isLoading;
  const us = sessionState("US");
  const date = new Intl.DateTimeFormat("en-US", { weekday: "long", month: "long", day: "numeric" }).format(new Date());

  return (
    <div className="min-h-0 flex-1 overflow-y-auto bg-bg-base">
      <div className="mx-auto flex max-w-[1320px] flex-col gap-8 px-5 pb-12 pt-7 md:px-8 md:pt-9">
        <header className="flex flex-wrap items-end justify-between gap-x-8 gap-y-3">
          <div className="min-w-0 max-w-3xl">
            <p className="text-ui text-text-muted">
              {date}
              <span className="mx-2 text-border-focus">/</span>
              <span className={us.open ? "text-semantic-up" : ""}>
                {us.open
                  ? `Market open, ${humanMinutes(us.closesInMinutes ?? 0)} to the close`
                  : `Market closed, opens in ${humanMinutes(us.opensInMinutes ?? 0)}`}
              </span>
            </p>
            <h1 className="font-reading mt-2 text-[28px] font-semibold leading-tight text-text-primary md:text-[32px]">
              {loading ? <span className="skeleton inline-block h-9 w-[28rem] max-w-full" /> : headline(unexplained.length, earningsSoon.length, unreadCount)}
            </h1>
          </div>
          <div className="flex gap-2">
            <Link to="/research" className="action-secondary">
              Ask a question
            </Link>
            <Link to="/news" className="action-primary">
              Read the news
            </Link>
          </div>
        </header>

        <div className="grid min-w-0 gap-6 lg:grid-cols-[minmax(0,1.5fr)_minmax(320px,1fr)]">
          <Card title="Needs a look" to="/scanner/signals" linkLabel="All signals">
            {loading && attention.length === 0 ? (
              <Rows />
            ) : attention.length === 0 ? (
              <Quiet>Nothing is waiting. New alerts, unexplained moves and catalysts in the next two days will appear here.</Quiet>
            ) : (
              <ul className="divide-y divide-border-subtle">
                {attention.map((item) => (
                  <li key={item.id}>
                    <button
                      type="button"
                      onClick={item.go}
                      className="group flex w-full items-center gap-4 px-5 py-3.5 text-left transition-colors hover:bg-bg-panel-hover"
                    >
                      <KindIcon kind={item.kind} />
                      <span className="w-14 shrink-0 text-ui font-semibold text-text-primary">{item.symbol}</span>
                      <span className="min-w-0 flex-1">
                        <span className="block text-ui text-text-primary sm:truncate">{item.title}</span>
                        <span className="block text-meta text-text-muted sm:truncate">{item.detail}</span>
                      </span>
                      <ChevronRight size={16} className="shrink-0 text-text-muted transition-transform group-hover:translate-x-0.5" />
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <BriefCard
            data={brief.data?.brief ?? null}
            stale={brief.data?.stale ?? false}
            note={brief.data?.note}
            loading={brief.isLoading || generateBrief.isPending}
            error={(brief.error ?? generateBrief.error) as Error | null}
            onGenerate={() => generateBrief.mutate()}
          />
        </div>

        <div className="grid min-w-0 gap-6 lg:grid-cols-2">
          <Card title="Biggest moves" to="/scanner/signals" linkLabel="Open signals">
            {findings.length === 0 && !scan.isLoading ? (
              <Quiet>The scanner has not run yet today. It runs through every trading session.</Quiet>
            ) : (
              <table className="w-full text-ui">
                <thead>
                  <tr className="text-left text-meta text-text-muted">
                    <th className="px-5 py-2 font-medium">Company</th>
                    <th className="px-2 py-2 font-medium max-sm:hidden">Why it was flagged</th>
                    <th className="px-2 py-2 text-right font-medium">Day</th>
                    <th className="px-5 py-2 text-right font-medium">Volume</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border-subtle border-t border-border-subtle">
                  {[...findings]
                    .sort((a, b) => Math.abs(b.return_1d) - Math.abs(a.return_1d))
                    .slice(0, 8)
                    .map((f) => (
                      <tr key={f.id} onClick={() => toChart(f.symbol)} className="cursor-pointer transition-colors hover:bg-bg-panel-hover">
                        <td className="px-5 py-2.5">
                          <button type="button" onClick={() => toChart(f.symbol)} className="font-semibold text-text-primary">
                            {f.symbol}
                          </button>
                        </td>
                        <td className="truncate px-2 py-2.5 text-meta text-text-secondary max-sm:hidden">{signalSentence(f.signals)}</td>
                        <td className={"px-2 py-2.5 text-right font-medium " + toneOf(f.return_1d)}>{signed(f.return_1d)}%</td>
                        <td className="px-5 py-2.5 text-right text-text-secondary">{f.volume_ratio.toFixed(1)}×</td>
                      </tr>
                    ))}
                </tbody>
              </table>
            )}
          </Card>

          <Card title="What mattered in the news" to="/news?importance=significant&window=3d" linkLabel="All news">
            {news.isLoading ? (
              <Rows />
            ) : events.length === 0 ? (
              <Quiet>No significant events in the last two days.</Quiet>
            ) : (
              <ul className="divide-y divide-border-subtle">
                {events.slice(0, 7).map((e) => (
                  <li key={e.id}>
                    <button
                      type="button"
                      onClick={() => setOpenEvent(e)}
                      className="block w-full px-5 py-3 text-left transition-colors hover:bg-bg-panel-hover"
                    >
                      <span className="font-reading line-clamp-2 block text-emphasis font-medium leading-snug text-text-primary">{e.headline}</span>
                      <span className="mt-1 flex flex-wrap items-center gap-x-2.5 text-meta text-text-muted">
                        <span className="inline-flex items-center gap-1 text-text-secondary">
                          {e.official && <ShieldCheck size={13} className="text-brand" />}
                          {e.source}
                        </span>
                        <span>{typeLabel(e.event_type)}</span>
                        {importanceLabel(e.importance) === "Major" && <span className="font-medium text-brand">Major</span>}
                        <span>{formatClock(e.published_at || e.discovered_at)}</span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      </div>

      <EventDrawer
        id={openEvent?.id ?? null}
        seed={openEvent ?? undefined}
        onClose={() => setOpenEvent(null)}
        onSymbol={(s) => {
          setOpenEvent(null);
          toChart(s);
        }}
      />
    </div>
  );
}

function headline(moves: number, earnings: number, alerts: number): string {
  const parts: string[] = [];
  if (moves > 0) parts.push(`${moves} ${moves === 1 ? "move needs" : "moves need"} a look`);
  if (earnings > 0) parts.push(`${earnings} ${earnings === 1 ? "company reports" : "companies report"} this week`);
  if (alerts > 0) parts.push(`${alerts} ${alerts === 1 ? "alert is" : "alerts are"} unread`);
  if (parts.length === 0) return "A quiet market. Nothing needs you yet.";
  const last = parts.pop()!;
  const s = parts.length ? `${parts.join(", ")} and ${last}` : last;
  return s.charAt(0).toUpperCase() + s.slice(1) + ".";
}

function Card({ title, to, linkLabel, children }: { title: string; to: string; linkLabel: string; children: ReactNode }) {
  return (
    <section className="min-w-0 overflow-hidden rounded-lg border border-border-subtle bg-bg-panel">
      <header className="flex h-12 items-center justify-between gap-3 px-5">
        <h2 className="text-emphasis font-semibold text-text-primary">{title}</h2>
        <Link to={to} className="text-meta font-medium text-text-secondary underline-offset-4 hover:text-brand hover:underline">
          {linkLabel}
        </Link>
      </header>
      <div className="border-t border-border-subtle">{children}</div>
    </section>
  );
}

function Quiet({ children }: { children: ReactNode }) {
  return <p className="max-w-[52ch] px-5 py-8 text-ui text-text-secondary">{children}</p>;
}

function Rows() {
  return (
    <div className="flex flex-col gap-2 p-4" aria-busy="true">
      {[0, 1, 2, 3].map((n) => (
        <span key={n} className="skeleton h-11 w-full" />
      ))}
    </div>
  );
}

function KindIcon({ kind }: { kind: Attention["kind"] }) {
  const Icon = kind === "alert" ? Bell : kind === "move" ? Radar : CalendarClock;
  const label = kind === "alert" ? "Alert" : kind === "move" ? "Unexplained move" : "Upcoming catalyst";
  return (
    <span
      title={label}
      className={
        "flex h-8 w-8 shrink-0 items-center justify-center rounded-full " +
        (kind === "alert" ? "bg-brand-muted text-brand" : kind === "move" ? "bg-info-soft text-info" : "bg-bg-panel-hover text-text-secondary")
      }
    >
      <Icon size={15} />
    </span>
  );
}

function BriefCard({
  data,
  stale,
  note,
  loading,
  error,
  onGenerate,
}: {
  data: MorningBrief | null;
  stale: boolean;
  note?: string;
  loading: boolean;
  error: Error | null;
  onGenerate: () => void;
}) {
  return (
    <section className="min-w-0 overflow-hidden rounded-lg border border-border-subtle bg-bg-panel">
      <header className="flex h-12 items-center justify-between gap-3 px-5">
        <h2 className="flex items-center gap-2 text-emphasis font-semibold text-text-primary">
          <Sparkles size={15} className="text-brand" /> Morning brief
        </h2>
        {data && (
          <button type="button" onClick={onGenerate} disabled={loading} className="action-ghost text-meta">
            <RefreshCw size={13} className={loading ? "animate-spin" : ""} /> {loading ? "Rewriting…" : "Rewrite"}
          </button>
        )}
      </header>
      <div className="border-t border-border-subtle px-5 py-5">
        {data ? (
          <>
            <p className="text-meta text-text-muted">
              Written {formatAgo(data.generated_at)}
              {stale ? ", before the latest data arrived" : ""}
            </p>
            <h3 className="font-reading mt-2 text-[17px] font-semibold leading-snug text-text-primary">{data.headline}</h3>
            <ul className="mt-4 flex flex-col gap-3">
              {(data.bullets ?? []).slice(0, 5).map((b, i) => (
                <li key={i} className="flex gap-3 text-ui leading-relaxed text-text-secondary">
                  <span className="mt-[0.6em] h-1.5 w-1.5 shrink-0 rounded-full bg-brand" />
                  <span>{b}</span>
                </li>
              ))}
            </ul>
            {(data.watch_today ?? []).length > 0 && (
              <div className="mt-5 rounded-md bg-bg-base px-4 py-3">
                <p className="text-meta font-semibold text-text-secondary">Watch today</p>
                <ul className="mt-1.5 flex flex-col gap-1">
                  {data.watch_today.slice(0, 5).map((w) => (
                    <li key={w} className="text-meta leading-relaxed text-text-secondary">
                      {w}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </>
        ) : (
          <>
            <p className="max-w-[46ch] text-ui leading-relaxed text-text-secondary">
              {error ? error.message : note ?? "A short read on your watchlist, positions and overnight news, with sources."}
            </p>
            <button type="button" onClick={onGenerate} disabled={loading} className="action-primary mt-4">
              <Sparkles size={14} /> {loading ? "Writing the brief…" : "Write today’s brief"}
            </button>
          </>
        )}
      </div>
    </section>
  );
}

function alertItem(a: Alert, go: () => void): Attention {
  return {
    id: `alert-${a.id}`,
    kind: "alert",
    symbol: a.symbol,
    title: a.summary || `${a.algorithm_name} triggered`,
    detail: `${a.algorithm_name}, ${formatAgo(a.fired_at)}`,
    priority: 300 + new Date(a.fired_at).getTime() / 1e13,
    go,
  };
}

function moveItem(f: ScanFinding, go: () => void): Attention {
  return {
    id: `move-${f.id}`,
    kind: "move",
    symbol: f.symbol,
    title: (
      <>
        <span className={"font-semibold " + toneOf(f.return_1d)}>{signed(f.return_1d)}%</span> on {f.volume_ratio.toFixed(1)}× normal volume
      </>
    ),
    detail: f.explained === false ? "No news explains it yet" : signalSentence(f.signals),
    priority: 200 + f.score,
    go,
  };
}

function catalystItem(c: UpcomingCatalyst, go: () => void): Attention {
  const days = c.next_in_days ?? 99;
  const kind = (c.next_kind ?? "Catalyst").replace(/_/g, " ");
  return {
    id: `cat-${c.symbol}-${c.next_kind}`,
    kind: "catalyst",
    symbol: c.symbol,
    title: `${kind.charAt(0).toUpperCase() + kind.slice(1)} ${days === 0 ? "today" : days === 1 ? "tomorrow" : `in ${days} days`}`,
    detail:
      kind.toLowerCase().includes("earn") && c.eps_average != null
        ? `Analysts expect EPS of ${c.eps_average.toFixed(2)}`
        : c.industry || "Scheduled",
    priority: 150 - days,
    go,
  };
}
