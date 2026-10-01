import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Search, X } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, forecastApi, type ForecastPick, type ForecastReport, type ForecastSort } from "../../lib/api";
import { formatDate } from "../../lib/format";
import { PitHistogram, RangeBar, ReliabilityChart, pct, prob } from "../forecast/charts";
import { PageHeader, Segmented, SkeletonRows } from "../ui/controls";

const SORTS: { value: ForecastSort; label: string; title: string }[] = [
  { value: "vol_change", label: "Volatility rising", title: "Forecast volatility highest against its own past year" },
  { value: "earnings", label: "Earnings soon", title: "A report inside the next 20 sessions, soonest first" },
  { value: "risk", label: "Riskiest", title: "Worst average of the bottom 5% of outcomes over five sessions" },
  { value: "p_beat", label: "Beat the S&P", title: "Highest chance of beating the S&P 500 over five sessions" },
];

const PAGE = 40;

function years(rep: ForecastReport) {
  const ys = (rep.years ?? []).map((y) => y.year);
  return ys.length ? (ys.length > 1 ? `${ys[0]}–${ys[ys.length - 1]}` : String(ys[0])) : "";
}

/**
 * The forecast engine: a probability distribution for every stock over the
 * next 5, 10 and 20 sessions, and before that, the evidence of whether its
 * distributions have been right on years it never saw.
 */
export function ForecastPage({ onSelect }: { onSelect: (s: string) => void }) {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [asked, setAsked] = useState("");
  const [sort, setSort] = useState<ForecastSort>("vol_change");
  const [shown, setShown] = useState(PAGE);
  const { data, isLoading, error, isFetching } = useQuery({
    queryKey: ["forecast", asked, sort, shown],
    queryFn: () => forecastApi.latest({ q: asked, sort, limit: shown }),
    staleTime: 10 * 60_000,
    placeholderData: keepPreviousData,
  });
  const open = (s: string) => {
    onSelect(s);
    navigate("/charts");
  };
  const rep = data?.report;
  const ready = rep && rep.version >= 2;

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="Forecast"
        subtitle="A range of outcomes for every stock over the next 5, 10 and 20 sessions, simulated from volatility models, the market's regime, each stock's own earnings history and a factor model, and scored on years it never saw."
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <SkeletonRows count={8} height={56} />
        ) : error ? (
          <p className="px-8 py-10 text-ui text-text-secondary">
            {error instanceof ApiError && error.status === 404 ? "The engine has not run yet. It runs after each close." : (error as Error).message}
          </p>
        ) : ready && data ? (
          <div className="flex flex-col gap-6 px-5 py-6 md:px-8">
            <MarketNow rep={rep} />
            <Scorecard rep={rep} live={data.live_summary} />

            <section className="rounded-xl border border-border-subtle bg-bg-card">
              <div className="flex flex-wrap items-center gap-3 px-5 pb-3 pt-4">
                <h2 className="mr-auto text-[15px] font-semibold text-text-primary">Every stock, next sessions</h2>
                <Segmented
                  label="Sort by"
                  size="sm"
                  options={SORTS}
                  value={sort}
                  onChange={(v) => {
                    setSort(v);
                    setShown(PAGE);
                  }}
                />
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    setAsked(q.trim());
                    setShown(PAGE);
                  }}
                  className="relative w-56"
                >
                  <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
                  <input
                    value={q}
                    onChange={(e) => setQ(e.target.value)}
                    placeholder="Ticker or company…"
                    aria-label="Find a stock"
                    className="h-[32px] w-full rounded-md border border-border-subtle bg-bg-field pl-8 pr-8 text-meta outline-none focus:border-brand"
                  />
                  {q && (
                    <button
                      type="button"
                      aria-label="Clear"
                      onClick={() => {
                        setQ("");
                        setAsked("");
                      }}
                      className="absolute right-2 top-1/2 -translate-y-1/2 text-text-muted hover:text-text-primary"
                    >
                      <X size={13} />
                    </button>
                  )}
                </form>
              </div>
              <ForecastTable rows={data.rows ?? []} onOpen={open} />
              <div className="flex items-center gap-3 border-t border-border-subtle px-5 py-3 text-meta text-text-muted">
                <span>
                  {Math.min(shown, data.total).toLocaleString()} of {data.total.toLocaleString()} stocks
                </span>
                {shown < data.total && (
                  <button type="button" className="action-secondary" disabled={isFetching} onClick={() => setShown((n) => n + PAGE)}>
                    {isFetching ? "Loading…" : "Show more"}
                  </button>
                )}
              </div>
            </section>

            <Factors rep={rep} />
            <Method rep={rep} />
          </div>
        ) : (
          <p className="px-8 py-10 text-ui text-text-secondary">The engine's first run is still to come. It runs after each close.</p>
        )}
      </div>
    </div>
  );
}

