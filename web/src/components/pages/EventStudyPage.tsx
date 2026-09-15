import { useQuery } from "@tanstack/react-query";
import { FlaskConical } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "../../lib/api";
import { Empty } from "../ui/Empty";

const HOLDING_DAY_OPTIONS = [1, 3, 5, 10, 20];

/**
 * Does this event type actually move the stocks it names, historically?
 *
 * Retail tools show a chart with an arrow next to an earnings date and let
 * the reader's eye do the statistics. This runs the actual comparison: for
 * every event of a chosen type, how the stock moved over the days after
 * relative to its own venue's benchmark (S&P 500 or NIFTY 50) -- averaged,
 * with a hit rate and a sample size, so a mean of three events reads as the
 * coincidence it is rather than a pattern.
 *
 * See internal/eventstudy's package doc for the method, in particular why
 * the anchor is the moment this system knew about the event rather than
 * when it happened: that is the only version of the question a downstream
 * decision could ever have acted on.
 */
export function EventStudyPage() {
  const [type, setType] = useState("");
  const [days, setDays] = useState(5);

  const { data: typesData } = useQuery({
    queryKey: ["eventstudy-types"],
    queryFn: api.eventStudyTypes,
    staleTime: 5 * 60_000,
  });
  const types = typesData?.types ?? [];

  useEffect(() => {
    if (!type && types[0]) setType(types[0].event_type);
  }, [type, types]);

  const { data: result, isLoading } = useQuery({
    queryKey: ["eventstudy", type, days],
    queryFn: () => api.eventStudy(type, days),
    enabled: type !== "",
  });

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <div className="flex h-10 shrink-0 items-center gap-3 border-b border-border-subtle px-4">
        <span className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
          Event Study
        </span>
        <select
          value={type}
          onChange={(e) => setType(e.target.value)}
          className="border border-border-subtle bg-bg-base px-2 py-1 font-mono text-meta text-text-primary outline-none focus:border-border-focus"
        >
          {types.length === 0 && <option value="">no event types yet</option>}
          {types.map((t) => (
            <option key={t.event_type} value={t.event_type}>
              {t.event_type.replace(/_/g, " ").toLowerCase()} ({t.count})
            </option>
          ))}
        </select>
        <label className="flex items-center gap-1.5 font-mono text-meta text-text-muted">
          hold
          <select
            value={days}
            onChange={(e) => setDays(Number(e.target.value))}
            className="border border-border-subtle bg-bg-base px-1.5 py-1 font-mono text-meta text-text-primary outline-none focus:border-border-focus"
          >
            {HOLDING_DAY_OPTIONS.map((d) => (
              <option key={d} value={d}>
                {d}d
              </option>
            ))}
          </select>
        </label>
      </div>

      {types.length === 0 ? (
        <Empty
          icon={FlaskConical}
          title="No classified events yet."
          hint="An event study needs the archive to hold classified events of some type before it can measure anything."
        />
      ) : isLoading || !result ? (
        <div className="flex flex-1 items-center justify-center text-meta text-text-muted">
          computing…
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="grid grid-cols-1 divide-x divide-y divide-border-subtle border-b border-border-subtle md:grid-cols-4 md:divide-y-0">
            <Metric
              label="Mean abnormal return"
              value={result.mean_abnormal_return_pct}
              suffix="%"
              tone={result.mean_abnormal_return_pct > 0 ? "text-semantic-up" : result.mean_abnormal_return_pct < 0 ? "text-semantic-down" : ""}
              hint={`Average return vs. ${result.benchmark} over ${result.holding_days} trading day(s) after each event.`}
            />
            <Metric
              label="Median abnormal return"
              value={result.median_abnormal_return_pct}
              suffix="%"
              tone={result.median_abnormal_return_pct > 0 ? "text-semantic-up" : result.median_abnormal_return_pct < 0 ? "text-semantic-down" : ""}
              hint="Less exposed to one outlier event than the mean is."
            />
            <Metric
              label="Hit rate"
              value={result.hit_rate}
              suffix="%"
              hint="Share of events whose abnormal return was positive."
            />
            <Metric
              label="Samples"
              value={result.samples}
              digits={0}
              hint={`Of ${result.total_events} events of this type in the archive.`}
              sub={`± ${result.stddev_pct.toFixed(1)}% std. dev.`}
            />
          </div>

          {result.warnings && result.warnings.length > 0 && (
            <ul className="space-y-1.5 border-b border-border-subtle px-4 py-3">
              {result.warnings.map((w, i) => (
                <li key={i} className="text-meta leading-relaxed text-semantic-down">
                  {w}
                </li>
              ))}
            </ul>
          )}

          <p className="px-4 py-3 text-meta leading-relaxed text-text-muted">
            Anchored on the moment this system discovered each event, not when it happened or was
            published -- the only version of "known" a decision could actually have acted on. See
            internal/eventstudy for the full method.
          </p>
        </div>
      )}
    </div>
  );
}

function Metric({
  label,
  value,
  digits = 2,
  suffix = "",
  hint,
  sub,
  tone,
}: {
  label: string;
  value: number;
  digits?: number;
  suffix?: string;
  hint: string;
  sub?: string;
  tone?: string;
}) {
  return (
    <div className="bg-bg-panel px-5 py-6">
      <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">{label}</p>
      <p className={"mt-1.5 font-mono text-hero leading-none tabular-nums " + (tone || "text-text-primary")}>
        {Number.isFinite(value) ? `${value.toFixed(digits)}${suffix}` : "—"}
      </p>
      {sub && <p className="mt-1 font-mono text-meta text-text-muted">{sub}</p>}
      <p className="mt-2 max-w-[44ch] text-meta leading-relaxed text-text-muted">{hint}</p>
    </div>
  );
}
