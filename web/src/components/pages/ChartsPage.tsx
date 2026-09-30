import { useQuery } from "@tanstack/react-query";
import {
  Activity,
  ChevronDown,
  Columns2,
  Grid2x2,
  Maximize2,
  Minimize2,
  PencilLine,
  Plus,
  RefreshCw,
  Shapes,
  Square,
  Trash2,
  X,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, type Algorithm, type Candle, type Interval, type Node as RuleNode } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { usePersisted, useResize } from "../../lib/layout";
import { detectPatterns, detectStructure, type Direction } from "../../lib/patterns";
import { tickerOf } from "../../lib/symbol";
import { CHART_KINDS, DEFAULT_STUDIES, KLineChart, TOOLS, type ChartKind, type Study } from "../KLineChart";
import { BarAge } from "../BarAge";
import { Divider } from "../ui/Divider";
import { Tabs } from "../ui/Tabs";
import { Segmented } from "../ui/controls";

/**
 * Ranges, and how much history each one needs.
 *
 * `days` is trading days, not calendar days — a month of a daily chart is 22
 * bars, not 30, and asking for 30 would quietly include six weeks. The 30-day
 * view is the one an operator reaches for most, so it is the default.
 */
const RANGES = [
  { id: "1D", label: "1D", days: 1 },
  { id: "5D", label: "5D", days: 5 },
  { id: "1M", label: "30D", days: 22 },
  { id: "3M", label: "3M", days: 66 },
  { id: "6M", label: "6M", days: 126 },
  { id: "1Y", label: "1Y", days: 252 },
  { id: "2Y", label: "2Y", days: 504 },
] as const;

type RangeId = (typeof RANGES)[number]["id"];

/**
 * Bars in one trading session, per interval -- sized for a US-hours 6h30m
 * session: 390 one-minute bars.
 */
const PER_SESSION: Record<Interval, number> = {
  "1m": 390,
  "5m": 78,
  "15m": 26,
  "1h": 7,
  "1d": 1,
  "1wk": 0.2,
};

const INTERVALS: Interval[] = ["1m", "5m", "15m", "1h", "1d", "1wk"];

/** Studies offered, and which pane each belongs in. */
const CATALOGUE: { name: string; params: number[]; pane: "main" | "sub"; label: string }[] = [
  { name: "MA", params: [20, 50, 200], pane: "main", label: "MA 20/50/200" },
  { name: "EMA", params: [9, 21], pane: "main", label: "EMA 9/21" },
  { name: "BOLL", params: [20, 2], pane: "main", label: "Bollinger 20" },
  { name: "SAR", params: [2, 2, 20], pane: "main", label: "Parabolic SAR" },
  { name: "VOL", params: [], pane: "sub", label: "Volume" },
  { name: "RSI", params: [14], pane: "sub", label: "RSI 14" },
  { name: "MACD", params: [12, 26, 9], pane: "sub", label: "MACD" },
  { name: "KDJ", params: [9, 3, 3], pane: "sub", label: "Stochastic" },
  { name: "ATR", params: [14], pane: "sub", label: "ATR 14" },
  { name: "OBV", params: [], pane: "sub", label: "On-balance volume" },
  { name: "CCI", params: [20], pane: "sub", label: "CCI 20" },
  { name: "WR", params: [14], pane: "sub", label: "Williams %R" },
];

/**
 * How many bars a range needs at a given interval.
 *
 * This used to floor at 30, which quietly overrode the operator: picking 30D
 * on a daily chart asked for 30 bars rather than 22 and drew six weeks. A
 * floor still exists, but only to stop a degenerate one-bar chart, and it can
 * no longer widen a range the operator explicitly chose.
 */
function barsFor(range: RangeId, interval: Interval): number {
  const days = RANGES.find((r) => r.id === range)?.days ?? 22;
  return Math.min(2000, Math.max(3, Math.ceil(days * PER_SESSION[interval])));
}

/**
 * The interval a range needs to be legible.
 *
 * One day of a daily chart is a single candle. Rather than draw that, picking
 * a short range moves to an interval fine enough to show it — which is what
 * every trading terminal does and what makes the range buttons mean anything.
 */