function MarketNow({ rep }: { rep: ForecastReport }) {
  const g = rep.regime;
  const m20 = rep.market?.find((h) => h.h === 20);
  const stressed = g.stress_prob >= 0.5;
  return (
    <section className="rounded-xl border border-border-subtle bg-bg-card px-5 py-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-[15px] font-semibold text-text-primary">The market now</h2>
        <span className="text-micro text-text-muted">As of the close on {formatDate(rep.as_of)}</span>
      </div>
      <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
        <div>
          <dt className="text-meta text-text-muted">Regime</dt>
          <dd className="mt-0.5 flex items-center gap-2">
            <span className={"rounded-full px-2 py-px text-micro font-semibold " + (stressed ? "bg-semantic-down-soft text-semantic-down" : "bg-semantic-up-soft text-semantic-up")}>
              {stressed ? "Stressed" : "Calm"}
            </span>
            <span className="font-num text-[18px] font-semibold text-text-primary">{prob(g.stress_prob)}</span>
          </dd>
          <dd className="text-micro text-text-muted">
            chance of the stressed state, where the S&P 500 swings about {g.stress_vol_pct.toFixed(0)}% a year against {g.calm_vol_pct.toFixed(0)}%
          </dd>
        </div>
        <div>
          <dt className="text-meta text-text-muted">VIX</dt>
          <dd className="mt-0.5 font-num text-[18px] font-semibold text-text-primary">{g.vix ? g.vix.toFixed(1) : "—"}</dd>
          <dd className="text-micro text-text-muted">the options market's 30-day volatility</dd>
        </div>
        <div>
          <dt className="text-meta text-text-muted">S&P 500 volatility, next month</dt>
          <dd className="mt-0.5 font-num text-[18px] font-semibold text-text-primary">{(g.vol_forecast_pct?.["20"] ?? 0).toFixed(1)}%</dd>
          <dd className="text-micro text-text-muted">annualised, against {g.realised_vol_pct.toFixed(1)}% over the last month</dd>
        </div>
        {m20 && (
          <div>
            <dt className="text-meta text-text-muted">S&P 500, next 20 sessions</dt>
            <dd className="mt-0.5 font-num text-[18px] font-semibold text-text-primary">
              {pct(m20.q[1]!)} to {pct(m20.q[17]!)}
            </dd>
            <dd className="text-micro text-text-muted">80% range; rises in {prob(m20.p_up)} of simulated paths</dd>
          </div>
        )}
      </dl>
    </section>
  );
}

function Tile({ term, value, note, tone }: { term: string; value: string; note: string; tone?: "good" | "none" }) {
  return (
    <div className="rounded-lg bg-bg-field px-4 py-3">
      <dt className="text-meta text-text-muted">{term}</dt>
      <dd className={"mt-0.5 font-num text-[20px] font-semibold " + (tone === "good" ? "text-semantic-up" : tone === "none" ? "text-text-secondary" : "text-text-primary")}>
        {value}
      </dd>
      <dd className="text-micro leading-snug text-text-muted">{note}</dd>
    </div>
  );
}

