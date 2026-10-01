import { useMemo, useRef, useState } from "react";
import type { StockAnalysis } from "../../lib/api";
import { money } from "../charts/BarList";
import { formatDate } from "../../lib/format";
import { typeLabel } from "../EventDrawer";
import { safeHref } from "../../lib/url";

function pct(v: number, digits = 1) {
  return `${v > 0 ? "+" : v < 0 ? "−" : ""}${Math.abs(v).toFixed(digits)}%`;
}
function tone(v: number) {
  return v > 0 ? "text-semantic-up" : v < 0 ? "text-semantic-down" : "text-text-secondary";
}
const words = typeLabel;
const day = (d: string) => formatDate(d, { month: "short", day: "numeric", year: "numeric" });

/**
 * The stock against the S&P 500 over the year, both rebased to 100, with its
 * biggest market-adjusted days marked. Two series, so both are named in the
 * legend and directly at the line ends; the crosshair reads out both.
 */
function PriceChart({ a }: { a: StockAnalysis }) {
  const W = 760;
  const H = 260;
  const pad = { t: 14, r: 64, b: 26, l: 44 };
  const pts = a.series;
  const [hover, setHover] = useState<number | null>(null);
  const svg = useRef<SVGSVGElement>(null);
  const { x, y, ticks, lo } = useMemo(() => {
    const vals = pts.flatMap((p) => [p.s, p.b]);
    let lo = Math.min(...vals);
    let hi = Math.max(...vals);
    const padV = (hi - lo) * 0.08 || 5;
    lo -= padV;
    hi += padV;
    const x = (i: number) => pad.l + (i / Math.max(1, pts.length - 1)) * (W - pad.l - pad.r);
    const y = (v: number) => pad.t + ((hi - v) / (hi - lo)) * (H - pad.t - pad.b);
    const step = (hi - lo) / 4;
    const ticks = Array.from({ length: 5 }, (_, i) => lo + step * i);
    return { x, y, ticks, lo };
  }, [pts]);
  if (pts.length < 2) return null;
  const path = (k: "s" | "b") => pts.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(p[k]).toFixed(1)}`).join("");
  const area = `${path("s")}L${x(pts.length - 1)},${y(lo)}L${x(0)},${y(lo)}Z`;
  const moveIdx = new Map(a.big_moves.map((m) => [m.date, m]));
  // One label per month, skipping any that would sit on top of the last:
  // a window that opens on Sep 30 starts a new month the next session.
  const months: number[] = [];
  pts.forEach((p, i) => {
    if (i === 0 || p.d.slice(5, 7) !== pts[i - 1]!.d.slice(5, 7)) {
      if (months.length && x(i) - x(months[months.length - 1]!) < 32) months.pop();
      months.push(i);
    }
  });
  const last = pts[pts.length - 1]!;
  const onMove = (e: React.MouseEvent) => {
    const r = svg.current?.getBoundingClientRect();
    if (!r) return;
    const px = ((e.clientX - r.left) / r.width) * W;
    const i = Math.round(((px - pad.l) / (W - pad.l - pad.r)) * (pts.length - 1));
    setHover(Math.max(0, Math.min(pts.length - 1, i)));
  };
  const h = hover != null ? pts[hover]! : null;
  const hm = h ? moveIdx.get(h.d) : undefined;
  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-meta text-text-secondary">
        <span className="inline-flex items-center gap-1.5">
          <span className="h-0.5 w-4 rounded bg-brand" /> {a.symbol}
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-0.5 w-4 rounded bg-text-muted" /> S&P 500
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-2 w-2 rounded-full border-2 border-bg-card bg-semantic-down ring-1 ring-semantic-down" /> Biggest days
        </span>
        <span className="ml-auto min-h-5 font-num text-[12px]" aria-live="polite">
          {h ? (
            <>
              {day(h.d)}: {a.symbol} ${h.c.toFixed(2)}, <span className={tone(h.s - 100)}>{pct(h.s - 100)}</span> vs S&P{" "}
              <span className={tone(h.b - 100)}>{pct(h.b - 100)}</span> since start
              {hm ? `; that day ${pct(hm.abnormal_pct)} vs market` : ""}
            </>
          ) : (
            "Hover the chart to read any day"
          )}
        </span>
      </div>
      <svg
        ref={svg}
        viewBox={`0 0 ${W} ${H}`}
        className="w-full touch-none"
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
        role="img"
        aria-label={`${a.symbol} against the S&P 500 over the past year, rebased to 100`}
      >
        <defs>
          <linearGradient id={`g-${a.symbol}`} x1="0" x2="0" y1="0" y2="1">
            <stop offset="0" stopColor="var(--brass)" stopOpacity="0.22" />
            <stop offset="1" stopColor="var(--brass)" stopOpacity="0" />
          </linearGradient>
        </defs>
        {ticks.map((t) => (
          <g key={t}>
            <line x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} className="stroke-border-subtle" strokeWidth={1} />
            <text x={pad.l - 8} y={y(t)} dy="0.32em" textAnchor="end" className="fill-text-muted text-[10.5px]">
              {Math.round(t)}
            </text>
          </g>
        ))}
        {months.map((i) => (
          <text key={i} x={x(i)} y={H - 8} textAnchor="middle" className="fill-text-muted text-[10.5px]">
            {formatDate(pts[i]!.d, { month: "short" })}
          </text>
        ))}
        <line x1={pad.l} x2={W - pad.r} y1={y(100)} y2={y(100)} className="stroke-border-focus" strokeDasharray="3 4" strokeWidth={1} />
        <path d={area} fill={`url(#g-${a.symbol})`} />
        <path d={path("b")} fill="none" className="stroke-text-muted" strokeWidth={1.5} />
        <path d={path("s")} fill="none" className="stroke-brand" strokeWidth={2} />
        {pts.map((p, i) => {
          const m = moveIdx.get(p.d);
          if (!m) return null;
          return (
            <circle
              key={p.d}
              cx={x(i)}
              cy={y(p.s)}
              r={4.5}
              className={(m.abnormal_pct >= 0 ? "fill-semantic-up" : "fill-semantic-down") + " stroke-[var(--card)]"}
              strokeWidth={2}
            />
          );
        })}
        <text x={W - pad.r + 6} y={y(last.s)} dy="0.32em" className="fill-text-primary text-[11px] font-semibold">
          {a.symbol} {pct(last.s - 100, 0)}
        </text>
        <text x={W - pad.r + 6} y={y(last.b)} dy="0.32em" className="fill-text-muted text-[11px]">
          S&P {pct(last.b - 100, 0)}
        </text>
        {h && hover != null && (
          <g>
            <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={H - pad.b} className="stroke-text-muted" strokeWidth={1} />
            <circle cx={x(hover)} cy={y(h.s)} r={3.5} className="fill-brand" />
            <circle cx={x(hover)} cy={y(h.b)} r={3} className="fill-text-muted" />
          </g>
        )}
        <rect x={pad.l} y={pad.t} width={W - pad.l - pad.r} height={H - pad.t - pad.b} fill="transparent" />
      </svg>
    </div>
  );
}

