import { useMutation, useQuery } from "@tanstack/react-query";
import { AlertTriangle, Play } from "lucide-react";
import { useState } from "react";
import {
  api,
  type Algorithm,
  type BacktestConfig,
  type BacktestResult,
} from "../../lib/api";
import { Empty } from "../ui/Empty";

/** Windows offered, in bars of whatever interval the rule runs on. */
const WINDOWS = [
  { bars: 250, label: "1Y" },
  { bars: 500, label: "2Y" },
  { bars: 1000, label: "4Y" },
  { bars: 2000, label: "MAX" },
] as const;

const DEFAULT_CONFIG: BacktestConfig = {
  direction: "long",
  stop_loss_pct: 5,
  take_profit_pct: 10,
  max_hold_bars: 20,
  cost_bps: 10,
  // Full size by default, because it is the assumption every other
  // backtester makes and changing it silently would make results
  // incomparable with anything the operator has seen elsewhere. The sizing
  // control sits next to it so the choice is at least visible.
  size_mode: "full",
  size_pct: 25,
  risk_pct: 1,
};

/**
 * What a rule would have done.
 *
 * The design problem here is not showing the numbers, it is stopping them from
 * being believed too easily. So buy-and-hold sits beside every return rather
 * than in a footnote, the warnings the engine produces are given the top of
 * the panel rather than the bottom, and a multi-symbol run leads with how many
 * instruments the rule actually beat holding on — a figure that does not
 * flatter the way an average does.
 */
