import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { forecastApi } from "../lib/api";

/**
 * Where the forecast model ranks one stock for the next few sessions, and
 * which factors put it there.
 */
export function SymbolForecast({ symbol }: { symbol: string }) {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["forecast-symbol", symbol],
    queryFn: () => forecastApi.symbol(symbol),
    staleTime: 30 * 60_000,
    retry: false,
  });
  if (isError) return null;
  const p = data?.latest;
  return (
    <section className="border-t border-border-subtle px-4 py-4" aria-label="Forecast model">
      <div className="flex items-baseline justify-between">
        <h3 className="text-meta font-semibold text-text-secondary">Forecast model, next {data?.horizon ?? 5} sessions</h3>
        <Link to="/forecast" className="text-micro font-medium text-accent-text hover:underline">
          How it works
        </Link>
      </div>
      {isLoading || !p ? (
        <div className="skeleton mt-2 h-14" />
      ) : (
        <>
          <p className="mt-2 text-ui text-text-primary">
            Ranks <span className="font-semibold">{p.percentile.toFixed(0)}th</span> of the universe
            {p.percentile >= 90 ? " — among the most likely to outperform." : p.percentile <= 10 ? " — among the most likely to underperform." : "."}
          </p>
          <div className="mt-2 h-1.5 rounded-full bg-bg-chip">
            <span className="block h-full rounded-full bg-brand" style={{ width: `${Math.max(2, p.percentile)}%` }} />
          </div>
          <p className="mt-2 flex flex-wrap gap-1.5">
            {p.drivers.map((d) => (
              <span key={d.key} className={"rounded-full px-2 py-px text-micro " + (d.contribution >= 0 ? "bg-semantic-up-soft text-semantic-up" : "bg-semantic-down-soft text-semantic-down")}>
                {d.label}
              </span>
            ))}
          </p>
          {data?.stale && <p className="mt-1.5 text-micro text-text-muted">From an older run: the model runs after each close.</p>}
        </>
      )}
    </section>
  );
}
