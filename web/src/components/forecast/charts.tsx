import { useMemo, useRef, useState } from "react";
import type { ReliabilityBin } from "../../lib/api";

export const pct = (v: number, d = 1) => `${v > 0 ? "+" : v < 0 ? "−" : ""}${Math.abs(v * 100).toFixed(d)}%`;
export const prob = (v: number) => `${Math.round(v * 100)}%`;

/** Rounded, readable ticks spanning [lo, hi]. */
function ticks(lo: number, hi: number, n = 4) {
  const span = hi - lo || 1;
  const raw = span / n;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? raw;
  const out: number[] = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi + 1e-12; v += step) out.push(Number(v.toFixed(10)));
  return out;
}

/**
 * The simulated range of a stock's cumulative return over the sessions
 * ahead: the 90%, 80% and 50% bands in one hue, darker toward the middle,
 * and the median as a line. A report inside the window is marked, because
 * that is usually where the fan widens.
 */
export function FanChart({ fan, levels, earnings, height = 190 }: { fan: number[][]; levels: number[]; earnings?: number; height?: number }) {
  const W = 340;
  const H = height;
  const pad = { t: 10, r: 10, b: 22, l: 40 };
  const svg = useRef<SVGSVGElement>(null);
  const [hover, setHover] = useState<number | null>(null);
  const idx = (q: number) => Math.max(0, levels.findIndex((l) => Math.abs(l - q) < 1e-6));
  const i05 = idx(0.05);
  const i10 = idx(0.1);
  const i25 = idx(0.25);
  const i50 = idx(0.5);
  const i75 = idx(0.75);
  const i90 = idx(0.9);
  const i95 = idx(0.95);
  // Session 0 is today, at no change.
  const rows = useMemo(() => [levels.map(() => 0), ...fan], [fan, levels]);
  const { x, y, yt } = useMemo(() => {
    const lo = Math.min(0, ...rows.map((r) => r[i05] ?? 0));
    const hi = Math.max(0, ...rows.map((r) => r[i95] ?? 0));
    const padV = (hi - lo) * 0.06;
    const x = (d: number) => pad.l + (d / (rows.length - 1)) * (W - pad.l - pad.r);
    const y = (v: number) => pad.t + ((hi + padV - v) / (hi - lo + 2 * padV)) * (H - pad.t - pad.b);
    return { x, y, yt: ticks(lo, hi, 4) };
  }, [rows, i05, i95, H]);
  const band = (a: number, b: number) =>
    rows.map((r, d) => `${d ? "L" : "M"}${x(d).toFixed(1)},${y(r[a]!).toFixed(1)}`).join("") +
    rows
      .map((_, k) => rows.length - 1 - k)
      .map((d) => `L${x(d).toFixed(1)},${y(rows[d]![b]!).toFixed(1)}`)
      .join("") +
    "Z";
  const median = rows.map((r, d) => `${d ? "L" : "M"}${x(d).toFixed(1)},${y(r[i50]!).toFixed(1)}`).join("");
  const onMove = (e: React.MouseEvent) => {
    const r = svg.current?.getBoundingClientRect();
    if (!r) return;
    const px = ((e.clientX - r.left) / r.width) * W;
    const d = Math.round(((px - pad.l) / (W - pad.l - pad.r)) * (rows.length - 1));
    setHover(d >= 1 && d < rows.length ? d : null);
  };
  const h = hover != null ? rows[hover] : null;
  return (
    <div>
      <p className="min-h-4 font-num text-[11.5px] text-text-secondary" aria-live="polite">
        {h && hover != null ? (
          <>
            Session {hover}: median {pct(h[i50]!)}, 80% between {pct(h[i10]!)} and {pct(h[i90]!)}
          </>
        ) : (
          <span className="text-text-muted">Hover to read any session</span>
        )}
      </p>
      <svg
        ref={svg}
        viewBox={`0 0 ${W} ${H}`}
        className="mt-1 w-full touch-none"
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
        role="img"
        aria-label="Simulated range of the cumulative return over the next sessions"
      >
        {yt.map((t) => (
          <g key={t}>
            <line x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} className="stroke-border-subtle" strokeWidth={1} />
            <text x={pad.l - 6} y={y(t)} dy="0.32em" textAnchor="end" className="fill-text-muted text-[10px]">
              {pct(t, Math.abs(t) < 0.1 ? 0 : 0)}
            </text>
          </g>
        ))}
        <line x1={pad.l} x2={W - pad.r} y1={y(0)} y2={y(0)} className="stroke-border-focus" strokeDasharray="3 3" strokeWidth={1} />
        <path d={band(i05, i95)} fill="var(--brass)" fillOpacity={0.12} />
        <path d={band(i10, i90)} fill="var(--brass)" fillOpacity={0.16} />
        <path d={band(i25, i75)} fill="var(--brass)" fillOpacity={0.24} />
        <path d={median} fill="none" stroke="var(--brass)" strokeWidth={2} strokeLinejoin="round" />
        {earnings != null && earnings >= 1 && earnings < rows.length && (
          <g>
            <line x1={x(earnings)} x2={x(earnings)} y1={pad.t} y2={H - pad.b} className="stroke-text-muted" strokeDasharray="2 3" strokeWidth={1} />
            <text x={x(earnings) + 3} y={pad.t + 9} className="fill-text-secondary text-[10px]">
              Earnings
            </text>
          </g>
        )}
        {[1, 5, 10, 15, 20].filter((d) => d < rows.length).map((d) => (
          <text key={d} x={x(d)} y={H - 6} textAnchor="middle" className="fill-text-muted text-[10px]">
            {d}
          </text>
        ))}
        {h && hover != null && (
          <g>
            <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={H - pad.b} className="stroke-text-muted" strokeWidth={1} />
            <circle cx={x(hover)} cy={y(h[i50]!)} r={3.5} fill="var(--brass)" stroke="var(--card)" strokeWidth={2} />
          </g>
        )}
        <rect x={pad.l} y={pad.t} width={W - pad.l - pad.r} height={H - pad.t - pad.b} fill="transparent" />
      </svg>
      <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-micro text-text-muted">
        <span className="inline-flex items-center gap-1">
          <span className="h-0.5 w-3 rounded bg-brand" /> Median
        </span>
        <span className="inline-flex items-center gap-1">
          <span className="h-2.5 w-3 rounded-sm bg-brand opacity-40" /> Half of outcomes
        </span>
        <span className="inline-flex items-center gap-1">
          <span className="h-2.5 w-3 rounded-sm bg-brand opacity-20" /> 80%, then 90%
        </span>
        <span>Sessions ahead</span>
      </div>
    </div>
  );
}

