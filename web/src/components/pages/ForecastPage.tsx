import { useQuery } from "@tanstack/react-query";
import { Search, Sparkles, X } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, forecastApi, type ForecastPick } from "../../lib/api";
import { formatDate } from "../../lib/format";
import { PageHeader, SkeletonRows } from "../ui/controls";

const signed = (n: number | undefined, d = 2) => (n === undefined ? "—" : `${n > 0 ? "+" : n < 0 ? "−" : ""}${Math.abs(n).toFixed(d)}`);

/** How confident to be in the model, in words, from its out-of-sample record. */
function judge(ic: number, t: number) {
  if (t >= 3 && ic >= 0.01) return { tone: "text-semantic-up", text: "A small but statistically real edge, out of sample. Small is normal: the best published models of this kind score 0.03 to 0.05." };
  if (t >= 2 && ic > 0) return { tone: "text-text-primary", text: "Some evidence of an edge, but not enough to lean on." };
  return { tone: "text-semantic-down", text: "No reliable edge out of sample. Treat the ranking as a description, not a prediction." };
}

/**
 * The forecast model: which stocks are most likely to beat the rest over the
 * next few sessions, and — before anything else — how well that has worked
 * on years it was never fit to.
 */
export function ForecastPage({ onSelect }: { onSelect: (s: string) => void }) {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [asked, setAsked] = useState("");
  const { data, isLoading, error } = useQuery({ queryKey: ["forecast", asked], queryFn: () => forecastApi.latest(asked, 30), staleTime: 10 * 60_000 });
  const open = (s: string) => {
    onSelect(s);
    navigate("/charts");
  };
  const rep = data?.report;
  const verdict = rep ? judge(rep.overall.rank_ic, rep.overall.t) : null;

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="Forecast"
        subtitle={`Every stock in the universe ranked by how likely it is to beat the others over the next ${rep?.horizon ?? 5} sessions — from momentum, reversal, volatility, the 52-week high, earnings surprises and insider buying.`}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <SkeletonRows count={8} height={56} />
        ) : error ? (
          <p className="px-8 py-10 text-ui text-text-secondary">
            {error instanceof ApiError && error.status === 404 ? "The model has not run yet. It runs after each close." : (error as Error).message}
          </p>
        ) : rep && data ? (
          <div className="flex flex-col gap-6 px-5 py-6 md:px-8">
            <section className="rounded-xl border border-border-subtle bg-bg-card px-5 py-4">
              <h2 className="text-[15px] font-semibold text-text-primary">Does it work?</h2>
              <p className="mt-0.5 text-meta text-text-muted">
                Each year below was scored by a model fit only on the three years before it, with a gap so no answer leaks in. Scored as of{" "}
                {formatDate(rep.as_of)} over {rep.symbols.toLocaleString()} stocks.
              </p>
              <dl className="mt-4 grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
                <Stat term="Rank IC, out of sample" value={signed(rep.overall.rank_ic, 3)} note={`t = ${rep.overall.t.toFixed(1)} over ${rep.overall.days} sessions`} />
                <Stat term="Top tenth vs bottom tenth" value={`${signed(rep.overall.spread_pct)}%`} note={`per ${rep.horizon} sessions, on average`} />
                <Stat term="Top tenth beat the average" value={`${rep.overall.top_hit_pct.toFixed(0)}%`} note="of sessions" />
                <Stat
                  term="Live since launch"
                  value={data.live_summary?.runs ? signed(data.live_summary?.rank_ic, 3) : "—"}
                  note={data.live_summary?.runs ? `rank IC over ${data.live_summary?.runs} scored runs` : "first scores after five sessions"}
                />
              </dl>
              {verdict && <p className={"mt-4 text-ui " + verdict.tone}>{verdict.text}</p>}
              {(rep.years ?? []).length > 0 && (
                <div className="mt-4 overflow-x-auto">
                  <table className="w-full min-w-[520px] text-ui">
                    <thead>
                      <tr className="border-b border-border-subtle text-left text-meta text-text-muted">
                        <th className="py-2 pr-3 font-medium">Year</th>
                        <th className="py-2 pr-3 text-right font-medium">Sessions</th>
                        <th className="py-2 pr-3 text-right font-medium">Rank IC</th>
                        <th className="py-2 pr-3 text-right font-medium">t</th>
                        <th className="py-2 pr-3 text-right font-medium">Top − bottom</th>
                        <th className="py-2 text-right font-medium">Top beat avg</th>
                      </tr>
                    </thead>
                    <tbody className="font-num tabular-nums">
                      {(rep.years ?? []).map((y) => (
                        <tr key={y.year} className="border-b border-border-subtle last:border-0">
                          <td className="py-2 pr-3 font-sans text-text-primary">{y.year}</td>
                          <td className="py-2 pr-3 text-right text-text-secondary">{y.days}</td>
                          <td className={"py-2 pr-3 text-right " + (y.rank_ic > 0 ? "text-semantic-up" : "text-semantic-down")}>{signed(y.rank_ic, 3)}</td>
                          <td className="py-2 pr-3 text-right text-text-secondary">{y.t.toFixed(1)}</td>
                          <td className="py-2 pr-3 text-right">{signed(y.spread_pct)}%</td>
                          <td className="py-2 text-right text-text-secondary">{y.top_hit_pct.toFixed(0)}%</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>

            <section className="rounded-xl border border-border-subtle bg-bg-card px-5 py-4">
              <h2 className="text-[15px] font-semibold text-text-primary">What the model reads</h2>
              <p className="mt-0.5 text-meta text-text-muted">
                Each factor's own out-of-sample rank IC, and the weight the current model gives it. Weights can differ in sign from a factor's own IC when
                factors overlap.
              </p>
              <ul className="mt-3 divide-y divide-border-subtle">
                {(rep.factors ?? []).map((f) => (
                  <li key={f.key} className="grid gap-1 py-2.5 sm:grid-cols-[minmax(0,1.2fr)_90px_90px_minmax(0,2fr)] sm:items-center sm:gap-3">
                    <span className="text-ui text-text-primary">{f.label}</span>
                    <span className="font-num text-meta text-text-secondary">IC {signed(f.rank_ic, 3)}</span>
                    <span className="font-num text-meta text-text-secondary">w {signed(f.weight, 3)}</span>
                    <span className="text-meta text-text-muted">{f.why}</span>
                  </li>
                ))}
              </ul>
            </section>

            <div className="flex flex-wrap items-center gap-2">
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  setAsked(q.trim());
                }}
                className="relative w-64"
              >
                <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
                <input
                  value={q}
                  onChange={(e) => setQ(e.target.value.toUpperCase())}
                  placeholder="Find a ticker…"
                  aria-label="Find a ticker"
                  className="h-[34px] w-full rounded-md border border-border-subtle bg-bg-field pl-8 pr-8 text-meta outline-none focus:border-brand"
                />
                {q && (
                  <button type="button" aria-label="Clear" onClick={() => { setQ(""); setAsked(""); }} className="absolute right-2 top-1/2 -translate-y-1/2 text-text-muted hover:text-text-primary">
                    <X size={13} />
                  </button>
                )}
              </form>
              <span className="text-meta text-text-muted">{(data.total ?? 0).toLocaleString()} stocks ranked</span>
            </div>

            <div className="grid gap-5 lg:grid-cols-2">
              <PickList title="Most likely to outperform" picks={data.top ?? []} onOpen={open} />
              <PickList title="Most likely to underperform" picks={data.bottom ?? []} onOpen={open} />
            </div>
            <p className="max-w-3xl text-meta leading-relaxed text-text-muted">
              <Sparkles size={12} className="mr-1 inline" />
              A ranking, not a price target: it says which stocks the evidence favours relative to the rest, and research on models like this finds the
              edge concentrated in the top of the list. The model's strongest picks also get an AI outlook each morning, scored on the AI track record.
            </p>
          </div>
        ) : null}
      </div>
    </div>
  );
}

