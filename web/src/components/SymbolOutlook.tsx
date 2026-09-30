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
          <div className="mt-2 flex flex-col gap-1.5">
            {SCENARIOS.map((s) => (
              <ScenarioRow key={s.key} label={s.label} tone={s.tone} scenario={o[s.key]} happened={o.actual_scenario === s.key} />
            ))}
          </div>
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

function ScenarioRow({ label, tone, scenario, happened }: { label: string; tone: string; scenario: Scenario; happened: boolean }) {
  const pct = Math.round(scenario.probability * 100);
  return (
    <div title={scenario.reasoning} className="flex items-center gap-2 text-meta">
      <span className={"w-9 shrink-0 " + (happened ? "font-semibold text-text-primary" : "text-text-secondary")}>{label}</span>
      <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-bg-base">
        <span className={"block h-full rounded-full " + tone} style={{ width: `${pct}%` }} />
      </span>
      <span className="w-9 shrink-0 text-right font-num text-text-primary">{pct}%</span>
      <span className="w-14 shrink-0 text-right font-num text-text-muted">
        {scenario.move_percent >= 0 ? "+" : ""}
        {scenario.move_percent.toFixed(1)}%
      </span>
    </div>
  );
}