/**
 * A distribution in a table row: the 80% range as a bar, the middle half
 * darker, the median as a tick, against a shared scale with zero marked.
 */
export function RangeBar({ q, domain }: { q: number[]; domain: number }) {
  const at = (v: number) => `${((Math.max(-domain, Math.min(domain, v)) + domain) / (2 * domain)) * 100}%`;
  const span = (a: number, b: number) => ({ left: at(a), width: `calc(${at(b)} - ${at(a)})` });
  return (
    <div className="relative h-3 w-full min-w-[120px]" title={`80% between ${pct(q[1]!)} and ${pct(q[17]!)}; median ${pct(q[9]!)}`}>
      <span className="absolute inset-y-0 w-px bg-border-focus" style={{ left: "50%" }} />
      <span className="absolute top-1/2 h-1 -translate-y-1/2 rounded-full bg-brand opacity-30" style={span(q[1]!, q[17]!)} />
      <span className="absolute top-1/2 h-1.5 -translate-y-1/2 rounded-full bg-brand opacity-60" style={span(q[4]!, q[14]!)} />
      <span className="absolute inset-y-0 w-0.5 -translate-x-1/2 rounded bg-text-primary" style={{ left: at(q[9]!) }} />
    </div>
  );
}

/**
 * Stated probability against how often it came true, out of sample. On the
 * diagonal is calibrated; each dot's size is how many forecasts it holds.
 */
