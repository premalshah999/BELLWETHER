import { useQueries, useQuery } from "@tanstack/react-query";
import { TestTubeDiagonal, TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api } from "../../lib/api";
import { useUrlState } from "../../lib/url";
import { typeLabel } from "../EventDrawer";
import { PageHeader, Segmented, Select } from "../ui/controls";

const HOLDS = ["1", "3", "5", "10", "20"] as const;
type Hold = (typeof HOLDS)[number];

type Study = Awaited<ReturnType<typeof api.eventStudy>>;

/** Standard error of the mean, from the reported spread and sample count. */
function stderr(r: Study): number {
  return r.samples > 1 ? r.stddev_pct / Math.sqrt(r.samples) : Infinity;
}

/**
 * Does a kind of news actually move prices?
 *
 * The page answers in a sentence first, then says plainly whether the answer
 * is distinguishable from noise. An average of +0.3% over six hundred cases
 * with a 5% spread is not an edge, and a page that only printed the average
 * in a large green font would imply that it was.
 */
export function EventStudyPage() {
  const [type, setType] = useUrlState("type", "");
  const [hold, setHold] = useUrlState<Hold>("days", "5");
  const [hover, setHover] = useState<Hold | null>(null);

  const { data: typesData } = useQuery({
    queryKey: ["eventstudy-types"],
    queryFn: api.eventStudyTypes,
    staleTime: 5 * 60_000,
  });
  const types = useMemo(
    () => [...(typesData?.types ?? [])].filter((t) => t.event_type !== "UNCLASSIFIED").sort((a, b) => b.count - a.count),
    [typesData],
  );

  // Earnings first: the type with the clearest, best-sampled answer.
  useEffect(() => {
    if (type || !types.length) return;
    setType((types.find((t) => t.event_type === "EARNINGS") ?? types[0]!).event_type);
  }, [type, types, setType]);

  // Every holding period at once: the shape across horizons says more than
  // any one of them, and each is cached on the server.
  const studies = useQueries({
    queries: HOLDS.map((d) => ({
      queryKey: ["eventstudy", type, Number(d)],
      queryFn: () => api.eventStudy(type, Number(d)),
      enabled: type !== "",
      staleTime: 10 * 60_000,
    })),
  });
  const byHold = Object.fromEntries(HOLDS.map((d, i) => [d, studies[i]?.data])) as Record<Hold, Study | undefined>;
  const result = byHold[hold];
  const loading = type !== "" && studies[HOLDS.indexOf(hold)]?.isLoading;

  const t = result && result.samples > 1 ? result.mean_abnormal_return_pct / stderr(result) : 0;
  const real = Math.abs(t) >= 2 && (result?.samples ?? 0) >= 30;
  const phrase = eventPhrase(type);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <PageHeader
        title="Event study"
        subtitle="Does a kind of news actually move prices? Measured against the S&P 500 from the moment Bellwether learned of each event."
      >
        <Select
          label="Kind of event"
          value={type}
          onChange={setType}
          className="min-w-56"
          options={types.map((x) => ({ value: x.event_type, label: `${typeLabel(x.event_type)} (${x.count.toLocaleString()})` }))}
        />
        <Segmented
          label="Holding period"
          value={hold}
          onChange={setHold}
          options={HOLDS.map((d) => ({ value: d, label: `${d} ${d === "1" ? "day" : "days"}` }))}
        />
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {types.length === 0 && typesData ? (
          <div className="flex max-w-md flex-col items-start gap-3 px-6 py-12">
            <TestTubeDiagonal size={22} className="text-text-muted" />
            <p className="font-reading text-display text-text-primary">No classified events yet.</p>
            <p className="text-ui text-text-secondary">A study needs past events of a kind before it can measure anything.</p>
          </div>
        ) : loading || !result ? (
          <div className="mx-auto flex max-w-4xl flex-col gap-4 px-6 py-8">
            <span className="skeleton h-8 w-4/5" />
            <span className="skeleton h-5 w-2/5" />
            <span className="skeleton mt-4 h-56 w-full" />
          </div>
        ) : (
          <div className="mx-auto flex max-w-4xl flex-col gap-8 px-5 py-8 md:px-6">
            <section>
              <p className="font-reading text-[24px] font-semibold leading-snug text-text-primary md:text-[27px]">
                After {phrase}, the stock{" "}
                {result.mean_abnormal_return_pct >= 0 ? "beat" : "trailed"} the market by{" "}
                <span className={result.mean_abnormal_return_pct >= 0 ? "text-semantic-up" : "text-semantic-down"}>
                  {Math.abs(result.mean_abnormal_return_pct).toFixed(2)}%
                </span>{" "}
                on average over the next {result.holding_days} trading {result.holding_days === 1 ? "day" : "days"}.
              </p>
              <p
                className={
                  "mt-3 inline-flex items-center gap-2 rounded-md px-2.5 py-1 text-ui font-medium " +
                  (real ? "bg-brand-muted text-brand" : "bg-bg-panel-hover text-text-secondary")
                }
              >
                {real
                  ? "That is larger than chance would plausibly produce."
                  : "That is within what chance alone would produce, so it is not an edge."}
                <span className="font-normal text-text-muted">t = {t.toFixed(1)}</span>
              </p>
            </section>

            <dl className="grid grid-cols-2 gap-x-8 gap-y-4 border-y border-border-subtle py-5 sm:grid-cols-4">
              <Stat term="Typical case" value={`${signedPct(result.median_abnormal_return_pct)}`} note="The median, less swayed by outliers" />
              <Stat term="Beat the market" value={`${result.hit_rate.toFixed(0)}%`} note="Share of cases above zero" />
              <Stat term="Spread" value={`±${result.stddev_pct.toFixed(1)}%`} note="One standard deviation" />
              <Stat
                term="Cases measured"
                value={result.samples.toLocaleString()}
                note={`Of ${result.total_events.toLocaleString()} in the archive`}
              />
            </dl>

            <section>
              <h2 className="text-emphasis font-semibold text-text-primary">Effect by holding period</h2>
              <p className="mt-1 text-meta text-text-muted">
                Average return against the market, with the range the true average most likely falls in (95%).
              </p>
              <HoldChart byHold={byHold} selected={hold} hover={hover} setHover={setHover} onPick={setHold} />
            </section>

            {result.warnings && result.warnings.length > 0 && (
              <section className="flex gap-3 rounded-lg border border-border-subtle bg-bg-base px-4 py-3.5">
                <TriangleAlert size={16} className="mt-0.5 shrink-0 text-brand" />
                <ul className="flex flex-col gap-1.5">
                  {result.warnings.map((w, i) => (
                    <li key={i} className="text-ui leading-relaxed text-text-secondary">
                      {w.replace(/ -- /g, " — ")}
                    </li>
                  ))}
                </ul>
              </section>
            )}

            <p className="max-w-3xl text-meta leading-relaxed text-text-muted">
              Each case is timed from when Bellwether discovered the event, never from when it happened or was published, because
              that is the only moment a decision could have acted on it. Returns are measured against {result.benchmark}.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}

/** "an earnings report", "a buyback announcement", "a merger". */
function eventPhrase(type: string): string {
  const known: Record<string, string> = {
    EARNINGS: "an earnings report",
    GUIDANCE: "a guidance change",
    BUYBACK: "a buyback announcement",
    DIVIDEND: "a dividend announcement",
    MERGER: "a merger announcement",
    ACQUISITION: "an acquisition",
    INSIDER_TRANSACTION: "an insider trade filing",
    MANAGEMENT_CHANGE: "a management change",
    CREDIT_RATING: "a credit rating action",
    STOCK_SPLIT: "a stock split",
    ORDER_WIN: "a contract win",
    CONTRACT: "a new contract",
    LITIGATION: "news of litigation",
    REGULATORY_ACTION: "a regulatory action",
    FUND_RAISE: "a capital raise",
    STAKE_SALE: "a stake sale",
  };
  if (known[type]) return known[type];
  const words = typeLabel(type).toLowerCase();
  return `${/^[aeiou]/.test(words) ? "an" : "a"} ${words} event`;
}

function signedPct(n: number) {
  return `${n > 0 ? "+" : n < 0 ? "−" : ""}${Math.abs(n).toFixed(2)}%`;
}

function Stat({ term, value, note }: { term: string; value: string; note: string }) {
  return (
    <div>
      <dt className="text-meta text-text-muted">{term}</dt>
      <dd className="mt-0.5 text-display font-semibold text-text-primary">{value}</dd>
      <dd className="text-micro text-text-muted">{note}</dd>
    </div>
  );
}

/**
 * Mean abnormal return per holding period, with a 95% interval.
 *
 * One series, so no legend: the heading names it. Bars grow from a zero
 * baseline; the whisker is what says whether a bar means anything, so a
 * bar whose whisker crosses zero is drawn quieter than one that does not.
 */
function HoldChart({
  byHold,
  selected,
  hover,
  setHover,
  onPick,
}: {
  byHold: Record<Hold, Study | undefined>;
  selected: Hold;
  hover: Hold | null;
  setHover: (h: Hold | null) => void;
  onPick: (h: Hold) => void;
}) {
  const W = 640;
  const H = 220;
  const pad = { t: 16, r: 8, b: 44, l: 44 };
  const pts = HOLDS.map((d) => {
    const r = byHold[d];
    // Too few cases is no answer, not an answer of zero.
    if (!r || r.samples < 2) return { d, r: undefined, mean: 0, lo: 0, hi: 0 };
    const se = stderr(r);
    return { d, r, mean: r.mean_abnormal_return_pct, lo: r.mean_abnormal_return_pct - 1.96 * se, hi: r.mean_abnormal_return_pct + 1.96 * se };
  });
  const extent = Math.max(0.5, ...pts.flatMap((p) => [Math.abs(p.lo), Math.abs(p.hi)]));
  const max = niceCeil(extent);
  const y = (v: number) => pad.t + ((max - v) / (2 * max)) * (H - pad.t - pad.b);
  const band = (W - pad.l - pad.r) / HOLDS.length;
  const barW = Math.min(36, band * 0.42);
  const ticks = [-max, -max / 2, 0, max / 2, max];
  const active = hover ?? selected;
  const ap = pts.find((p) => p.d === active);

  return (
    <div className="mt-4">
      <p className="h-5 text-meta text-text-secondary" aria-live="polite">
        {ap?.r
          ? `${ap.d} ${ap.d === "1" ? "day" : "days"}: ${signedPct(ap.mean)} (95% range ${signedPct(ap.lo)} to ${signedPct(ap.hi)}), ${ap.r.samples.toLocaleString()} cases`
          : ""}
      </p>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full max-w-[720px]" role="img" aria-label="Average abnormal return by holding period">
        {ticks.map((v) => (
          <g key={v}>
            <line x1={pad.l} x2={W - pad.r} y1={y(v)} y2={y(v)} className={v === 0 ? "stroke-text-muted" : "stroke-border-subtle"} strokeWidth={1} />
            <text x={pad.l - 8} y={y(v)} dy="0.32em" textAnchor="end" className="fill-text-muted text-[11px]">
              {v === 0 ? "0%" : `${v > 0 ? "+" : "−"}${Math.abs(v).toFixed(max < 2 ? 1 : 0)}%`}
            </text>
          </g>
        ))}
        {pts.map((p, i) => {
          const cx = pad.l + band * i + band / 2;
          const sig = p.r ? p.lo > 0 || p.hi < 0 : false;
          const up = p.mean >= 0;
          const top = Math.min(y(p.mean), y(0));
          const h = Math.max(1, Math.abs(y(p.mean) - y(0)));
          const isSel = p.d === selected;
          return (
            <g
              key={p.d}
              onMouseEnter={() => setHover(p.d)}
              onMouseLeave={() => setHover(null)}
              onClick={() => onPick(p.d)}
              className="cursor-pointer"
            >
              <rect x={cx - band / 2} y={pad.t} width={band} height={H - pad.t - pad.b} className={hover === p.d ? "fill-bg-panel-hover" : "fill-transparent"} />
              {p.r && (
                <>
                  <rect
                    x={cx - barW / 2}
                    y={top}
                    width={barW}
                    height={h}
                    rx={4}
                    className={(up ? "fill-semantic-up" : "fill-semantic-down") + (sig ? "" : " opacity-45")}
                  />
                  <line x1={cx} x2={cx} y1={y(p.hi)} y2={y(p.lo)} className="stroke-text-primary" strokeWidth={1.5} />
                  <line x1={cx - 6} x2={cx + 6} y1={y(p.hi)} y2={y(p.hi)} className="stroke-text-primary" strokeWidth={1.5} />
                  <line x1={cx - 6} x2={cx + 6} y1={y(p.lo)} y2={y(p.lo)} className="stroke-text-primary" strokeWidth={1.5} />
                </>
              )}
              <text x={cx} y={H - pad.b + 18} textAnchor="middle" className={"text-[12px] " + (isSel ? "fill-text-primary font-semibold" : "fill-text-muted")}>
                {p.d}d
              </text>
              <text x={cx} y={H - pad.b + 34} textAnchor="middle" className="fill-text-secondary text-[11px]">
                {p.r ? signedPct(p.mean) : byHold[p.d] ? "No data" : "…"}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}

function niceCeil(v: number) {
  for (const step of [0.5, 1, 2, 2.5, 5, 10, 20]) if (v <= step) return step;
  return Math.ceil(v / 10) * 10;
}
