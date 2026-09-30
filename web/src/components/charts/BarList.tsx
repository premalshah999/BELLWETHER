import { useState } from "react";

export interface BarDatum {
  key: string;
  label: string;
  sub?: string;
  value: number;
  /** Shown instead of the formatted value, e.g. "$4.2M · 3 insiders". */
  display?: string;
  onClick?: () => void;
}

/**
 * A ranked horizontal bar list: one series, so no legend; each bar carries
 * its own label and value, and the hovered row says exactly what it is.
 */
export function BarList({
  data,
  tone = "up",
  format = (v) => v.toLocaleString("en-US"),
  empty = "Nothing yet.",
}: {
  data: BarDatum[];
  tone?: "up" | "down" | "accent";
  format?: (v: number) => string;
  empty?: string;
}) {
  const [hover, setHover] = useState<string | null>(null);
  if (data.length === 0) return <p className="px-5 py-8 text-ui text-text-muted">{empty}</p>;
  const max = Math.max(...data.map((d) => Math.abs(d.value)), 1);
  const fill = tone === "up" ? "bg-semantic-up" : tone === "down" ? "bg-semantic-down" : "bg-brand";
  return (
    <ul className="flex flex-col py-1.5">
      {data.map((d) => (
        <li key={d.key}>
          <button
            type="button"
            onClick={d.onClick}
            onMouseEnter={() => setHover(d.key)}
            onMouseLeave={() => setHover(null)}
            className="group grid w-full grid-cols-[4.5rem_minmax(0,1fr)_auto] items-center gap-3 px-5 py-1.5 text-left transition-colors hover:bg-bg-panel-hover"
          >
            <span className="min-w-0">
              <span className="block truncate font-num text-[12.5px] font-medium text-text-primary">{d.label}</span>
            </span>
            <span className="relative flex h-6 items-center">
              <span
                className={"h-2.5 rounded-r-[3px] transition-[width,opacity] " + fill + (hover && hover !== d.key ? " opacity-40" : "")}
                style={{ width: `${Math.max(2, (Math.abs(d.value) / max) * 100)}%` }}
              />
              {d.sub && (
                <span className="pointer-events-none absolute left-2 top-1/2 hidden -translate-y-1/2 truncate text-micro text-text-secondary group-hover:block">
                  {d.sub}
                </span>
              )}
            </span>
            <span className="w-24 text-right font-num text-[12px] text-text-secondary">{d.display ?? format(d.value)}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}

/** $1.2B, $34.5M, $820K, $9,400. */
export function money(v: number | undefined | null): string {
  if (v == null || !Number.isFinite(v)) return "—";
  const a = Math.abs(v);
  const sign = v < 0 ? "−" : "";
  if (a >= 1e9) return `${sign}$${(a / 1e9).toFixed(a >= 1e10 ? 0 : 1)}B`;
  if (a >= 1e6) return `${sign}$${(a / 1e6).toFixed(a >= 1e8 ? 0 : 1)}M`;
  if (a >= 1e4) return `${sign}$${Math.round(a / 1e3)}K`;
  return `${sign}$${a.toLocaleString("en-US", { maximumFractionDigits: 0 })}`;
}