export function ReliabilityChart({ bins, label }: { bins: ReliabilityBin[]; label: string }) {
  const S = 200;
  const pad = 26;
  const [hover, setHover] = useState<ReliabilityBin | null>(null);
  const lo = 0.3;
  const hi = 0.7;
  const p = (v: number) => pad + ((Math.max(lo, Math.min(hi, v)) - lo) / (hi - lo)) * (S - 2 * pad);
  const maxN = Math.max(1, ...bins.map((b) => b.n));
  return (
    <figure className="flex flex-col">
      <svg viewBox={`0 0 ${S} ${S}`} className="w-full max-w-[240px]" role="img" aria-label={`${label}: forecast probability against observed frequency`}>
        {[0.3, 0.4, 0.5, 0.6, 0.7].map((t) => (
          <g key={t}>
            <line x1={p(t)} x2={p(t)} y1={pad} y2={S - pad} className="stroke-border-subtle" strokeWidth={1} />
            <line x1={pad} x2={S - pad} y1={S - p(t)} y2={S - p(t)} className="stroke-border-subtle" strokeWidth={1} />
            <text x={p(t)} y={S - pad + 12} textAnchor="middle" className="fill-text-muted text-[9px]">
              {Math.round(t * 100)}
            </text>
            <text x={pad - 4} y={S - p(t)} dy="0.32em" textAnchor="end" className="fill-text-muted text-[9px]">
              {Math.round(t * 100)}
            </text>
          </g>
        ))}
        <line x1={p(lo)} y1={S - p(lo)} x2={p(hi)} y2={S - p(hi)} className="stroke-border-focus" strokeDasharray="3 3" strokeWidth={1} />
        {bins.map((b) => (
          <circle
            key={b.forecast}
            cx={p(b.forecast)}
            cy={S - p(b.observed)}
            r={3 + 6 * Math.sqrt(b.n / maxN)}
            fill="var(--brass)"
            fillOpacity={0.8}
            stroke="var(--card)"
            strokeWidth={2}
            onMouseEnter={() => setHover(b)}
            onMouseLeave={() => setHover(null)}
          />
        ))}
      </svg>
      <figcaption className="mt-1 min-h-8 text-micro text-text-muted">
        {hover
          ? `Said ${prob(hover.forecast)}, happened ${prob(hover.observed)} of ${hover.n.toLocaleString()} times`
          : `${label}: stated probability (across) against how often it happened (up), in %`}
      </figcaption>
    </figure>
  );
}

/**
 * Where outcomes landed inside their own forecast, in tenths. A calibrated
 * forecast puts 10% in each: a tall middle means the ranges were too wide,
 * tall ends that they were too narrow.
 */
export function PitHistogram({ pit }: { pit: number[] }) {
  const W = 240;
  const H = 120;
  const pad = { t: 8, b: 18, l: 4, r: 4 };
  const top = Math.max(0.15, ...pit);
  const bw = (W - pad.l - pad.r) / pit.length;
  const y = (v: number) => pad.t + (1 - v / top) * (H - pad.t - pad.b);
  const [hover, setHover] = useState<number | null>(null);
  return (
    <figure className="flex flex-col">
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full max-w-[280px]" role="img" aria-label="Share of outcomes in each tenth of their forecast distribution">
        {pit.map((v, i) => (
          <rect
            key={i}
            x={pad.l + i * bw + 1}
            y={y(v)}
            width={bw - 2}
            height={H - pad.b - y(v)}
            rx={2}
            fill="var(--brass)"
            fillOpacity={hover === i ? 0.9 : 0.6}
            onMouseEnter={() => setHover(i)}
            onMouseLeave={() => setHover(null)}
          />
        ))}
        <line x1={pad.l} x2={W - pad.r} y1={y(0.1)} y2={y(0.1)} className="stroke-text-secondary" strokeDasharray="3 3" strokeWidth={1} />
        <text x={pad.l} y={H - 5} className="fill-text-muted text-[9px]">
          lowest tenth
        </text>
        <text x={W - pad.r} y={H - 5} textAnchor="end" className="fill-text-muted text-[9px]">
          highest tenth
        </text>
      </svg>
      <figcaption className="mt-1 min-h-8 text-micro text-text-muted">
        {hover != null
          ? `${(pit[hover]! * 100).toFixed(1)}% of outcomes fell in tenth ${hover + 1} of their forecast; 10% is calibrated`
          : "Where outcomes fell in their own forecast; the dashed line is the 10% a calibrated forecast puts in each tenth"}
      </figcaption>
    </figure>
  );
}
