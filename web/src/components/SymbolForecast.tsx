import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { forecastApi } from "../lib/api";
import { FanChart, pct, prob } from "./forecast/charts";

/**
 * The engine's forecast for one stock: the range of where it could be over
 * the next 20 sessions, the odds at 5, 10 and 20, and what the ranking
 * leans on.
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
  const d = p?.dist;
  const cal = data?.calibration?.["20"];
  return (
    <section className="border-t border-border-subtle px-4 py-4" aria-label="Forecast">
      <div className="flex items-baseline justify-between">
        <h3 className="text-meta font-semibold text-text-secondary">Forecast, next 20 sessions</h3>
        <Link to="/forecast" className="text-micro font-medium text-accent-text hover:underline">
          Track record
        </Link>
      </div>
      {isLoading || !p ? (
        <div className="skeleton mt-2 h-40" />
      ) : !d ? (
        <p className="mt-2 text-meta text-text-muted">The engine ranks {symbol} at the {p.percentile.toFixed(0)}th percentile but has no distribution for it yet.</p>
      ) : (
        <>
          {d.fan?.length ? <FanChart fan={d.fan} levels={d.fan_levels} earnings={d.earnings?.session} /> : null}
          <table className="mt-3 w-full text-meta">
            <thead>
              <tr className="text-left text-micro text-text-muted">
                <th className="pb-1 font-medium">Sessions</th>
                <th className="pb-1 text-right font-medium">Rises</th>
                <th className="pb-1 text-right font-medium">Beats S&P</th>
                <th className="pb-1 text-right font-medium">80% range</th>
              </tr>
            </thead>
            <tbody className="font-num tabular-nums">
              {d.horizons.map((h) => (
                <tr key={h.h} className="border-t border-border-subtle">
                  <td className="py-1.5 font-sans text-text-secondary">{h.h}</td>
                  <td className="py-1.5 text-right text-text-primary">{prob(h.p_up)}</td>
                  <td className="py-1.5 text-right text-text-primary">{prob(h.p_beat)}</td>
                  <td className="py-1.5 text-right text-text-secondary">
                    {pct(h.q[1]!)} to {pct(h.q[17]!)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <ul className="mt-3 flex flex-col gap-1 text-meta leading-snug text-text-secondary">
            <li>
              Volatility {d.vol_pct.toFixed(0)}% a year ahead, against {d.normal_vol_pct.toFixed(0)}% over the past year
              {d.vol_pct > d.normal_vol_pct * 1.15 ? ": calmer times are over for now." : d.vol_pct < d.normal_vol_pct * 0.85 ? ": quieter than usual." : "."}
            </li>
            {d.earnings && (
              <li>
                Reports {d.earnings.session === 1 ? "next session" : `in ${d.earnings.session} sessions`}; its earnings days have moved about ±
                {d.earnings.typical_move_pct.toFixed(1)}%, and that jump is in the range above.
              </li>
            )}
            <li>
              Ranked {p.percentile.toFixed(0)}th of the universe over 5 sessions and {d.percentile_20.toFixed(0)}th over 20; beta {d.beta.toFixed(2)}.
            </li>
          </ul>
          <p className="mt-2 flex flex-wrap gap-1.5">
            {p.drivers.map((dr) => (
              <span
                key={dr.key}
                className={"rounded-full px-2 py-px text-micro " + (dr.contribution >= 0 ? "bg-semantic-up-soft text-semantic-up" : "bg-semantic-down-soft text-semantic-down")}
              >
                {dr.label}
              </span>
            ))}
          </p>
          <p className="mt-2 text-micro leading-snug text-text-muted">
            {cal ? `Out of sample, the engine's 80% ranges over 20 sessions held ${(cal.calibrated ? cal.calibrated_coverage[1] : cal.coverage[1]).toFixed(0)}% of outcomes. ` : ""}
            The odds of rising have been close to a coin toss; the range is the forecast.
            {data?.stale ? " From an older run: the engine runs after each close." : ""}
          </p>
        </>
      )}
    </section>
  );
}
