import { useMutation, useQuery } from "@tanstack/react-query";
import { Sparkles } from "lucide-react";
import { api } from "../lib/api";
import { formatDate, formatDateTime } from "../lib/format";
import { signalSentence, signed, toneOf } from "../lib/signals";
import { safeHref } from "../lib/url";

/**
 * What is known about one stock: the latest news the archive holds on it,
 * and two questions for the AI. "Explain" reads today's move against news
 * and the open web; "debrief" reads everything collected over the last
 * month, for a position nobody has looked at in a while.
 */
export function SymbolBrief({ symbol }: { symbol: string }) {
  const news = useQuery({
    queryKey: ["symbol-news", symbol],
    queryFn: () => api.events({ symbol, hours: 24 * 14, universe: "all", limit: 6 }),
    staleTime: 2 * 60_000,
  });
  const scans = useQuery({
    queryKey: ["scan-history", symbol],
    queryFn: () => api.scanHistory(symbol),
    staleTime: 10 * 60_000,
  });
  const explain = useMutation({ mutationFn: () => api.explainMove(symbol) });
  const debrief = useMutation({ mutationFn: () => api.debrief(symbol) });
  const busy = explain.isPending || debrief.isPending;
  const failed = explain.error ?? debrief.error;
  const events = news.data?.events ?? [];

  return (
    <section className="border-t border-border-subtle px-4 py-4" aria-label={`About ${symbol}`}>
      <h3 className="text-meta font-semibold text-text-secondary">Latest on {symbol}</h3>
      {events.length === 0 ? (
        <p className="mt-2 text-meta text-text-muted">{news.isLoading ? "Loading…" : "Nothing in the last two weeks."}</p>
      ) : (
        <ul className="mt-1 divide-y divide-border-subtle">
          {events.map((e) => (
            <li key={e.id} className="py-2">
              <a
                href={safeHref(e.primary_url)}
                target="_blank"
                rel="noopener noreferrer"
                className="text-[13px] font-medium leading-snug text-text-primary hover:text-accent-text hover:underline"
              >
                {e.headline}
              </a>
              <p className="mt-0.5 text-micro text-text-muted">{formatDateTime(e.published_at || e.discovered_at)}</p>
            </li>
          ))}
        </ul>
      )}

      {(scans.data?.findings ?? []).length > 0 && (
        <>
          <h3 className="mt-3 text-meta font-semibold text-text-secondary">Recent signals</h3>
          <ul className="mt-1 space-y-1">
            {(scans.data?.findings ?? []).map((f) => (
              <li key={f.id} className="flex items-baseline gap-2 text-meta text-text-secondary">
                <span className="w-12 shrink-0 text-micro text-text-muted">{formatDate(f.as_of, { month: "short", day: "numeric" })}</span>
                <span className={"w-12 shrink-0 text-right font-num " + toneOf(f.return_1d)}>{signed(f.return_1d)}%</span>
                <span className="min-w-0 flex-1">{signalSentence(f.signals)}</span>
              </li>
            ))}
          </ul>
        </>
      )}

      <div className="mt-3 flex flex-wrap gap-2">
        <button type="button" disabled={busy} onClick={() => { debrief.reset(); explain.mutate(); }} className="action-secondary">
          <Sparkles size={13} /> {explain.isPending ? "Reading…" : "Explain today's move"}
        </button>
        <button type="button" disabled={busy} onClick={() => { explain.reset(); debrief.mutate(); }} className="action-secondary">
          {debrief.isPending ? "Reading…" : "Debrief the last month"}
        </button>
      </div>
      {failed && (
        <p role="alert" className="mt-2 text-meta text-semantic-down">
          {failed instanceof Error ? failed.message : "The AI could not answer right now."}
        </p>
      )}

      {explain.data && (
        <div className="mt-3 rounded-lg bg-bg-field px-3 py-2.5">
          <p className="whitespace-pre-wrap text-meta leading-relaxed text-text-primary">{explain.data.explanation}</p>
          {explain.data.caveat && <p className="mt-1.5 text-micro text-text-muted">{explain.data.caveat}</p>}
          <ol className="mt-2 space-y-1 border-t border-border-subtle pt-2">
            {(explain.data.citations ?? []).filter((c) => c.used).map((c) => (
              <li key={c.index} className="text-micro">
                <a href={safeHref(c.url)} target="_blank" rel="noopener noreferrer" className="text-accent-text hover:underline">
                  [{c.index}] {c.title}
                </a>
              </li>
            ))}
          </ol>
          <p className="mt-2 text-micro text-text-muted">AI-written {formatDateTime(explain.data.generated_at)}. Not advice.</p>
        </div>
      )}

      {debrief.data && (
        <div className="mt-3 rounded-lg bg-bg-field px-3 py-2.5">
          <p className="text-[13.5px] font-semibold leading-snug text-text-primary">{debrief.data.headline}</p>
          <p className="mt-0.5 text-micro text-text-muted">
            {debrief.data.period}: {debrief.data.event_count} events, {debrief.data.official_count} official
          </p>
          {(debrief.data.sections ?? []).map((s) => (
            <div key={s.title} className="mt-2">
              <p className="text-micro font-semibold text-text-secondary">{s.title}</p>
              <p className="mt-0.5 text-meta leading-relaxed text-text-primary">{s.body}</p>
            </div>
          ))}
          {!!debrief.data.blind_spots?.length && (
            <p className="mt-2 border-t border-border-subtle pt-2 text-micro text-text-muted">
              Not covered: {debrief.data.blind_spots.join("; ")}
            </p>
          )}
          <p className="mt-2 text-micro text-text-muted">AI-written {formatDateTime(debrief.data.generated_at)}. Not advice.</p>
        </div>
      )}
    </section>
  );
}