export function AnalysisPanel({ a, onSymbol }: { a: StockAnalysis; onSymbol?: (s: string) => void }) {
  const y1 = a.excess.find((e) => e.horizon === "1y") ?? a.excess[a.excess.length - 1];
  const onRecord = (m: StockAnalysis["big_moves"][number]) => !!(m.earnings || m.insider || m.events?.length || m.web?.length);
  const explained = a.big_moves.filter(onRecord).length;
  const uncovered = a.big_moves.filter((m) => !onRecord(m) && m.news_covered === false).length;
  return (
    <section className="mt-5 overflow-hidden rounded-xl border border-border-subtle bg-bg-card">
      <header className="flex flex-wrap items-baseline justify-between gap-2 px-5 pt-4">
        <h4 className="text-[16px] font-semibold text-text-primary">
          <button type="button" onClick={() => onSymbol?.(a.symbol)} className="hover:text-accent-text">
            {a.symbol}
          </button>{" "}
          <span className="font-normal text-text-muted">against the market</span>
        </h4>
        <span className="text-micro text-text-muted">
          Measured from {a.bars} sessions of prices to {day(a.as_of)}. Not a source claim.
        </span>
      </header>

      <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-3 border-y border-border-subtle px-5 py-3 sm:grid-cols-4">
        {y1 && (
          <div>
            <dt className="text-meta text-text-muted">Return vs S&P 500</dt>
            <dd className={"font-num text-[18px] font-semibold " + tone(y1.percent)}>{pct(y1.percent)}</dd>
            <dd className="text-micro text-text-muted">
              {pct(y1.from)} against {pct(y1.from - y1.percent)}
            </dd>
          </div>
        )}
        <div>
          <dt className="text-meta text-text-muted">Beta</dt>
          <dd className="font-num text-[18px] font-semibold text-text-primary">{a.beta.toFixed(2)}</dd>
          <dd className="text-micro text-text-muted">{a.beta > 1.1 ? "Swings more than the market" : a.beta < 0.9 ? "Swings less than the market" : "Moves with the market"}</dd>
        </div>
        <div>
          <dt className="text-meta text-text-muted">Correlation</dt>
          <dd className="font-num text-[18px] font-semibold text-text-primary">{a.correlation.toFixed(2)}</dd>
          <dd className="text-micro text-text-muted">Market explains {Math.round(a.correlation * a.correlation * 100)}% of its moves</dd>
        </div>
        <div>
          <dt className="text-meta text-text-muted">Big days with a cause on record</dt>
          <dd className="font-num text-[18px] font-semibold text-text-primary">
            {explained} of {a.big_moves.length}
          </dd>
          <dd className="text-micro text-text-muted">
            {uncovered > 0
              ? `${uncovered} before the news archive${a.news_since ? ` (from ${day(a.news_since)})` : ""}`
              : `${a.up_days} up days, ${a.down_days} down`}
          </dd>
        </div>
      </dl>

      <div className="px-5 pt-4">
        <PriceChart a={a} />
      </div>

      {a.notes.length > 0 && (
        <ul className="mx-5 mt-3 flex flex-col gap-1.5 rounded-lg bg-bg-field px-4 py-3">
          {a.notes.map((n, i) => (
            <li key={i} className="text-ui leading-relaxed text-text-secondary">
              {n}
            </li>
          ))}
        </ul>
      )}

      <div className="mt-4 px-5">
        <h5 className="text-meta font-semibold text-text-secondary">Biggest days, and what is on record about them</h5>
        <p className="text-micro text-text-muted">
          Earnings from the company's results history, insider trades from SEC Form 4 filings, news from Bellwether's archive
          {a.news_since ? `, which covers ${a.symbol} from ${day(a.news_since)}` : ""}. Days older than the archive are searched in
          that day's published news.
        </p>
      </div>
      <div className="mt-2 overflow-x-auto">
        <table className="w-full min-w-[640px] text-ui">
          <thead>
            <tr className="border-y border-border-subtle text-left text-meta text-text-muted">
              <th className="px-5 py-2 font-medium">Session</th>
              <th className="px-3 py-2 text-right font-medium">vs market</th>
              <th className="px-3 py-2 text-right font-medium">Volume</th>
              <th className="px-5 py-2 font-medium">On record</th>
            </tr>
          </thead>
          <tbody>
            {a.big_moves.map((m) => (
              <tr key={m.date} className="border-b border-border-subtle last:border-0 align-top">
                <td className="whitespace-nowrap px-5 py-2.5 font-num text-[12px] text-text-muted">{day(m.date)}</td>
                <td className={"px-3 py-2.5 text-right font-num text-[12.5px] font-semibold " + tone(m.abnormal_pct)}>{pct(m.abnormal_pct)}</td>
                <td className="px-3 py-2.5 text-right font-num text-[12px] text-text-secondary">{m.volume_ratio ? `${m.volume_ratio.toFixed(1)}×` : "—"}</td>
                <td className="px-5 py-2.5">
                  {m.earnings && <span className="block text-meta font-medium leading-snug text-text-primary">{m.earnings}</span>}
                  {m.insider && <span className="block text-meta leading-snug text-text-primary">{m.insider}</span>}
                  {m.events?.map((ev) => (
                    <span key={ev.id} className="block text-meta leading-snug text-text-secondary">
                      {ev.headline} <span className="text-text-muted">({words(ev.type)})</span>
                    </span>
                  ))}
                  {m.web?.map((w) => (
                    <a
                      key={w.url}
                      href={safeHref(w.url)}
                      target="_blank"
                      rel="noreferrer"
                      className="block text-meta leading-snug text-text-secondary hover:text-accent-text"
                    >
                      {w.title} <span className="text-text-muted">({w.publisher}, that day)</span>
                    </a>
                  ))}
                  {!onRecord(m) && (
                    <span className="text-meta text-text-muted">
                      {m.news_covered === false
                        ? `Before the news archive covers ${a.symbol}, and a search of that day's news found nothing`
                        : "No earnings, insider filing or news on record for this day"}
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {!!a.reactions?.length && (
        <>
          <div className="mt-4 px-5">
            <h5 className="text-meta font-semibold text-text-secondary">How it has reacted, by kind of event</h5>
            <p className="text-micro text-text-muted">
              Against the market, from the first session to trade after the event became public. Each session counts once. Rows
              under three sessions mean little.
            </p>
          </div>
          <div className="mt-2 overflow-x-auto">
            <table className="w-full min-w-[640px] text-ui">
              <thead>
                <tr className="border-y border-border-subtle text-left text-meta text-text-muted">
                  <th className="px-5 py-2 font-medium">Event</th>
                  <th className="px-3 py-2 text-right font-medium">Sessions</th>
                  <th className="px-3 py-2 text-right font-medium">First session</th>
                  <th className="px-3 py-2 text-right font-medium">Typical move</th>
                  <th className="px-3 py-2 text-right font-medium">Five sessions</th>
                  <th className="px-5 py-2 text-right font-medium">Beat market</th>
                </tr>
              </thead>
              <tbody>
                {a.reactions.map((r) => (
                  <tr key={r.type} className={"border-b border-border-subtle last:border-0 " + (r.count < 3 ? "opacity-60" : "")}>
                    <td className="px-5 py-2.5">
                      <span className="text-text-primary">{r.label ?? words(r.type)}</span>
                      {r.since && (
                        <span className="block text-micro text-text-muted">
                          {r.source === "earnings" ? "Results history" : r.source === "insiders" ? "SEC Form 4" : "News archive"} since {day(r.since)}
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2.5 text-right font-num text-[12px] text-text-secondary">{r.count}</td>
                    <td className={"px-3 py-2.5 text-right font-num text-[12.5px] " + tone(r.day1_mean_pct)}>{pct(r.day1_mean_pct, 2)}</td>
                    <td className="px-3 py-2.5 text-right font-num text-[12px] text-text-secondary">
                      {r.abs_mean_pct != null ? `±${r.abs_mean_pct.toFixed(2)}%` : "—"}
                    </td>
                    <td className={"px-3 py-2.5 text-right font-num text-[12.5px] " + tone(r.day5_mean_pct)}>{pct(r.day5_mean_pct, 2)}</td>
                    <td className="px-5 py-2.5 text-right font-num text-[12px] text-text-secondary">{r.hit_rate.toFixed(0)}%</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      {(a.smart_money || a.catalyst) && (
        <div className="grid gap-px border-t border-border-subtle bg-border-subtle sm:grid-cols-2">
          {a.smart_money && (
            <div className="bg-bg-card px-5 py-4">
              <h5 className="text-meta font-semibold text-text-secondary">Insiders and funds, six months</h5>
              <p className="mt-1 text-ui text-text-primary">
                Bought <span className="font-num text-semantic-up">{money(a.smart_money.insider_buy_value)}</span>, sold{" "}
                <span className="font-num text-semantic-down">{money(a.smart_money.insider_sell_value)}</span> on the open market.
              </p>
              {!!a.smart_money.insider_buyers?.length && (
                <p className="mt-1 text-meta text-text-secondary">Buyers: {a.smart_money.insider_buyers.join(", ")}</p>
              )}
              {(a.smart_money.fund_moves ?? []).slice(0, 4).map((f) => (
                <p key={f} className="mt-1 text-meta text-text-secondary">
                  {f}
                </p>
              ))}
              {a.smart_money.congress_filings > 0 && (
                <p className="mt-1 text-meta text-text-secondary">{a.smart_money.congress_filings} Congressional disclosures mention it.</p>
              )}
            </div>
          )}
          {a.catalyst && (
            <div className="bg-bg-card px-5 py-4">
              <h5 className="text-meta font-semibold text-text-secondary">Next on the calendar</h5>
              <p className="mt-1 text-ui text-text-primary">
                {words(a.catalyst.kind)} on {day(a.catalyst.date)}, in {a.catalyst.in_days} days.
              </p>
              {a.catalyst.eps_mean != null && <p className="mt-1 text-meta text-text-secondary">Analysts expect EPS of {a.catalyst.eps_mean.toFixed(2)}.</p>}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
