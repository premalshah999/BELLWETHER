import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Newspaper, RefreshCw, Sparkles } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type Interval } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { tickerOf, venueOf } from "../../lib/symbol";
import { usePersisted, useResize } from "../../lib/layout";
import { Divider } from "../ui/Divider";
import { DEFAULT_STUDIES, KLineChart } from "../KLineChart";
import { BarAge } from "../BarAge";
import { Empty } from "../ui/Empty";
import { Panel } from "../ui/Panel";
import { Pill } from "../ui/Pill";

/** Ranges offered on the dashboard chart, in trading days. */
const RANGES = [
  { days: 5, label: "5D" },
  { days: 22, label: "30D" },
  { days: 66, label: "3M" },
  { days: 126, label: "6M" },
  { days: 252, label: "1Y" },
] as const;

/**
 * Bars in one trading session, per interval -- sized for a US-hours 6h30m
 * session (390 one-minute bars) rather than NSE's shorter 6h15m (375),
 * since this only ever decides how many bars to *request*: asking for
 * slightly more than an NSE session actually has is harmless, while sizing
 * to NSE's shorter session would under-fetch and silently truncate a US
 * intraday range.
 */
const PER_SESSION: Record<Interval, number> = {
  "1m": 390,
  "5m": 78,
  "15m": 26,
  "1h": 7,
  "1d": 1,
  "1wk": 0.2,
};

/**
 * A compact exclusive choice.
 *
 * Written once because the same control appears three times on this screen and
 * had been hand-rolled at each site with slightly different padding, which is
 * exactly the kind of near-miss that makes an interface feel assembled.
 */