function Stat({ term, value, note }: { term: string; value: string; note: string }) {
  return (
    <div>
      <dt className="text-meta text-text-muted">{term}</dt>
      <dd className="mt-0.5 font-num text-[22px] font-semibold tabular-nums text-text-primary">{value}</dd>
      <dd className="text-micro text-text-muted">{note}</dd>
    </div>
  );
}

function PickList({ title, picks, onOpen }: { title: string; picks: ForecastPick[]; onOpen: (s: string) => void }) {
  return (
    <section className="rounded-xl border border-border-subtle bg-bg-card">
      <h2 className="px-5 pb-2 pt-4 text-[15px] font-semibold text-text-primary">{title}</h2>
      <ul className="divide-y divide-border-subtle border-t border-border-subtle">
        {picks.map((p) => (
          <li key={p.symbol} className="px-5 py-2.5">
            <div className="flex items-baseline gap-2">
              <button type="button" onClick={() => onOpen(p.symbol)} className="font-medium text-text-primary hover:text-brand">
                {p.symbol}
              </button>
              <span className="min-w-0 flex-1 truncate text-meta text-text-muted">
                {p.name}
                {p.sector ? ` · ${p.sector}` : ""}
              </span>
              <span className="font-num text-meta text-text-secondary">{p.percentile.toFixed(0)}th pct</span>
            </div>
            <p className="mt-1 flex flex-wrap gap-1.5">
              {p.drivers.map((d) => (
                <span key={d.key} className={"rounded-full px-2 py-px text-micro " + (d.contribution >= 0 ? "bg-semantic-up-soft text-semantic-up" : "bg-semantic-down-soft text-semantic-down")}>
                  {d.label}
                </span>
              ))}
            </p>
          </li>
        ))}
      </ul>
    </section>
  );
}
