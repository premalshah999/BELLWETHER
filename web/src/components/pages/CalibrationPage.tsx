import { useQuery } from "@tanstack/react-query";
import { Target } from "lucide-react";
import { api, type Calibration, type Outlook } from "../../lib/api";
import { formatDateTime } from "../../lib/format";
import { Empty } from "../ui/Empty";
import { Panel } from "../ui/Panel";
import { Pill } from "../ui/Pill";
import { PageHeader } from "../ui/controls";

/**
 * Whether the model's confidence means anything.
 *
 * The three numbers at the top are the only honest summary of a forecaster:
 * how often it is right, how well its stated confidence matches that, and how
 * much it has actually been asked. A page of predictions without them is a
 * page of opinions.
 */
export function CalibrationPage() {
  const { data: cal } = useQuery({
    queryKey: ["calibration"],
    queryFn: api.calibration,
    refetchInterval: 300_000,
  });
  const { data: outlooks } = useQuery({
    queryKey: ["outlooks"],
    queryFn: () => api.outlooks(undefined, 60),
    refetchInterval: 120_000,
  });

  const rows = (outlooks?.outlooks ?? []).filter((o) => o.resolved_at);
  const skill = skillScore(cal);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
    <PageHeader
      title="AI track record"
      subtitle="Every AI outlook is scored against what actually happened, so you can see how far to trust it."
    />
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="grid grid-cols-1 divide-x divide-y divide-border-subtle border-b border-border-subtle md:grid-cols-3 md:divide-y-0">
        <Metric
          label="Overconfidence"
          value={overconfidence(cal)}
          digits={2}
          hint="Stated confidence minus what actually happened. Positive means the model claims more certainty than it earns."
          tone={(v) => (v == null ? "" : v > 0.05 ? "text-semantic-down" : "text-semantic-up")}
        />
        <Metric
          label="Skill vs baseline"
          value={skill}
          digits={2}
          hint="Brier score against always predicting the base rate. Above zero is better than guessing; below zero is worse."
          tone={(v) => (v == null ? "" : v > 0 ? "text-semantic-up" : "text-semantic-down")}
        />
        <Metric
          label="Outlooks"
          value={cal ? cal.resolved + cal.pending : undefined}
          digits={0}
          hint="Total forecasts made. The resolved share is what the other two numbers rest on."
          sub={cal ? `${cal.resolved} resolved · ${cal.pending} pending` : undefined}
        />
      </div>

      {cal && cal.buckets.some((b) => b.forecasts > 0) && (
        <ReliabilityCurve cal={cal} />
      )}

      {cal && cal.resolved === 0 && (
        <p className="border-b border-border-subtle px-3 py-2 text-meta leading-relaxed text-text-muted">
          No outlook has resolved yet, so the figures above rest on nothing. They become
          meaningful once forecasts reach their horizon and are scored against the realised
          price — until then, treat the model's confidence as unverified.
        </p>
      )}

      <Panel title="Recent outlook validations" className="border-none">
        {rows.length === 0 ? (
          <Empty
            icon={Target}
            title="No resolved outlooks."
            hint="An outlook resolves once its horizon passes and the realised price can be compared with what was predicted."
          />
        ) : (
          <table className="w-full border-collapse">
            <thead>
              <tr className="border-b border-border-subtle text-left">
                {["Date", "Symbol", "Prediction", "Horizon", "Realised", "Result"].map((h) => (
                  <th
                    key={h}
                    className="px-3.5 py-2 font-mono text-micro font-medium text-text-muted"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-border-subtle">
              {rows.map((o) => (
                <Row key={o.id} outlook={o} />
              ))}
            </tbody>
          </table>
        )}
      </Panel>
    </div>
    </div>
  );
}

/**
 * A reliability diagram.
 *
 * The one picture that answers "does this model's confidence mean anything".
 * The diagonal is perfect calibration: of everything it called 70% likely,
 * 70% should have happened. A point below the line is overconfidence, above
 * is under. Bucket size is drawn as the dot's area, because a bucket holding
 * three forecasts should not look as authoritative as one holding three
 * hundred — and on a plain scatter it would.
 */
function ReliabilityCurve({ cal }: { cal: Calibration }) {
  const size = 200;
  const pad = 26;
  const inner = size - pad * 2;
  const live = cal.buckets.filter((b) => b.forecasts > 0);
  const maxN = Math.max(...live.map((b) => b.forecasts), 1);

  const x = (v: number) => pad + v * inner;
  const y = (v: number) => size - pad - v * inner;

  return (
    <div className="flex flex-wrap items-start gap-6 border-b border-border-subtle bg-bg-panel px-5 py-4">
      <svg width={size} height={size} className="shrink-0">
        {/* Perfect calibration. Dashed, because it is a reference and not a
            measurement. */}
        <line
          x1={x(0)} y1={y(0)} x2={x(1)} y2={y(1)}
          className="stroke-border-focus" strokeWidth={1} strokeDasharray="3 3"
        />
        <line x1={x(0)} y1={y(0)} x2={x(1)} y2={y(0)} className="stroke-border-subtle" strokeWidth={1} />
        <line x1={x(0)} y1={y(0)} x2={x(0)} y2={y(1)} className="stroke-border-subtle" strokeWidth={1} />

        {live.map((b, i) => {
          const cx = x(b.mean_stated);
          const cy = y(b.observed_rate);
          const r = 2 + (b.forecasts / maxN) * 5;
          const over = b.mean_stated > b.observed_rate;
          return (
            <g key={i}>
              <line
                x1={cx} y1={cy} x2={cx} y2={y(b.mean_stated)}
                className={over ? "stroke-semantic-down" : "stroke-semantic-up"} strokeWidth={1} opacity={0.35}
              />
              <circle cx={cx} cy={cy} r={r} className={over ? "fill-semantic-down" : "fill-semantic-up"} />
            </g>
          );
        })}

        <text x={x(0.5)} y={size - 6} textAnchor="middle" className="fill-text-muted" fontSize={10}>
          stated confidence
        </text>
        <text
          x={10} y={y(0.5)} textAnchor="middle" className="fill-text-muted" fontSize={10}
          transform={`rotate(-90 10 ${y(0.5)})`}
        >
          actually happened
        </text>
      </svg>

      <div className="max-w-[52ch]">
        <p className="font-mono text-micro text-text-muted">
          reliability
        </p>
        <p className="mt-1.5 text-ui leading-relaxed text-text-secondary">
          Each dot is a confidence band. The dashed diagonal is perfect calibration — of
          everything called 70% likely, 70% happened. Dots below the line are overconfident,
          above are underconfident, and dot size is how many forecasts the band rests on.
        </p>
        <p className="mt-2 text-meta leading-relaxed text-text-muted">
          {live.length} of {cal.buckets.length} bands have forecasts in them. A band holding a
          handful of predictions says very little on its own.
        </p>
      </div>
    </div>
  );
}

function Row({ outlook: o }: { outlook: Outlook }) {
  const predicted = strongest(o);
  const hit = o.actual_scenario === predicted.name;
  return (
    <tr className="hover:bg-bg-panel-hover">
      <td className="px-3.5 py-2 font-mono text-meta text-text-muted">
        {formatDateTime(o.created_at).slice(0, 11)}
      </td>
      <td className="px-3.5 py-2 font-mono text-meta text-text-primary">{o.symbol}</td>
      <td className="px-3.5 py-2 font-mono text-meta text-text-secondary">
        {predicted.name} {(predicted.probability * 100).toFixed(0)}%
      </td>
      <td className="px-3.5 py-2 font-mono text-meta text-text-muted">{o.horizon_days}d</td>
      <td className="px-3.5 py-2 font-mono text-meta">
        <span className="text-text-secondary">{o.realized_price?.toFixed(2) ?? "—"}</span>
        {o.realized_move_percent != null && (
          <span
            className={
              "ml-1.5 " +
              (o.realized_move_percent >= 0 ? "text-semantic-up" : "text-semantic-down")
            }
          >
            {o.realized_move_percent >= 0 ? "+" : ""}
            {o.realized_move_percent.toFixed(1)}%
          </span>
        )}
      </td>
      <td className="px-3 py-1.5">
        <Pill tone={hit ? "up" : "down"}>{hit ? "hit" : "miss"}</Pill>
      </td>
    </tr>
  );
}

function Metric({
  label,
  value,
  digits,
  hint,
  sub,
  tone,
}: {
  label: string;
  value?: number;
  digits: number;
  hint: string;
  sub?: string;
  tone?: (v?: number) => string;
}) {
  return (
    <div className="bg-bg-panel px-5 py-6">
      <p className="font-mono text-micro text-text-muted">{label}</p>
      <p className={"mt-1.5 font-mono text-hero leading-none " + (tone?.(value) ?? "text-text-primary")}>
        {value == null || !Number.isFinite(value) ? "—" : value.toFixed(digits)}
      </p>
      {sub && <p className="mt-1 font-mono text-meta text-text-muted">{sub}</p>}
      <p className="mt-2 max-w-[44ch] text-meta leading-relaxed text-text-muted">{hint}</p>
    </div>
  );
}

/** The scenario the model backed hardest. */
function strongest(o: Outlook): { name: "base" | "bull" | "bear"; probability: number } {
  const options = [
    { name: "base" as const, probability: o.base?.probability ?? 0 },
    { name: "bull" as const, probability: o.bull?.probability ?? 0 },
    { name: "bear" as const, probability: o.bear?.probability ?? 0 },
  ];
  return options.reduce((a, b) => (b.probability > a.probability ? b : a));
}

/**
 * Mean stated confidence minus the rate those forecasts actually occurred,
 * weighted by how many forecasts fall in each bucket.
 *
 * Weighted because an unweighted average lets a bucket holding two forecasts
 * move the headline number as much as one holding two hundred.
 */
function overconfidence(cal?: Calibration): number | undefined {
  if (!cal) return undefined;
  let n = 0;
  let sum = 0;
  for (const b of cal.buckets) {
    if (b.forecasts === 0) continue;
    n += b.forecasts;
    sum += (b.mean_stated - b.observed_rate) * b.forecasts;
  }
  return n === 0 ? undefined : sum / n;
}

/** Brier skill against the baseline. Positive beats guessing. */
function skillScore(cal?: Calibration): number | undefined {
  if (!cal || cal.resolved === 0 || cal.baseline_brier === 0) return undefined;
  return 1 - cal.mean_brier / cal.baseline_brier;
}