function Segments<T extends string | number>({
  options,
  value,
  onChange,
}: {
  options: readonly (readonly [T, string])[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div className="flex border border-border-subtle">
      {options.map(([v, label]) => (
        <button
          key={String(v)}
          type="button"
          onClick={() => onChange(v)}
          className={
            "h-6 border-r border-border-subtle px-2 font-mono text-meta last:border-r-0 transition-colors " +
            (v === value
              ? "bg-brand-muted text-brand"
              : "text-text-muted hover:bg-bg-panel-hover hover:text-text-primary")
          }
        >
          {label}
        </button>
      ))}
    </div>
  );
}

export function DashboardPage({ symbol }: { symbol: string }) {
  // One chart, one instrument. Comparing several is what the Charts page is
  // for, and duplicating its grid here gave two places to learn the same
  // controls and two places for them to disagree.
  const [interval, setInterval] = usePersisted<Interval>("dashboard.interval", "1d");
  const [range, setRange] = usePersisted<number>("dashboard.range", 22);

  // Floored at 3 only to avoid a degenerate chart. It was floored at 30,
  // which silently widened a 30D request into six weeks.
  const bars = Math.min(2000, Math.max(3, Math.ceil(range * PER_SESSION[interval])));
  const {
    data: candles,
    isLoading,
    isFetching,
    dataUpdatedAt,
    refetch,
  } = useQuery({
    queryKey: ["candles", symbol, interval, bars],
    queryFn: () => api.candles(symbol, interval, bars),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  });
  const series = candles?.candles ?? [];
  const lastBar = series.length ? series[series.length - 1] ?? null : null;

  // The chart/news split. Dragged rather than fixed: a reader following a
  // story wants the feed tall, and one reading price action wants it gone.
  const bottom = useResize({
    key: "dashboard.bottom",
    initial: 288,
    min: 40,
    max: 640,
    direction: "n",
  });
  // The split between the two bottom panes.
  const news = useResize({
    key: "dashboard.news",
    initial: 560,
    min: 260,
    max: 1100,
    direction: "e",
  });

  const { data: quote } = useQuery({
    queryKey: ["quote", symbol],
    queryFn: () => api.quote(symbol),
    refetchInterval: 60_000,
  });

  const q = quote?.quote;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* Quote strip. The price is the largest thing on the screen because it
          is what an operator looks at first; provenance sits beside it small. */}
      <div className="flex h-16 min-w-0 shrink-0 items-center gap-5 overflow-hidden border-b border-border-subtle bg-bg-panel px-4">
        <div className="flex shrink-0 items-baseline gap-2">
          <span className="font-mono text-display font-semibold tracking-tight text-text-primary">
            {tickerOf(symbol)}
          </span>
          <span className="font-mono text-meta text-text-muted">
            {venueOf(symbol) === "NSE" ? "NSE" : ""}
          </span>
        </div>

        <div className="flex shrink-0 items-baseline gap-2">
          <span className="font-mono text-display text-text-primary">
            {q ? q.price.toFixed(2) : "—"}
          </span>
          {/* How old this price is.
              The feed is minute bars, so a price is up to a minute or so
              behind the market. Stating it is the difference between a
              number a reader can weigh and one they have to guess about —
              and it is the honest answer to "is this live". */}
          {q?.as_of && <PriceAge asOf={q.as_of} />}
          <span
            className={
              "font-mono text-ui " +
              (!q
                ? "text-text-muted"
                : q.change_percent >= 0
                  ? "text-semantic-up"
                  : "text-semantic-down")
            }
          >
            {q ? `${q.change_percent >= 0 ? "+" : ""}${q.change_percent.toFixed(2)}%` : ""}
          </span>
        </div>

        <dl className="hidden shrink-0 items-baseline gap-4 xl:flex">
          <Stat label="High" value={q?.day_high} />
          <Stat label="Low" value={q?.day_low} />
          <Stat label="Vol" value={q?.volume} digits={0} />
        </dl>


      </div>

      {/* The chart's own controls, on their own row.
          They were in the quote strip, where eleven buttons plus the ticker,
          price, age, change and three stats needed 1,047px of a 756px strip
          and overflowed straight over the rails. A strip that has to hold an
          unknown amount of identity is the wrong home for a fixed set of
          controls. */}
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border-subtle bg-bg-panel px-4">
        <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          range
        </span>
        <Segments
          options={RANGES.map((r) => [r.days, r.label] as const)}
          value={range}
          onChange={setRange}
        />
        <span className="ml-2 font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          interval
        </span>
        <Segments
          options={(["5m", "15m", "1h", "1d", "1wk"] as Interval[]).map((i) => [i, i] as const)}
          value={interval}
          onChange={setInterval}
        />
        <div className="ml-auto flex items-center gap-3">
          <BarAge
            bar={lastBar}
            interval={interval}
            fetchedAt={dataUpdatedAt}
            venue={venueOf(symbol)}
          />
          <button
            type="button"
            onClick={() => refetch()}
            title="Fetch the latest bars now"
            className={
              "transition-colors " +
              (isFetching ? "text-brand" : "text-text-muted hover:text-text-primary")
            }
          >
            <RefreshCw size={12} className={isFetching ? "animate-spin" : ""} />
          </button>
          <a
            href="/charts"
            className="font-mono text-meta text-text-muted transition-colors hover:text-brand"
          >
            open in charts →
          </a>
        </div>
      </div>

      {/* The chart. Grows to fill whatever the panes below leave. */}
      <div className="min-h-0 flex-1 bg-bg-panel">
        {isLoading && !candles ? (
          <p className="px-4 py-6 font-mono text-meta text-text-muted">loading price history…</p>
        ) : (
          <KLineChart
            candles={series}
            symbol={symbol}
            interval={interval}
            studies={DEFAULT_STUDIES}
          />
        )}
      </div>

      {/* What happened, and what it might mean.
          Hidden once there is more than one chart: the whole reason to open a
          second chart is to compare, and leaving a news feed and an
          explanation pane underneath would give four charts a third of the
          screen between them. One chart is a study of an instrument and wants
          its context; several are a comparison and want the room. */}
      <Divider resize={bottom} orientation="horizontal" />
      <div className="flex shrink-0" style={{ height: bottom.size }}>
        {/* The dragged width is a preference, not a promise. At 1280 with both
            rails open the bottom row is 672px wide, and a fixed 560px news
            pane left the explainability panel 112px — narrow enough that its
            own header controls overlapped each other. The cap keeps the drag
            while guaranteeing the neighbour stays usable. */}
        <div className="flex min-w-0 shrink" style={{ width: news.size, maxWidth: "62%" }}>
          <NewsPane symbol={symbol} />
        </div>
        <Divider resize={news} orientation="vertical" />
        <ExplainPane symbol={symbol} />
      </div>
    </div>
  );
}