function intervalFor(range: RangeId, current: Interval): Interval {
  const finest: Partial<Record<RangeId, Interval>> = { "1D": "5m", "5D": "15m" };
  const needed = finest[range];
  if (!needed) {
    // Long ranges at a fine interval blow past the upstream's history caps,
    // so anything beyond a quarter moves to daily bars.
    if ((range === "1Y" || range === "2Y") && current !== "1d" && current !== "1wk") return "1d";
    return current;
  }
  return PER_SESSION[current] >= PER_SESSION[needed] ? current : needed;
}

const skey = (s: { name: string; params: number[]; pane: string }) =>
  `${s.name}:${s.params.join(",")}:${s.pane}`;

/** Converts the studies on a chart into rule conditions the builder accepts. */
function studiesToConditions(studies: Study[]): RuleNode[] {
  const out: RuleNode[] = [];
  for (const s of studies) {
    switch (s.name) {
      case "RSI":
        out.push({ indicator: "rsi", period: s.params[0] ?? 14, op: "<", value: 30 });
        break;
      case "MACD":
        out.push({ indicator: "macd", op: "crosses_above", compare: { indicator: "macd_signal" } });
        break;
      case "MA":
        out.push({ indicator: "close", op: ">", compare: { indicator: "sma", period: s.params[0] ?? 20 } });
        break;
      case "EMA":
        out.push({ indicator: "close", op: ">", compare: { indicator: "ema", period: s.params[0] ?? 20 } });
        break;
      case "VOL":
        out.push({ indicator: "volume", op: ">", compare: { indicator: "vol_avg", period: 20 } });
        break;
      case "ATR":
        out.push({ indicator: "atr", period: s.params[0] ?? 14, op: ">", value: 0 });
        break;
    }
  }
  return out;
}

interface Pane {
  symbol: string;
  interval: Interval;
  range: RangeId;
  kind: ChartKind;
  studies: Study[];
}

const BLANK_PANE: Pane = {
  symbol: "AAPL",
  interval: "1d",
  range: "1M",
  kind: "candle_solid" as ChartKind,
  studies: DEFAULT_STUDIES,
};

/**
 * The chart workspace.
 *
 * Charts had been a strip on the dashboard, sharing its height with a news
 * feed and an explanation pane, which meant the thing an operator spends most
 * of their time looking at got the least room of anything on the screen. Here
 * they get the whole page, and everything else is either in the rail or one
 * tab away.
 */