export function BacktestPanel({ draft }: { draft: Algorithm }) {
  const [bars, setBars] = useState<number>(500);
  const [cfg, setCfg] = useState<BacktestConfig>(DEFAULT_CONFIG);
  const [scope, setScope] = useState<number[]>([]);
  const [exitOn, setExitOn] = useState(false);
  const [exit, setExit] = useState({ indicator: "rsi", period: 14, op: ">", value: 60 });
  const [selected, setSelected] = useState<string | null>(null);

  const lists = useQuery({ queryKey: ["watchlists"], queryFn: api.watchlists });
  const vocab = useQuery({ queryKey: ["vocab"], queryFn: api.vocabulary });

  const run = useMutation({
    mutationFn: () =>
      api.backtest({
        algorithm: draft,
        ...(scope.length ? { watchlist_ids: scope } : {}),
        bars,
        config: {
          ...cfg,
          // The server fills in the interval and instruments; this is a
          // condition on the same rule, not a schedule of its own.
          exit_rule: exitOn
            ? {
                name: "exit",
                symbols: [],
                interval: draft.interval,
                all: [{ indicator: exit.indicator, period: exit.period, op: exit.op as never, value: exit.value }],
                cooldown_hours: 1,
                notify: { telegram: false, ai_context: false },
                enabled: false,
              }
            : null,
        },
      }),
    onSuccess: (d) => setSelected(d.results.at(-1)?.symbol ?? null),
  });

  const data = run.data;
  const results = data?.results ?? [];
  const shown = results.find((r) => r.symbol === selected) ?? results.at(-1) ?? null;
  const multi = results.length > 1;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* Controls. */}
      <div className="shrink-0 border-b border-border-subtle px-4 py-3">
        <div className="flex flex-wrap items-center gap-x-5 gap-y-2.5">
          <Field label="window">
            <Segments
              options={WINDOWS.map((w) => [w.bars, w.label] as const)}
              value={bars}
              onChange={setBars}
            />
          </Field>

          <Num
            label="stop loss"
            unit="%"
            value={cfg.stop_loss_pct}
            onChange={(v) => setCfg({ ...cfg, stop_loss_pct: v })}
          />
          <Num
            label="take profit"
            unit="%"
            value={cfg.take_profit_pct}
            onChange={(v) => setCfg({ ...cfg, take_profit_pct: v })}
          />
          <Num
            label="max hold"
            unit="bars"
            value={cfg.max_hold_bars}
            onChange={(v) => setCfg({ ...cfg, max_hold_bars: v })}
          />
          <Num
            label="cost"
            unit="bps/side"
            value={cfg.cost_bps}
            onChange={(v) => setCfg({ ...cfg, cost_bps: v })}
            title="Brokerage, STT, exchange fees and slippage together, charged on entry and again on exit."
          />

          <Field label="side">
            <Segments
              options={[["long", "long"], ["short", "short"]] as const}
              value={cfg.direction}
              onChange={(v) => setCfg({ ...cfg, direction: v })}
            />
          </Field>

          <Field label="size">
            <Segments
              options={[["full", "full"], ["fraction", "fixed %"], ["risk", "by risk"]] as const}
              value={cfg.size_mode}
              onChange={(v) => setCfg({ ...cfg, size_mode: v })}
            />
          </Field>
          {cfg.size_mode === "fraction" && (
            <Num
              label="of account"
              unit="%"
              value={cfg.size_pct}
              onChange={(v) => setCfg({ ...cfg, size_pct: v })}
            />
          )}
          {cfg.size_mode === "risk" && (
            <Num
              label="risk per trade"
              unit="%"
              value={cfg.risk_pct}
              onChange={(v) => setCfg({ ...cfg, risk_pct: v })}
              title="Sized so that being stopped out costs exactly this much of the account. Needs a stop loss."
            />
          )}

          <button
            type="button"
            onClick={() => run.mutate()}
            disabled={run.isPending}
            className="ml-auto flex items-center gap-1.5 border border-border-subtle px-3 py-1 font-mono text-meta uppercase tracking-wider text-text-secondary transition-colors hover:border-brand hover:text-brand disabled:opacity-40"
          >
            <Play size={11} />
            {run.isPending ? "running…" : "run backtest"}
          </button>
        </div>

        <div className="mt-2.5 flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
            exit
          </span>
          <Chip on={!exitOn} onClick={() => setExitOn(false)}>
            stop, target or time only
          </Chip>
          <Chip on={exitOn} onClick={() => setExitOn(true)}>
            also on a condition
          </Chip>
          {exitOn && (
            <>
              <select
                value={exit.indicator}
                onChange={(e) => setExit({ ...exit, indicator: e.target.value })}
                className="border border-border-subtle bg-bg-base px-1.5 py-0.5 font-mono text-meta text-text-primary outline-none focus:border-brand"
              >
                {(vocab.data?.indicators ?? []).map((i) => (
                  <option key={i.name} value={i.name}>
                    {i.name}
                  </option>
                ))}
              </select>
              <input
                type="number"
                value={exit.period}
                onChange={(e) => setExit({ ...exit, period: Number(e.target.value) })}
                title="Period"
                className="w-14 border border-border-subtle bg-bg-base px-1.5 py-0.5 text-right font-mono text-meta outline-none focus:border-brand"
              />
              <select
                value={exit.op}
                onChange={(e) => setExit({ ...exit, op: e.target.value })}
                className="border border-border-subtle bg-bg-base px-1.5 py-0.5 font-mono text-meta text-text-primary outline-none focus:border-brand"
              >
                {["<", "<=", ">", ">=", "crosses_above", "crosses_below"].map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </select>
              <input
                type="number"
                step="any"
                value={exit.value}
                onChange={(e) => setExit({ ...exit, value: Number(e.target.value) })}
                className="w-20 border border-border-subtle bg-bg-base px-1.5 py-0.5 text-right font-mono text-meta outline-none focus:border-brand"
              />
            </>
          )}
        </div>

        {(lists.data?.watchlists ?? []).length > 0 && (
          <div className="mt-2.5 flex flex-wrap items-center gap-x-3 gap-y-1.5">
            <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
              test on
            </span>
            <Chip on={scope.length === 0} onClick={() => setScope([])}>
              the rule's own instruments
            </Chip>
            {(lists.data?.watchlists ?? []).map((l) => {
              const on = scope.includes(l.id);
              return (
                <Chip
                  key={l.id}
                  on={on}
                  onClick={() =>
                    setScope(on ? scope.filter((x) => x !== l.id) : [...scope, l.id])
                  }
                >
                  {l.name} {l.count}
                </Chip>
              );
            })}
          </div>
        )}

        {run.isPending && (
          <p className="mt-2.5 text-meta leading-relaxed text-text-muted">
            Re-evaluating the rule at every bar, which is what makes look-ahead impossible and
            the run slow. Seconds per instrument.
          </p>
        )}
        {run.isError && (
          <p className="mt-2.5 text-meta text-semantic-down">{(run.error as Error).message}</p>
        )}
      </div>

      {!data ? (
        <Empty
          icon={Play}
          title="Nothing tested yet."
          hint="A backtest evaluates this rule bar by bar through history, entering at the next bar's open after each signal and charging costs both ways."
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          {/* Warnings first. Everything below them is easier to believe than it
              should be, and these are the reasons not to. */}
          {(shown?.warnings ?? []).length > 0 && (
            <div className="border-b border-border-subtle bg-bg-base px-4 py-3">
              {(shown?.warnings ?? []).map((w) => (
                <p key={w} className="flex items-start gap-2 py-0.5 text-meta leading-relaxed text-text-secondary">
                  <AlertTriangle size={11} className="mt-0.5 shrink-0 text-brand" />
                  {w}
                </p>
              ))}
            </div>
          )}

          {multi && data.summary && (
            <div className="border-b border-border-subtle px-4 py-3">
              <p className="mb-2 font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
                across {data.summary.symbols} instruments · {data.elapsed}
              </p>
              <div className="flex flex-wrap gap-x-8 gap-y-3">
                {/* The headline for a multi-symbol run. An average return is
                    carried by its best instrument; this cannot be. */}
                <Metric
                  label="beat buy & hold"
                  value={`${data.summary.beat} of ${data.summary.symbols}`}
                  tone={data.summary.beat * 2 >= data.summary.symbols ? "up" : "down"}
                  big
                />
                <Metric label="trades" value={String(data.summary.trades)} />
                <Metric label="win rate" value={`${data.summary.win_rate.toFixed(1)}%`} />
                <Metric
                  label="median return"
                  value={pct(data.summary.median_total_pct)}
                  tone={data.summary.median_total_pct >= 0 ? "up" : "down"}
                />
                <Metric
                  label="median buy & hold"
                  value={pct(data.summary.median_buy_hold_pct)}
                  tone={data.summary.median_buy_hold_pct >= 0 ? "up" : "down"}
                />
                <Metric label="worst drawdown" value={pct(data.summary.worst_drawdown)} tone="down" />
              </div>
            </div>
          )}

          {multi && (
            <div className="border-b border-border-subtle">
              <table className="w-full max-w-[1100px] border-collapse">
                <thead>
                  <tr className="border-b border-border-subtle">
                    <Th className="w-[170px]">instrument</Th>
                    <Th right>return</Th>
                    <Th right>buy & hold</Th>
                    <Th right>trades</Th>
                    <Th right>win rate</Th>
                    <Th right>drawdown</Th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border-subtle">
                  {results.map((r) => (
                    <tr
                      key={r.symbol}
                      onClick={() => setSelected(r.symbol)}
                      className={
                        "cursor-pointer transition-colors " +
                        (r.symbol === shown?.symbol ? "bg-brand-muted" : "hover:bg-bg-panel-hover")
                      }
                    >
                      <td className="px-3 py-2 font-mono text-ui text-text-primary">{r.symbol}</td>
                      <Cell v={r.stats.total_pct} />
                      <Cell v={r.stats.buy_hold_pct} muted />
                      <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
                        {r.stats.trades}
                      </td>
                      <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
                        {r.stats.trades ? `${r.stats.win_rate.toFixed(0)}%` : "—"}
                      </td>
                      <Cell v={r.stats.max_drawdown} />
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {shown && <SymbolReport r={shown} single={!multi} elapsed={data.elapsed} />}

          {(data.skipped ?? []).length > 0 && (
            <div className="border-t border-border-subtle px-4 py-3">
              <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
                not tested
              </p>
              {(data.skipped ?? []).map((s) => (
                <p key={s.symbol} className="mt-1 font-mono text-meta text-text-muted">
                  {s.symbol} — {s.reason}
                </p>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function SymbolReport({
  r,
  single,
  elapsed,
}: {
  r: BacktestResult;
  single: boolean;
  elapsed: string;
}) {
  const s = r.stats;
  // Only worth widening the table when sizing is actually fractional; at full
  // size the two return columns would carry identical numbers.
  const sized = r.trades.some((t) => t.size < 0.999);
  const shorts = r.trades.filter((t) => t.direction === "short").length;
  return (
    <>
      <div className="border-b border-border-subtle px-4 py-3">
        <p className="mb-2.5 font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          {r.symbol} · {r.bars} bars · {r.from.slice(0, 10)} to {r.to.slice(0, 10)}
          {shorts > 0 ? ` · ${shorts} short` : ""}
          {sized ? " · fractional sizing" : ""}
          {single ? ` · ${elapsed}` : ""}
        </p>
        <div className="flex flex-wrap gap-x-8 gap-y-3">
          <Metric label="return" value={pct(s.total_pct)} tone={s.total_pct >= 0 ? "up" : "down"} big />
          {/* Immediately beside the return, never below it: a return figure
              without its benchmark is not an answer to any question. */}
          <Metric
            label="buy & hold"
            value={pct(s.buy_hold_pct)}
            tone={s.buy_hold_pct >= 0 ? "up" : "down"}
            big
          />
          <Metric label="CAGR" value={pct(s.cagr)} tone={s.cagr >= 0 ? "up" : "down"} />
          <Metric label="max drawdown" value={pct(s.max_drawdown)} tone="down" />
          <Metric label="trades" value={String(s.trades)} />
          <Metric label="win rate" value={s.trades ? `${s.win_rate.toFixed(1)}%` : "—"} />
          <Metric
            label="avg win / loss"
            value={s.trades ? `${pct(s.avg_win_pct)} / ${pct(s.avg_loss_pct)}` : "—"}
          />
          <Metric
            label="profit factor"
            value={s.profit_factor > 0 ? s.profit_factor.toFixed(2) : "—"}
          />
          <Metric label="sharpe" value={s.sharpe ? s.sharpe.toFixed(2) : "—"} />
          <Metric label="exposure" value={`${s.exposure_pct.toFixed(0)}%`} />
        </div>
      </div>

      {r.equity.length > 2 && <EquityCurve r={r} />}

      {r.trades.length > 0 && (
        <table className="w-full max-w-[1100px] border-collapse">
          <thead className="sticky top-0 z-10 bg-bg-panel">
            <tr className="border-b border-border-subtle">
              <Th className="w-[70px]">side</Th>
              <Th className="w-[120px]">entered</Th>
              <Th right className="w-[100px]">at</Th>
              <Th className="w-[120px]">exited</Th>
              <Th right className="w-[100px]">at</Th>
              <Th right className="w-[60px]">bars</Th>
              {sized && <Th right className="w-[70px]">size</Th>}
              <Th right className="w-[90px]">position</Th>
              {sized && <Th right className="w-[90px]">account</Th>}
              <Th>why it closed</Th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border-subtle">
            {r.trades.map((t, i) => (
              <tr key={i} className="hover:bg-bg-panel-hover">
                <td
                  className={
                    "px-3 py-2 font-mono text-meta " +
                    (t.direction === "short" ? "text-semantic-down" : "text-text-secondary")
                  }
                >
                  {t.direction}
                </td>
                <td className="px-3 py-2 font-mono text-meta text-text-muted">
                  {t.entry_time.slice(0, 10)}
                </td>
                <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
                  {t.entry_price.toFixed(2)}
                </td>
                <td className="px-3 py-2 font-mono text-meta text-text-muted">
                  {t.exit_time.slice(0, 10)}
                </td>
                <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
                  {t.exit_price.toFixed(2)}
                </td>
                <td className="px-3 py-2 text-right font-mono text-ui text-text-muted">{t.bars}</td>
                {sized && (
                  <td className="px-3 py-2 text-right font-mono text-ui text-text-muted">
                    {(t.size * 100).toFixed(0)}%
                  </td>
                )}
                {/* Return on the position, then what it did to the account.
                    With fractional sizing these diverge sharply, and reading
                    the first as though it were the second is how a backtest
                    gets believed. */}
                <Cell v={t.return_pct} muted={sized} />
                {sized && <Cell v={t.account_pct} />}
                <td className="px-3 py-2 text-meta text-text-muted">
                  {t.reason}
                  {t.ambiguous && (
                    <span
                      className="ml-2 text-brand"
                      title="This bar reached both the stop and the target. OHLC cannot say which came first, so the stop was assumed."
                    >
                      ambiguous
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

/**
 * Strategy against buy-and-hold, on one axis.
 *
 * Two lines rather than one, because the shape of the gap is the argument: a
 * strategy that tracks the benchmark and lands slightly ahead is a different
 * proposition from one that sat in cash through a drawdown and caught the
 * recovery, and a single number cannot tell them apart.
 */
function EquityCurve({ r }: { r: BacktestResult }) {
  const w = 1100;
  const h = 150;
  const pts = r.equity;
  const vals = pts.flatMap((p) => [p.e, p.h]);
  const lo = Math.min(...vals);
  const hi = Math.max(...vals);
  const span = hi - lo || 1;

  const path = (pick: (p: (typeof pts)[number]) => number) =>
    pts
      .map((p, i) => {
        const x = (i / (pts.length - 1)) * w;
        const y = h - ((pick(p) - lo) / span) * h;
        return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
      })
      .join(" ");

  // The starting level, so a reader can see at a glance which side of break
  // even each line spent its time on.
  const baseY = h - ((100 - lo) / span) * h;

  return (
    <div className="border-b border-border-subtle px-4 py-3">
      <div className="mb-2 flex items-center gap-4">
        <span className="flex items-center gap-1.5 font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          <span className="h-px w-4 bg-brand" /> strategy
        </span>
        <span className="flex items-center gap-1.5 font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
          <span className="h-px w-4 bg-text-muted" /> buy &amp; hold
        </span>
      </div>
      <div className="overflow-x-auto">
        <svg viewBox={`0 0 ${w} ${h}`} className="h-[150px] w-full min-w-[560px]" preserveAspectRatio="none">
          <line x1={0} y1={baseY} x2={w} y2={baseY} className="stroke-border-subtle" strokeWidth={1} strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />
          <path d={path((p) => p.h)} fill="none" strokeWidth={1} className="stroke-text-muted" vectorEffect="non-scaling-stroke" />
          <path d={path((p) => p.e)} fill="none" strokeWidth={1.5} className="stroke-brand" vectorEffect="non-scaling-stroke" />
        </svg>
      </div>
    </div>
  );
}

/* ----------------------------------------------------------------- parts */

function pct(v: number) {
  return `${v >= 0 ? "+" : ""}${v.toFixed(2)}%`;
}

function Metric({
  label,
  value,
  tone,
  big,
}: {
  label: string;
  value: string;
  tone?: "up" | "down";
  big?: boolean;
}) {
  return (
    <div>
      <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">{label}</p>
      <p
        className={
          "mt-0.5 font-mono " +
          (big ? "text-display " : "text-emphasis ") +
          (tone === "up" ? "text-semantic-up" : tone === "down" ? "text-semantic-down" : "text-text-primary")
        }
      >
        {value}
      </p>
    </div>
  );
}

function Cell({ v, muted }: { v: number; muted?: boolean }) {
  return (
    <td
      className={
        "px-3 py-2 text-right font-mono text-ui " +
        (muted ? "text-text-muted" : v >= 0 ? "text-semantic-up" : "text-semantic-down")
      }
    >
      {pct(v)}
    </td>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2">
      <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
        {label}
      </span>
      {children}
    </div>
  );
}

function Num({
  label,
  unit,
  value,
  onChange,
  title,
}: {
  label: string;
  unit: string;
  value: number;
  onChange: (v: number) => void;
  title?: string;
}) {
  return (
    <div className="flex items-center gap-2" title={title}>
      <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
        {label}
      </span>
      <input
        type="number"
        min={0}
        step="any"
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="w-16 border border-border-subtle bg-bg-base px-1.5 py-0.5 text-right font-mono text-meta outline-none focus:border-brand"
      />
      <span className="font-mono text-micro text-text-muted">{unit}</span>
    </div>
  );
}

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
            (v === value ? "bg-brand-muted text-brand" : "text-text-muted hover:text-text-primary")
          }
        >
          {label}
        </button>
      ))}
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
        "border px-2 py-0.5 font-mono text-meta transition-colors " +
        (on
          ? "border-brand bg-brand-muted text-brand"
          : "border-border-subtle text-text-muted hover:text-text-primary")
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