function PriceAge({ asOf }: { asOf: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const t = window.setInterval(() => tick((n) => n + 1), 5_000);
    return () => window.clearInterval(t);
  }, []);

  const seconds = Math.max(0, Math.floor((Date.now() - new Date(asOf).getTime()) / 1000));
  const label = seconds < 90 ? `${seconds}s` : formatAgo(asOf);
  // Amber past two minutes: on a minute-bar feed that is late rather than
  // normal, and is the first sign the upstream has stalled.
  const tone = seconds < 120 ? "text-text-muted" : "text-semantic-down";
  return (
    <span className={"font-mono text-meta " + tone} title={`Price observed ${label} ago`}>
      {label}
    </span>
  );
}

function Stat({ label, value, digits = 2 }: { label: string; value?: number; digits?: number }) {
  return (
    <div className="flex items-baseline gap-1">
      <dt className="font-mono text-micro uppercase tracking-wider text-text-muted">{label}</dt>
      <dd className="font-mono text-meta text-text-secondary">
        {value == null ? "—" : value.toLocaleString(undefined, { maximumFractionDigits: digits })}
      </dd>
    </div>
  );
}

function NewsPane({ symbol }: { symbol: string }) {
  const qc = useQueryClient();
  const { data, isFetching } = useQuery({
    queryKey: ["symbol-events", symbol],
    queryFn: () => api.symbolEvents(symbol, 40),
    refetchInterval: 120_000,
  });

  const events = data?.events ?? [];

  return (
    <Panel
      title="Recent news"
      collapseKey="dashboard.news"
      scroll
      className="min-w-0 flex-1"
      action={
        <button
          type="button"
          onClick={() => qc.invalidateQueries({ queryKey: ["symbol-events", symbol] })}
          className="flex items-center gap-1 font-mono text-meta text-text-muted transition-colors hover:text-brand"
        >
          <RefreshCw size={10} className={isFetching ? "animate-spin" : ""} />
          refresh
        </button>
      }
    >
      {events.length === 0 ? (
        <Empty icon={Newspaper} title="Nothing collected for this instrument yet." />
      ) : (
        <ul className="divide-y divide-border-subtle">
          {events.map((e) => (
            <li key={e.id} className="px-4 py-3">
              <a
                href={e.primary_url || undefined}
                target="_blank"
                rel="noreferrer noopener"
                className={
                  "block text-ui leading-relaxed " +
                  (e.primary_url
                    ? "text-text-primary hover:text-brand"
                    : "cursor-default text-text-primary")
                }
              >
                {e.headline}
              </a>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                {/* "seen" rather than "published" where the timestamp is an
                    aggregator's surfacing time: the distinction is what stops
                    a two-year-old article reading as this morning's. */}
                <span className="font-mono text-meta text-text-muted">
                  {e.timestamp_trust === "observed" ? "seen " : ""}
                  {formatAgo(e.published_at || e.discovered_at)}
                </span>
                {e.event_type && e.event_type !== "UNCLASSIFIED" && (
                  <Pill tone="muted">{e.event_type.replace(/_/g, " ").toLowerCase()}</Pill>
                )}
                {e.official && <Pill tone="brand">official</Pill>}
                {(e.importance ?? 0) >= 7 && <Pill tone="down">high</Pill>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}

/**
 * What the scanner has noticed about this instrument.
 *
 * Fills a pane that was otherwise an empty prompt, and closes a gap in the
 * product rather than in the layout: the scanner runs three times a day and
 * knows whether this instrument has been behaving abnormally, and until now
 * that was only visible on its own page. A reader looking at a chart wants to
 * know whether the move in front of them was already flagged — and whether
 * anything explained it.
 */
function ScannerContext({ symbol }: { symbol: string }) {
  const { data } = useQuery({
    queryKey: ["scan-history", symbol],
    queryFn: () => api.scanHistory(symbol, 10),
    retry: false,
    staleTime: 10 * 60 * 1000,
  });

  const findings = data?.findings ?? [];

  if (findings.length === 0) {
    return (
      <Empty
        icon={Sparkles}
        title="No explanation requested."
        hint="Explain reads today's move against volume and moving averages. Debrief reads every source collected for this instrument."
      />
    );
  }

  return (
    <div className="px-4 py-3">
      <p className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
        scanner history · {tickerOf(symbol)}
      </p>
      <ul className="mt-2 space-y-1.5">
        {findings.map((f) => (
          <li key={f.id} className="flex items-baseline gap-2 font-mono text-meta">
            <span className="w-[74px] shrink-0 text-text-muted">
              {formatAgo(f.scanned_at)}
            </span>
            <span
              className={
                "w-[52px] shrink-0 text-right " +
                (f.return_1d >= 0 ? "text-semantic-up" : "text-semantic-down")
              }
            >
              {f.return_1d >= 0 ? "+" : ""}
              {f.return_1d.toFixed(2)}%
            </span>
            <span className="w-[54px] shrink-0 text-right text-text-secondary">
              {f.volume_ratio.toFixed(1)}× vol
            </span>
            {f.explained === false && <Pill tone="down">unexplained</Pill>}
            {f.explained === true && <Pill tone="muted">explained</Pill>}
          </li>
        ))}
      </ul>
      <p className="mt-2.5 text-meta leading-relaxed text-text-muted">
        Explain reads today's move; debrief reads the whole collected record.
      </p>
    </div>
  );
}

function ExplainPane({ symbol }: { symbol: string }) {
  // Two different questions. Explain reads today's move; debrief reads
  // everything collected about the instrument over weeks. They share a pane
  // because the answer occupies the same space, and only one is ever wanted
  // at a time.
  const explain = useMutation({ mutationFn: () => api.explainMove(symbol) });
  const debrief = useMutation({ mutationFn: () => api.debrief(symbol) });
  const busy = explain.isPending || debrief.isPending;

  return (
    <Panel
      title="Explain"
      collapseKey="dashboard.ai"
      scroll
      className="min-w-0 flex-1"
      action={
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => explain.mutate()}
            disabled={busy}
            className="flex items-center gap-1 font-mono text-meta text-text-muted transition-colors hover:text-brand disabled:opacity-50"
          >
            <Sparkles size={10} />
            {explain.isPending ? "reading…" : "explain move"}
          </button>
          <button
            type="button"
            onClick={() => debrief.mutate()}
            disabled={busy}
            className="font-mono text-meta text-text-muted transition-colors hover:text-brand disabled:opacity-50"
          >
            {debrief.isPending ? "reading…" : "debrief"}
          </button>
        </div>
      }
    >
      {busy && (
        <p className="px-3 py-3 text-meta text-text-muted">
          {explain.isPending
            ? "Reading today's price action against the collected record…"
            : "Reading every source collected for this instrument…"}
        </p>
      )}

      {explain.isError && (
        <p className="px-3 py-3 text-meta text-semantic-down">
          {(explain.error as Error)?.message ?? "The explanation could not be generated."}
        </p>
      )}

      {debrief.data && (
        <div className="px-3.5 py-3">
          <p className="text-ui font-medium leading-snug text-text-primary">
            {debrief.data.headline}
          </p>
          <p className="mt-0.5 font-mono text-micro text-text-muted">
            {debrief.data.period} · {debrief.data.event_count} events,{" "}
            {debrief.data.official_count} official
          </p>
          {(debrief.data.sections ?? []).map((sec, i) => (
            <div key={i} className="mt-2.5">
              <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
                {sec.title}
              </p>
              <p className="mt-0.5 text-ui leading-relaxed text-text-secondary">{sec.body}</p>
            </div>
          ))}
          {!!debrief.data.blind_spots?.length && (
            <div className="mt-2.5 border-t border-border-subtle pt-2">
              <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
                not covered
              </p>
              <ul className="mt-0.5 space-y-0.5">
                {debrief.data.blind_spots.map((b, i) => (
                  <li key={i} className="text-meta leading-relaxed text-text-muted">
                    {b}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}

      {!busy && !explain.data && !debrief.data && !explain.isError && (
        <ScannerContext symbol={symbol} />
      )}

      {explain.data && (
        <div className="px-3.5 py-3">
          <p className="whitespace-pre-wrap text-ui leading-relaxed text-text-secondary">
            {explain.data.explanation}
          </p>
          {!!explain.data.citations?.length && (
            <ul className="mt-2 space-y-1 border-t border-border-subtle pt-2">
              {explain.data.citations.map((c, i) => (
                <li key={i}>
                  <a
                    href={c.url}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="font-mono text-meta text-text-muted hover:text-brand"
                  >
                    [{i + 1}] {c.title}
                  </a>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Panel>
  );
}