export function ChartsPage({
  symbol,
  onSelect,
}: {
  symbol: string;
  onSelect: (s: string) => void;
}) {
  const [layout, setLayout] = usePersisted<1 | 2 | 4>("charts.layout", 1);
  const [panes, setPanes] = usePersisted<Pane[]>("charts.panes.v2", [BLANK_PANE]);
  const [focus, setFocus] = useState(0);
  const [analysisShut, setAnalysisShut] = usePersisted("charts.analysis.shut", false);

  const analysis = useResize({
    key: "charts.analysis",
    initial: 232,
    min: 120,
    max: 460,
    direction: "n",
  });

  // The rail retargets the focused pane only, so clicking through a watchlist
  // replaces one chart and leaves a comparison intact.
  useEffect(() => {
    setPanes((ps) => {
      const next = ps.length ? [...ps] : [BLANK_PANE];
      while (next.length < layout) next.push({ ...(next[next.length - 1] ?? BLANK_PANE) });
      if (next[focus] && next[focus].symbol !== symbol) next[focus] = { ...next[focus], symbol };
      return next;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [symbol, layout]);

  const shown = panes.slice(0, layout);
  const active = shown[Math.min(focus, shown.length - 1)] ?? BLANK_PANE;

  const grid =
    layout === 1 ? "grid-cols-1 grid-rows-1" : layout === 2 ? "grid-cols-2 grid-rows-1" : "grid-cols-2 grid-rows-2";

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="flex h-12 shrink-0 items-center gap-3 border-b border-border-subtle px-4">
        <Segmented
          label="Layout"
          size="sm"
          value={layout}
          onChange={setLayout}
          options={[
            { value: 1, label: <Square size={13} />, title: "One chart" },
            { value: 2, label: <Columns2 size={13} />, title: "Two charts side by side" },
            { value: 4, label: <Grid2x2 size={13} />, title: "Four charts" },
          ]}
        />
        {layout > 1 && (
          <span className="text-meta text-text-muted max-md:hidden">
            Picking from the watchlist changes the outlined chart.
          </span>
        )}
        <button type="button" onClick={() => setAnalysisShut((v) => !v)} className="action-ghost ml-auto text-meta">
          {analysisShut ? <Maximize2 size={14} /> : <Minimize2 size={14} />}
          {analysisShut ? "Show patterns" : "Hide patterns"}
        </button>
      </div>

      <div className={`grid min-h-0 flex-1 gap-px bg-border-subtle ${grid}`}>
        {shown.map((p, i) => (
          <ChartPane
            key={i}
            pane={p}
            focused={layout > 1 && i === focus}
            onFocus={() => setFocus(i)}
            onChange={(next) =>
              setPanes((ps) => ps.map((old, j) => (j === i ? { ...old, ...next } : old)))
            }
            onSelect={onSelect}
          />
        ))}
      </div>

      {!analysisShut && (
        <>
          <Divider resize={analysis} orientation="horizontal" />
          <div className="shrink-0" style={{ height: analysis.size }}>
            <AnalysisPanel pane={active} />
          </div>
        </>
      )}
    </div>
  );
}

/* ------------------------------------------------------------------ pane */

function ChartPane({
  pane,
  focused,
  onFocus,
  onChange,
  onSelect,
}: {
  pane: Pane;
  focused: boolean;
  onFocus: () => void;
  onChange: (p: Partial<Pane>) => void;
  onSelect: (s: string) => void;
}) {
  const [sheet, setSheet] = useState<null | "studies" | "draw" | "symbol">(null);
  const host = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);

  // Measured rather than inferred from the layout count. One chart with both
  // rails open is narrower than four charts with the rails shut, so a toolbar
  // that decides what to show from the number of panes gets it backwards
  // exactly when it matters.
  useEffect(() => {
    const el = host.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([e]) => e && setWidth(e.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const compact = width > 0 && width < 760;
  const [tool, setTool] = useState<string | null>(null);
  const [clearToken, setClearToken] = useState(0);

  const limit = barsFor(pane.range, pane.interval);

  const { data, isLoading, isFetching, dataUpdatedAt, refetch } = useQuery({
    queryKey: ["candles", pane.symbol, pane.interval, limit],
    queryFn: () => api.candles(pane.symbol, pane.interval, limit),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  });

  const candles = data?.candles ?? [];
  const lastBar = candles.length ? candles[candles.length - 1] ?? null : null;

  // The authoritative price, from the same endpoint the watchlist, the
  // scanner and the dashboard all read.
  //
  // An intraday chart's last candle is not always the session close: the free
  // feed can stop short of the final bars, so the chart and the rest of the
  // app would otherwise show two different closing prices without saying why.
  const { data: quote } = useQuery({
    queryKey: ["quote", pane.symbol],
    queryFn: () => api.quote(pane.symbol),
    refetchInterval: 60_000,
  });
  const truth = quote?.quote?.price ?? null;
  const intraday = pane.interval !== "1d" && pane.interval !== "1wk";
  // A tenth of a percent is comfortably inside rounding and tick size; beyond
  // it the two numbers are genuinely different and saying so is the point.
  const drifted =
    intraday && truth != null && lastBar != null && Math.abs(lastBar.c - truth) / truth > 0.001;

  const on = new Set(pane.studies.map(skey));

  return (
    <div
      ref={host}
      onMouseDown={onFocus}
      className={
        "flex min-h-0 min-w-0 flex-col bg-bg-panel " +
        (focused ? "outline outline-1 -outline-offset-1 outline-brand" : "")
      }
    >
      {/* One toolbar row, and every control on it is an icon or a short group.
          Six spelled-out tool names used to sit here and were clipped mid-word
          the moment a pane got narrower than the full window. */}
      <div className="flex h-12 shrink-0 items-center gap-2.5 border-b border-border-subtle px-3">
        <button
          type="button"
          onClick={() => setSheet(sheet === "symbol" ? null : "symbol")}
          title="Change symbol"
          className="flex h-8 shrink-0 items-center gap-2 rounded-md px-2 transition-colors hover:bg-bg-panel-hover"
        >
          <span className="text-emphasis font-semibold text-text-primary">{tickerOf(pane.symbol)}</span>
          <ChevronDown size={14} className="text-text-muted" />
        </button>

        <Group>
          {RANGES.map((r) => (
            <Seg
              key={r.id}
              on={pane.range === r.id}
              onClick={() => onChange({ range: r.id, interval: intervalFor(r.id, pane.interval) })}
              hide={compact && !["1M", "6M", "1Y"].includes(r.id) && pane.range !== r.id}
            >
              {r.label}
            </Seg>
          ))}
        </Group>

        <Group>
          {INTERVALS.map((iv) => (
            <Seg
              key={iv}
              on={pane.interval === iv}
              onClick={() => onChange({ interval: iv })}
              hide={compact && !["1d", "1h"].includes(iv) && pane.interval !== iv}
            >
              {iv}
            </Seg>
          ))}
        </Group>

        {truth != null && (
          <span className="flex shrink-0 items-baseline gap-1.5">
            <span className="text-emphasis font-semibold text-text-primary">{truth.toFixed(2)}</span>
            {quote?.quote && (
              <span
                className={
                  "text-meta font-medium " +
                  (quote.quote.change_percent >= 0 ? "text-semantic-up" : "text-semantic-down")
                }
              >
                {quote.quote.change_percent >= 0 ? "+" : ""}
                {quote.quote.change_percent.toFixed(2)}%
              </span>
            )}
          </span>
        )}

        {drifted && lastBar && (
          <span
            className="shrink-0 font-mono text-micro text-brand"
            title={`The last bar drawn closes at ${lastBar.c.toFixed(2)}, but the session closed at ${truth?.toFixed(2)}. The price feed did not deliver the session's final bars. Every other price in the app uses the official close.`}
          >
            chart ends {lastBar.c.toFixed(2)}
          </span>
        )}

        <div className="ml-auto flex shrink-0 items-center gap-2.5">
          <BarAge
            bar={lastBar}
            interval={pane.interval}
            fetchedAt={dataUpdatedAt}
          />
          <IconBtn title="Fetch the latest bars" on={isFetching} onClick={() => refetch()} icon={RefreshCw} />
          <IconBtn
            title="Chart type"
            on={sheet === "studies"}
            onClick={() => setSheet(sheet === "studies" ? null : "studies")}
            icon={Activity}
            badge={pane.studies.length}
          />
          <IconBtn
            title="Drawing tools"
            on={sheet === "draw" || tool != null}
            onClick={() => setSheet(sheet === "draw" ? null : "draw")}
            icon={PencilLine}
          />
        </div>
      </div>

      {sheet === "symbol" && (
        <SymbolPicker
          onPick={(s) => {
            onChange({ symbol: s });
            onSelect(s);
            setSheet(null);
          }}
          onClose={() => setSheet(null)}
        />
      )}

      {sheet === "draw" && (
        <Sheet>
          {TOOLS.map((t) => (
            <Chip key={t.id} on={tool === t.id} onClick={() => setTool(tool === t.id ? null : t.id)}>
              {t.label}
            </Chip>
          ))}
          <button
            type="button"
            onClick={() => setClearToken((n) => n + 1)}
            className="action-ghost ml-auto text-meta"
          >
            <Trash2 size={13} /> Clear drawings
          </button>
        </Sheet>
      )}

      {sheet === "studies" && (
        <Sheet>
          <span className="pr-1 text-meta font-medium text-text-muted">Chart</span>
          {CHART_KINDS.map((k) => (
            <Chip key={k.id} on={pane.kind === k.id} onClick={() => onChange({ kind: k.id })}>
              {k.label}
            </Chip>
          ))}
          <span className="pl-3 pr-1 text-meta font-medium text-text-muted">Studies</span>
          {CATALOGUE.map((c) => {
            const active = on.has(skey(c));
            return (
              <Chip
                key={c.label}
                on={active}
                onClick={() =>
                  onChange({
                    studies: active
                      ? pane.studies.filter((s) => skey(s) !== skey(c))
                      : [...pane.studies, { name: c.name, params: c.params, pane: c.pane }],
                  })
                }
              >
                {c.label}
                {active && <X size={11} className="ml-1 inline" />}
              </Chip>
            );
          })}
        </Sheet>
      )}

      <div className="min-h-0 flex-1">
        {isLoading && !data ? (
          <div className="skeleton m-4 h-[calc(100%-2rem)]" aria-label="Loading price history" />
        ) : (
          <KLineChart
            candles={candles}
            symbol={pane.symbol}
            interval={pane.interval}
            studies={pane.studies}
            kind={pane.kind}
            tool={tool}
            onToolUsed={() => setTool(null)}
            clearToken={clearToken}
          />
        )}
      </div>
    </div>
  );
}

/* -------------------------------------------------------------- analysis */

function AnalysisPanel({ pane }: { pane: Pane }) {
  const [tab, setTab] = useState<"patterns" | "structure">("patterns");
  const navigate = useNavigate();

  const limit = Math.min(
    2000,
    Math.max(30, Math.ceil((RANGES.find((r) => r.id === pane.range)?.days ?? 22) * PER_SESSION[pane.interval])),
  );
  const { data } = useQuery({
    queryKey: ["candles", pane.symbol, pane.interval, limit],
    queryFn: () => api.candles(pane.symbol, pane.interval, limit),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  });

  const candles: Candle[] = useMemo(() => data?.candles ?? [], [data]);
  const patterns = useMemo(() => detectPatterns(candles), [candles]);
  const structure = useMemo(() => detectStructure(candles), [candles]);

  const toStrategy = () => {
    const conditions = studiesToConditions(pane.studies);
    const draft: Algorithm = {
      name: "",
      symbols: [pane.symbol],
      interval: pane.interval,
      all: conditions.length ? conditions : [{ indicator: "rsi", period: 14, op: "<", value: 35 }],
      cooldown_hours: 12,
      notify: { telegram: true, ai_context: true },
      enabled: false,
    };
    sessionStorage.setItem("algorithm.draft", JSON.stringify(draft));
    navigate("/algorithms");
  };

  return (
    <div className="flex h-full min-h-0 flex-col border-t border-border-subtle bg-bg-panel">
      <Tabs
        tabs={[
          { value: "patterns", label: "Candlestick patterns", badge: patterns.length },
          { value: "structure", label: "Structure" },
        ]}
        value={tab}
        onChange={setTab}
        action={
          <button
            type="button"
            onClick={toStrategy}
            title="Open the algorithm builder with these studies as conditions"
            className="flex items-center gap-1 font-mono text-meta text-text-muted transition-colors hover:text-brand"
          >
            <Plus size={11} /> strategy from this chart
          </button>
        }
      />

      <div className="min-h-0 flex-1 overflow-y-auto">
        {tab === "patterns" ? (
          patterns.length === 0 ? (
            <Blank
              icon={Shapes}
              title="No named pattern in this window."
              hint="Widen the range, or drop to a shorter interval — patterns are shapes on individual bars, so a 30-day daily chart offers only twenty-two of them."
            />
          ) : (
            <table className="w-full border-collapse">
              <thead className="sticky top-0 z-10 bg-bg-panel">
                <tr className="border-b border-border-subtle text-left">
                  <Th className="w-[150px]">when</Th>
                  <Th className="w-[180px]">pattern</Th>
                  <Th className="w-[70px]">reads</Th>
                  <Th>shape</Th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border-subtle">
                {patterns.slice(0, 60).map((p, i) => (
                  <tr key={`${p.index}-${i}`} className="hover:bg-bg-panel-hover">
                    <td className="px-3 py-2 font-mono text-meta text-text-muted">
                      {formatAgo(p.time)}
                    </td>
                    <td className="px-3 py-2 text-ui text-text-primary">{p.name}</td>
                    <td className="px-3 py-2">
                      <Dir d={p.direction} />
                    </td>
                    <td className="px-3 py-2 text-meta leading-relaxed text-text-secondary">
                      {p.note}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )
        ) : (
          <div className="divide-y divide-border-subtle">
            {structure.length === 0 ? (
              <Blank
                icon={Activity}
                title="Not enough bars to describe a structure."
                hint="Twenty bars minimum. Widen the range."
              />
            ) : (
              structure.map((s) => (
                <div key={s.name} className="flex items-baseline gap-3 px-4 py-3">
                  <span className="w-[220px] shrink-0 text-ui text-text-primary">{s.name}</span>
                  <Dir d={s.direction} />
                  <span className="flex-1 text-meta leading-relaxed text-text-secondary">
                    {s.detail}
                  </span>
                </div>
              ))
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/* ----------------------------------------------------------------- parts */

function SymbolPicker({
  onPick,
  onClose,
}: {
  onPick: (s: string) => void;
  onClose: () => void;
}) {
  const [q, setQ] = useState("");
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const away = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) onClose();
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [onClose]);

  const { data } = useQuery({
    queryKey: ["symbol-search", q],
    queryFn: () => api.searchSymbols(q),
    enabled: q.trim().length >= 2,
  });

  return (
    <div ref={box} className="border-b border-border-subtle bg-bg-base">
      <input
        autoFocus
        value={q}
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && onClose()}
        placeholder="Search a company or paste a ticker…"
        className="w-full bg-transparent px-4 py-2.5 font-mono text-ui outline-none placeholder:text-text-muted"
      />
      {(data?.results ?? []).length > 0 && (
        <div className="max-h-48 overflow-y-auto border-t border-border-subtle">
          {(data?.results ?? []).slice(0, 12).map((r) => (
            <button
              key={r.symbol}
              type="button"
              onClick={() => onPick(r.symbol)}
              className="flex w-full items-baseline gap-3 px-4 py-2 text-left transition-colors hover:bg-bg-panel-hover"
            >
              <span className="w-28 shrink-0 font-mono text-ui text-text-primary">{r.ticker}</span>
              <span className="min-w-0 flex-1 truncate text-meta text-text-secondary">{r.name}</span>
              <span className="shrink-0 font-mono text-micro text-text-muted">{r.exchange}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function Group({ children }: { children: React.ReactNode }) {
  return <div className="flex shrink-0 rounded-md bg-bg-base p-0.5 ring-1 ring-border-subtle">{children}</div>;
}

function Seg({
  on,
  onClick,
  children,
  hide,
}: {
  on: boolean;
  onClick: () => void;
  children: React.ReactNode;
  hide?: boolean;
}) {
  if (hide) return null;
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "h-6 rounded-[5px] px-2 text-meta font-medium transition-colors " +
        (on ? "bg-bg-panel text-text-primary shadow-sm ring-1 ring-border-subtle" : "text-text-muted hover:text-text-primary")
      }
    >
      {children}
    </button>
  );
}

function IconBtn({
  title,
  on,
  onClick,
  icon: Icon,
  badge,
}: {
  title: string;
  on: boolean;
  onClick: () => void;
  icon: typeof Activity;
  badge?: number;
}) {
  return (
    <button
      type="button"
      title={title}
      aria-label={title}
      aria-pressed={on}
      onClick={onClick}
      className={
        "flex h-8 min-w-8 items-center justify-center gap-1 rounded-md px-1.5 text-meta font-medium transition-colors " +
        (on ? "bg-brand-muted text-brand" : "text-text-muted hover:bg-bg-panel-hover hover:text-text-primary")
      }
    >
      <Icon size={15} />
      {badge != null && badge > 0 && <span>{badge}</span>}
    </button>
  );
}

function Sheet({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5 border-b border-border-subtle bg-bg-base px-4 py-2.5 [animation:fade-in_140ms_ease]">
      {children}
    </div>
  );
}

function Chip({
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
        "inline-flex h-7 items-center rounded-md px-2.5 text-meta font-medium transition-colors " +
        (on
          ? "bg-brand-muted text-brand ring-1 ring-brand/40"
          : "bg-bg-panel text-text-secondary ring-1 ring-border-subtle hover:text-text-primary")
      }
    >
      {children}
    </button>
  );
}

function Dir({ d }: { d: Direction }) {
  const tone =
    d === "bullish"
      ? "text-semantic-up"
      : d === "bearish"
        ? "text-semantic-down"
        : "text-text-muted";
  return <span className={"shrink-0 font-mono text-meta " + tone}>{d}</span>;
}

function Th({ children, className = "" }: { children: string; className?: string }) {
  return (
    <th
      className={
        "px-4 py-2 text-left text-meta font-medium text-text-muted first-letter:uppercase " +
        className
      }
    >
      {children}
    </th>
  );
}

function Blank({
  icon: Icon,
  title,
  hint,
}: {
  icon: typeof Activity;
  title: string;
  hint: string;
}) {
  return (
    <div className="flex flex-col items-start gap-2 px-6 py-10">
      <Icon size={18} className="text-text-muted" />
      <p className="text-ui text-text-secondary">{title}</p>
      <p className="max-w-md text-meta leading-relaxed text-text-muted">{hint}</p>
    </div>
  );
}
