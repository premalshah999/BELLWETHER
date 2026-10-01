import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, type Outlook, type Scenario } from "../lib/api";
import { formatDateTime } from "../lib/format";

const SCENARIOS: { key: "bull" | "base" | "bear"; label: string; tone: string }[] = [
  { key: "bull", label: "Bull", tone: "bg-semantic-up" },
  { key: "base", label: "Base", tone: "bg-text-muted" },
  { key: "bear", label: "Bear", tone: "bg-semantic-down" },
];

/**
 * The AI's scenario outlook for one stock: three cases with stated
 * probabilities, later scored against what happened on the track record page.
 */
export function SymbolOutlook({ symbol }: { symbol: string }) {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ["outlooks", symbol],
    queryFn: () => api.outlooks(symbol, 1),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const write = useMutation({
    mutationFn: () => api.generateOutlook(symbol),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["outlooks"] }),
  });
  const o: Outlook | undefined = data?.outlooks?.[0];

  return (
    <section className="border-t border-border-subtle px-4 py-4" aria-label="AI outlook">
      <div className="flex items-baseline justify-between">
        <h3 className="text-meta font-semibold text-text-secondary">AI outlook{o ? `, next ${o.horizon_days} days` : ""}</h3>
        <Link to="/ai" className="text-micro font-medium text-accent-text hover:underline">
          Track record
        </Link>
      </div>
      {isLoading ? (
        <div className="skeleton mt-2 h-20" />
      ) : o ? (
        <>
          {o.quant && (
            <p className="mt-1.5 text-micro leading-snug text-text-muted">
              Bull and bear are moves beyond ±{o.quant.threshold_pct.toFixed(1)}%, one normal-sized move for this stock. Probabilities start from{" "}
              {o.quant.source === "engine" ? "the forecast engine's simulation" : "the stock's own past returns"}
              {adjusted(o) ? "; the AI adjusted them on the evidence below." : "; the AI saw no news the model had missed and left them."}
            </p>
          )}
          <div className="mt-2 flex flex-col gap-1.5">
            {SCENARIOS.map((s) => (
              <ScenarioRow
                key={s.key}
                label={s.label}
                tone={s.tone}
                scenario={o[s.key]}
                prior={adjusted(o) ? o.quant?.[s.key] : undefined}
                happened={o.actual_scenario === s.key}
              />
            ))}
          </div>
          {adjusted(o) && (o.adjustment?.evidence?.length ?? 0) > 0 && (
            <ul className="mt-2 list-disc pl-4 text-meta leading-snug text-text-secondary">
              {o.adjustment!.evidence!.map((ev) => (
                <li key={ev}>{ev}</li>
              ))}
            </ul>
          )}
          {o.adjustment?.reasoning && <p className="mt-2 text-meta leading-relaxed text-text-secondary">{o.adjustment.reasoning}</p>}
          {o.key_risk && <p className="mt-2 text-meta leading-relaxed text-text-secondary">Key risk: {o.key_risk}</p>}
          <p className="mt-1.5 text-micro text-text-muted">
            Written {formatDateTime(o.created_at)} at ${o.price_at_creation.toFixed(2)}
            {o.resolved_at && o.realized_move_percent != null
              ? `. Scored: it moved ${o.realized_move_percent >= 0 ? "+" : ""}${o.realized_move_percent.toFixed(1)}%.`
              : ". Not yet scored."}
          </p>
        </>
      ) : (
        <p className="mt-2 text-meta text-text-muted">No outlook has been written for {symbol} yet.</p>
      )}
      <button type="button" onClick={() => write.mutate()} disabled={write.isPending} className="action-secondary mt-3">
        {write.isPending ? "Writing…" : o ? "Write a new outlook" : "Write an outlook"}
      </button>
      {write.isError && (
        <p role="alert" className="mt-2 text-meta text-semantic-down">
          {write.error instanceof Error ? write.error.message : "The outlook could not be written."}
        </p>
      )}
    </section>
  );
}

/** Whether the writer moved the prior at all. */
function adjusted(o: Outlook) {
  const a = o.adjustment;
  return !!a && (a.shift_sigma !== 0 || (a.vol_scale !== 0 && a.vol_scale !== 1));
}

function ScenarioRow({ label, tone, scenario, prior, happened }: { label: string; tone: string; scenario: Scenario; prior?: number; happened: boolean }) {
  const pct = Math.round(scenario.probability * 100);
  return (
    <div title={scenario.reasoning + (prior != null ? ` (model: ${Math.round(prior * 100)}%)` : "")} className="flex items-center gap-2 text-meta">
      <span className={"w-9 shrink-0 " + (happened ? "font-semibold text-text-primary" : "text-text-secondary")}>{label}</span>
      <span className="relative h-1.5 flex-1 rounded-full bg-bg-base">
        <span className={"block h-full rounded-full " + tone} style={{ width: `${pct}%` }} />
        {prior != null && (
          <span className="absolute -top-0.5 h-2.5 w-0.5 rounded bg-text-primary" style={{ left: `${Math.round(prior * 100)}%` }} aria-label={`model ${Math.round(prior * 100)}%`} />
        )}
      </span>
      <span className="w-9 shrink-0 text-right font-num text-text-primary">{pct}%</span>
      <span className="w-14 shrink-0 text-right font-num text-text-muted">
        {scenario.move_percent >= 0 ? "+" : ""}
        {scenario.move_percent.toFixed(1)}%
      </span>
    </div>
  );
}
