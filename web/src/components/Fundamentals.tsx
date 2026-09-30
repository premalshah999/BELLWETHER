import { useQuery } from "@tanstack/react-query";
import { api, type PeerStat } from "../lib/api";
import { tickerOf } from "../lib/symbol";
import { Empty } from "./ui/Empty";
import { Panel } from "./ui/Panel";
import { RailCloseButton } from "./layout/RailToggle";
import { Landmark } from "lucide-react";

/**
 * What a company is worth, next to what its peers are worth.
 *
 * The absolute ratios are the least interesting thing here and are shown
 * smallest. A P/E of 23 is not information; a P/E of 23 where the sector
 * trades at 12, on returns in the bottom quarter of that sector, is the whole
 * argument. The peer column carries the emphasis.
 */
export function Fundamentals({ symbol, onHide }: { symbol: string; onHide?: () => void }) {
  const ticker = tickerOf(symbol);
  const { data, isError } = useQuery({
    queryKey: ["fundamentals", symbol],
    queryFn: () => api.fundamentals(symbol),
    retry: false,
    staleTime: 6 * 60 * 60 * 1000,
  });

  if (isError || !data) {
    return (
      <Panel title="Valuation" collapseKey="valuation" className="min-w-0 flex-1">
        <Empty icon={Landmark} title={`No fundamentals for ${ticker} yet.`} hint="Refreshed weekly." />
      </Panel>
    );
  }

  const s = data.snapshot;
  const peers = data.peers;

  return (
    <Panel
      title="Valuation"
      collapseKey="valuation"
      scroll
      className="min-w-0 flex-1"
      action={onHide ? <RailCloseButton side="right" onClose={onHide} /> : undefined}
    >
      <div className="flex flex-wrap gap-x-4 gap-y-1 border-b border-border-subtle px-3 py-2 font-mono text-meta">
        <Stat label="P/E" v={s.pe_trailing} />
        <Stat label="P/B" v={s.price_to_book} />
        <Stat label="EV/EBITDA" v={s.ev_to_ebitda} />
        <Stat label="ROE" v={s.return_on_equity} pct />
        <Stat label="OP MGN" v={s.operating_margin} pct />
        <Stat label="D/E" v={s.debt_to_equity} digits={1} />
      </div>

      {data.mixed_currency && (
        <p className="border-b border-border-subtle px-3 py-1.5 text-meta leading-relaxed text-text-muted">
          {ticker} files in {s.financial_currency} while the share trades in {s.quote_currency}.
          Ratios combining reported figures with the market price are withheld rather than shown
          in mixed units.
        </p>
      )}

      {peers && (
        <div className="px-3 py-2">
          <p className="mb-2 font-mono text-micro text-text-muted">
            against {peers.peer_count} listed peers in {peers.industry}
          </p>
          <div className="space-y-0.5">
            {peers.stats
              .filter((st) => st.value != null && st.percentile != null && st.median != null)
              .map((st) => (
                <PeerRow key={st.metric} stat={st} />
              ))}
          </div>
        </div>
      )}

      {!!data.trends?.length && (
        <div className="border-t border-border-subtle px-3 py-2">
          <p className="mb-2 font-mono text-micro text-text-muted">
            reported quarters
          </p>
          {data.trends.map((t) => (
            <div key={t.metric} className="flex flex-wrap items-baseline gap-x-2 font-mono text-meta">
              <span className="w-[104px] shrink-0 text-text-muted">{t.metric}</span>
              <span
                className={
                  t.direction === "improving"
                    ? "text-semantic-up"
                    : t.direction === "deteriorating"
                      ? "text-semantic-down"
                      : "text-text-secondary"
                }
              >
                {t.direction}
              </span>
              {t.change_percent != null && (
                <span
                  className={
                    t.direction === "improving"
                      ? "text-semantic-up"
                      : t.direction === "deteriorating"
                        ? "text-semantic-down"
                        : "text-text-secondary"
                  }
                >
                  {t.change_percent >= 0 ? "+" : ""}
                  {t.change_percent.toFixed(1)}%
                </span>
              )}
              <span className="text-text-muted">
                {t.from?.slice(0, 7)}–{t.to?.slice(0, 7)}
                {/* Gaps are stated, never smoothed over: a five-point line
                    presented as eight quarters would misdescribe the slope. */}
                {!!t.gaps && ` · ${t.gaps} not reported`}
              </span>
            </div>
          ))}
        </div>
      )}
    </Panel>
  );
}

function Stat({ label, v, pct, digits = 2 }: { label: string; v?: number; pct?: boolean; digits?: number }) {
  return (
    <span>
      <span className="text-text-muted">{label} </span>
      <span className="text-text-primary">
        {v == null ? "—" : pct ? `${(v * 100).toFixed(1)}%` : v.toFixed(digits)}
      </span>
    </span>
  );
}

/**
 * One metric against its peer group, sized for a 320px rail.
 *
 * An earlier version laid this out as metric / value / median / bar / verdict
 * on one line. That needs about 400px, so in the rail the verdict ran off the
 * right edge and the rows below were clipped — the reader saw a number and a
 * median with the entire point of the comparison cut off.
 *
 * The favourable/unfavourable judgement now rides on the colour of the value
 * itself, which costs no width at all, and the bar spans the row beneath so
 * position still carries the percentile.
 */
function PeerRow({ stat }: { stat: PeerStat }) {
  const pctile = stat.percentile ?? 50;
  // Favourable means good for an owner: high for returns, low for multiples.
  const favourable = stat.higher_is_better ? pctile >= 50 : pctile < 50;
  const tone = favourable ? "text-semantic-up" : "text-semantic-down";

  return (
    <div className="py-1.5" title={`${pctile.toFixed(0)}th percentile of ${stat.peers} peers`}>
      <div className="flex items-baseline gap-2 font-mono text-meta">
        <span className="min-w-0 flex-1 truncate text-text-muted">{stat.metric}</span>
        <span className={"shrink-0 " + tone}>{fmt(stat.value, stat.unit)}</span>
        <span className="w-[74px] shrink-0 whitespace-nowrap text-right text-text-muted">
          med {fmt(stat.median, stat.unit)}
        </span>
      </div>
      <span className="mt-1 block h-px w-full bg-border-subtle">
        <span
          className={"block h-px " + (favourable ? "bg-semantic-up" : "bg-semantic-down")}
          style={{ width: `${Math.max(2, Math.min(100, pctile))}%` }}
        />
      </span>
    </div>
  );
}

/**
 * Renders a peer metric in its own units.
 *
 * The unit comes from the server rather than being guessed, because the
 * upstream provider mixes them: return on equity arrives as a fraction and
 * dividend yield as a percentage.
 */
function fmt(v: number | undefined, unit?: string) {
  if (v == null) return "—";
  if (unit === "fraction") return `${(v * 100).toFixed(1)}%`;
  if (unit === "percent") return `${v.toFixed(2)}%`;
  return v.toFixed(2);
}