function Scorecard({ rep, live }: { rep: ForecastReport; live?: { runs: number; scored_runs?: number; in80_pct?: number; brier_beat?: number; brier_beat_base?: number } }) {
  const d5 = rep.dist?.["5"];
  const d20 = rep.dist?.["20"];
  const v5 = rep.vol?.["5"];
  const v20 = rep.vol?.["20"];
  const a5 = rep.alpha?.["5"];
  const a20 = rep.alpha?.["20"];
  if (!d5 || !d20 || !v5 || !v20 || !a5 || !a20) return null;
  const cov = (d: typeof d5) => (d.calibrated ? d.calibrated_coverage[1] : d.coverage[1]);
  const upEdge = d5.brier_up_calibrated < d5.brier_up_base_rate;
  const beatEdge = d5.brier_beat_calibrated < d5.brier_beat_base_rate;
  const rankReal = a20.t >= 2 && a20.ic > a20.momentum_ic;
  return (
    <section className="rounded-xl border border-border-subtle bg-bg-card px-5 py-4">
      <h2 className="text-[15px] font-semibold text-text-primary">Has it been right?</h2>
      <p className="mt-0.5 max-w-3xl text-meta text-text-muted">
        Every year {years(rep)} was forecast by models fitted only on the three years before it, then scored against what happened:{" "}
        {d5.n.toLocaleString()} five-session distributions for {rep.eval_stocks} stocks, and every stock's volatility and ranking.
      </p>
      <dl className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Tile
          term="Ranges hold what they claim"
          value={`${cov(d5).toFixed(0)}% · ${cov(d20).toFixed(0)}%`}
          note="of outcomes fell inside the 80% range, over 5 and 20 sessions"
          tone={Math.abs(cov(d5) - 80) <= 3 ? "good" : undefined}
        />
        <Tile
          term="Sharper than a bell curve"
          value={`${d5.skill_normal_pct >= 0 ? "+" : ""}${d5.skill_normal_pct.toFixed(1)}%`}
          note={`lower CRPS than a normal curve on 60-day volatility (t = ${d5.dm_normal_t.toFixed(1)}); ${d20.skill_history_pct >= 0 ? "+" : ""}${d20.skill_history_pct.toFixed(1)}% over 20 sessions against the stock's own past year`}
          tone={d5.dm_normal_t >= 2 ? "good" : undefined}
        />
        <Tile
          term="Volatility forecast"
          value={`${v5.skill_pct >= 0 ? "+" : ""}${v5.skill_pct.toFixed(0)}% · ${v20.skill_pct >= 0 ? "+" : ""}${v20.skill_pct.toFixed(0)}%`}
          note="less forecast error than assuming next week and next month look like the last (QLIKE)"
          tone={v5.skill_pct > 5 ? "good" : undefined}
        />
        <Tile
          term="Ranking the universe"
          value={`IC ${a20.ic.toFixed(3)}`}
          note={`over 20 sessions, t = ${a20.t.toFixed(1)}; momentum alone ${a20.momentum_ic.toFixed(3)}. ${rankReal ? "A small, real edge." : "Not yet a proven edge."}`}
          tone={rankReal ? "good" : "none"}
        />
      </dl>
      <p className="mt-4 max-w-3xl text-ui leading-relaxed text-text-secondary">
        The engine's skill is in the shape of the outcome, not its direction: how wide the range is, how fat its tails, and how much an earnings
        report widens it. {upEdge || beatEdge ? "Its probabilities of rising and of beating the S&P 500 have been slightly better than the base rate." : "Its probabilities of rising and of beating the S&P 500 have been no better than the base rate, so read them as close to a coin toss, and read the ranges as the forecast."}
      </p>
      {live && live.runs > 0 && (
        <p className="mt-2 text-meta text-text-muted">
          Live since launch: {live.runs} runs scored
          {live.scored_runs ? `; their 80% ranges held ${live.in80_pct?.toFixed(0)}% of outcomes` : ""}.
        </p>
      )}
      <div className="mt-5 grid gap-6 lg:grid-cols-[240px_280px_minmax(0,1fr)] lg:items-start">
        {rep.reliability?.["5"]?.beat?.length ? <ReliabilityChart bins={rep.reliability["5"].beat} label="Beating the S&P 500, 5 sessions" /> : null}
        {d5.pit?.length ? <PitHistogram pit={d5.pit} /> : null}
        <YearTable rep={rep} />
      </div>
    </section>
  );
}

function YearTable({ rep }: { rep: ForecastReport }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[460px] text-ui">
        <thead>
          <tr className="border-b border-border-subtle text-left text-meta text-text-muted">
            <th className="py-2 pr-3 font-medium">Year</th>
            <th className="py-2 pr-3 text-right font-medium" title="Share of five-session outcomes inside the 80% range">In 80%</th>
            <th className="py-2 pr-3 text-right font-medium" title="CRPS against a normal curve, five sessions">vs bell curve</th>
            <th className="py-2 pr-3 text-right font-medium" title="Volatility forecast error against last month's, five sessions">Volatility</th>
            <th className="py-2 text-right font-medium" title="Rank IC over 20 sessions, and momentum alone">Ranking IC</th>
          </tr>
        </thead>
        <tbody className="font-num tabular-nums">
          {(rep.years ?? []).map((y) => {
            const d = y.dist["5"];
            const v = y.vol["5"];
            const a = y.alpha["20"];
            return (
              <tr key={y.year} className="border-b border-border-subtle last:border-0">
                <td className="py-2 pr-3 font-sans text-text-primary">{y.year}</td>
                <td className="py-2 pr-3 text-right text-text-secondary">{d ? `${(d.calibrated ? d.calibrated_coverage[1] : d.coverage[1]).toFixed(0)}%` : "—"}</td>
                <td className={"py-2 pr-3 text-right " + (d && d.skill_normal_pct > 0 ? "text-semantic-up" : "text-text-secondary")}>
                  {d ? `${d.skill_normal_pct >= 0 ? "+" : ""}${d.skill_normal_pct.toFixed(1)}%` : "—"}
                </td>
                <td className="py-2 pr-3 text-right text-text-secondary">{v ? `${v.skill_pct >= 0 ? "+" : ""}${v.skill_pct.toFixed(0)}%` : "—"}</td>
                <td className="py-2 text-right text-text-secondary">{a ? `${a.ic.toFixed(3)} / ${a.momentum_ic.toFixed(3)}` : "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function ForecastTable({ rows, onOpen }: { rows: ForecastPick[]; onOpen: (s: string) => void }) {
  // One scale for every row's range bar, wide enough for most of them.
  const spans = rows.map((r) => r.dist?.horizons.find((h) => h.h === 5)).filter(Boolean).flatMap((h) => [Math.abs(h!.q[1]!), Math.abs(h!.q[17]!)]);
  spans.sort((a, b) => a - b);
  const domain = Math.max(0.05, spans[Math.floor(spans.length * 0.9)] ?? 0.08);
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[880px] text-ui">
        <thead>
          <tr className="border-y border-border-subtle text-left text-meta text-text-muted">
            <th className="px-5 py-2 font-medium">Stock</th>
            <th className="px-3 py-2 text-right font-medium" title="Chance of beating the S&P 500 over five sessions">Beat S&P, 5d</th>
            <th className="px-3 py-2 font-medium" title={`80% range of the five-session return; the scale runs from ${pct(-domain, 0)} to ${pct(domain, 0)}`}>
              Range, 5 sessions
            </th>
            <th className="px-3 py-2 text-right font-medium" title="Average of the worst 5% of simulated five-session outcomes">Worst 5%</th>
            <th className="px-3 py-2 text-right font-medium" title="Volatility forecast for the next month against the stock's past year">Volatility</th>
            <th className="px-3 py-2 font-medium">Earnings</th>
            <th className="px-5 py-2 font-medium">Leaning on</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((p) => {
            const h5 = p.dist?.horizons.find((h) => h.h === 5);
            const d = p.dist;
            const volRatio = d && d.normal_vol_pct ? d.vol_pct / d.normal_vol_pct : 1;
            return (
              <tr key={p.symbol} className="border-b border-border-subtle align-middle last:border-0 hover:bg-bg-field">
                <td className="px-5 py-2">
                  <button type="button" onClick={() => onOpen(p.symbol)} className="font-medium text-text-primary hover:text-brand">
                    {p.symbol}
                  </button>
                  <span className="block max-w-[200px] truncate text-micro text-text-muted">{p.name}</span>
                </td>
                <td className="px-3 py-2 text-right font-num text-[12.5px] text-text-primary">{h5 ? prob(h5.p_beat) : "—"}</td>
                <td className="px-3 py-2">
                  {h5 ? (
                    <div className="flex items-center gap-2">
                      <RangeBar q={h5.q} domain={domain} />
                      <span className="w-[92px] shrink-0 font-num text-micro text-text-muted">
                        {pct(h5.q[1]!, 1)} {pct(h5.q[17]!, 1)}
                      </span>
                    </div>
                  ) : (
                    "—"
                  )}
                </td>
                <td className="px-3 py-2 text-right font-num text-[12.5px] text-text-secondary">{h5 ? pct(h5.es5, 1) : "—"}</td>
                <td className="px-3 py-2 text-right font-num text-[12px] text-text-secondary" title={d ? `${d.vol_pct.toFixed(0)}% forecast against ${d.normal_vol_pct.toFixed(0)}% over the past year` : undefined}>
                  {d ? `${d.vol_pct.toFixed(0)}%` : "—"}
                  {d && Math.abs(volRatio - 1) >= 0.15 && <span className="ml-1 text-micro text-text-muted">{volRatio > 1 ? "↑" : "↓"}</span>}
                </td>
                <td className="px-3 py-2 text-meta text-text-secondary">
                  {d?.earnings ? `in ${d.earnings.session} · ±${d.earnings.typical_move_pct.toFixed(1)}%` : <span className="text-text-muted">—</span>}
                </td>
                <td className="px-5 py-2">
                  <span className="flex flex-wrap gap-1">
                    {p.drivers.slice(0, 2).map((dr) => (
                      <span
                        key={dr.key}
                        className={"rounded-full px-2 py-px text-micro " + (dr.contribution >= 0 ? "bg-semantic-up-soft text-semantic-up" : "bg-semantic-down-soft text-semantic-down")}
                      >
                        {dr.label}
                      </span>
                    ))}
                  </span>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function Factors({ rep }: { rep: ForecastReport }) {
  const list = [...(rep.factors ?? [])].sort((a, b) => (b.t ?? -99) - (a.t ?? -99));
  return (
    <section className="rounded-xl border border-border-subtle bg-bg-card px-5 py-4">
      <h2 className="text-[15px] font-semibold text-text-primary">What the ranking reads</h2>
      <p className="mt-0.5 max-w-3xl text-meta text-text-muted">
        Published, replicated factors, ranked across the universe each week. Shown first: each factor's own rank correlation with the next 20
        sessions' return against the market, out of sample over {years(rep)}, and its t statistic; few clear |t| = 2 at this horizon in large US
        stocks, which is why the ranking's expected returns are shrunk close to zero. Then the linear model's weight and the trees' share.
      </p>
      <ul className="mt-3 divide-y divide-border-subtle">
        {list.map((f) => (
          <li key={f.key} className="grid gap-1 py-2.5 sm:grid-cols-[minmax(0,1.3fr)_120px_80px_120px_minmax(0,2fr)] sm:items-center sm:gap-3">
            <span className="text-ui text-text-primary">
              {f.label}
              {f.regime && <span className="ml-1.5 rounded-full bg-bg-chip px-1.5 py-px text-micro text-text-muted">market</span>}
            </span>
            <span className={"font-num text-meta " + (f.t != null && Math.abs(f.t) >= 2 ? "text-text-primary" : "text-text-muted")}>
              {f.ic != null && !f.regime ? `IC ${(f.ic >= 0 ? "+" : "−") + Math.abs(f.ic).toFixed(3)} · t ${f.t?.toFixed(1)}` : "—"}
            </span>
            <span className="font-num text-meta text-text-secondary">{f.regime ? "—" : (f.ridge_weight >= 0 ? "+" : "−") + Math.abs(f.ridge_weight).toFixed(3)}</span>
            <span className="flex items-center gap-2">
              <span className="h-1.5 w-16 overflow-hidden rounded-full bg-bg-chip">
                <span className="block h-full rounded-full bg-brand" style={{ width: `${Math.min(100, f.tree_share_pct * 4)}%` }} />
              </span>
              <span className="font-num text-micro text-text-muted">{f.tree_share_pct.toFixed(1)}%</span>
            </span>
            <span className="text-meta text-text-muted">{f.why}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Method({ rep }: { rep: ForecastReport }) {
  return (
    <section className="max-w-3xl text-meta leading-relaxed text-text-muted">
      <h2 className="text-ui font-semibold text-text-secondary">How each forecast is made</h2>
      <ul className="mt-1 list-disc space-y-1 pl-5">
        <li>
          Volatility from two models, blended by which forecast better in earlier years: HAR on each stock's daily range (Corsi, 2009), with the
          market's volatility and VIX, and GJR-GARCH with fat-tailed errors.
        </li>
        <li>The market moves through a calm and a stressed regime, estimated by a hidden Markov model on the S&P 500.</li>
        <li>
          Each stock's {rep.paths.toLocaleString()} simulated paths redraw its own past shocks, rescaled to today's volatility, so its skew and fat
          tails carry through; a report inside the window adds a jump drawn from its own past earnings days.
        </li>
        <li>
          The expected return comes from the ranking, scaled by how much such rankings actually earned in earlier years, and shrunk to nothing
          when that evidence is weak.
        </li>
        <li>Ranges and probabilities are recalibrated on earlier years' misses before they are shown.</li>
        <li>
          Prices are today's index members traced back, so stocks that were dropped or failed are missing from the history; that flatters past
          returns more than it does volatility.
        </li>
      </ul>
    </section>
  );
}
