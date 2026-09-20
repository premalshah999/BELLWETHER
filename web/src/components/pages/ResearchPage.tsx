import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowUp,
  Search,
  FileText,
  Sparkles,
  PanelLeft,
  Plus,
  RotateCw,
  Trash2,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, type MarketStats, type ResearchTurn } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { useResize, usePersisted } from "../../lib/layout";
import { Divider } from "../ui/Divider";
import { Panel } from "../ui/Panel";

const SUGGESTIONS = [
  "What changed in NVIDIA’s earnings and export restrictions?",
  "Which Indian cement companies are exposed to the coal price?",
  "What do recent central bank releases say about inflation?",
  "How is the tariff dispute affecting Indian steel exporters?",
];

export function ResearchPage() {
  const qc = useQueryClient();
  const [active, setActive] = usePersisted<number | null>(
    "research.active",
    null,
  );
  const [mode, setMode] = usePersisted<"evidence" | "brief">(
    "research.mode",
    "evidence",
  );
  const [showThreads, setShowThreads] = useState(false);
  const { data: coverage } = useQuery({
    queryKey: ["research-scrapers"],
    queryFn: api.researchScrapers,
    staleTime: 60_000,
  });
  const [question, setQuestion] = useState("");
  /** A question posted but not yet visible in the thread. */
  const [pending, setPending] = useState<string | null>(null);
  const bottom = useRef<HTMLDivElement>(null);
  const threadsPane = useResize({
    key: "research.threads",
    initial: 256,
    min: 180,
    max: 460,
    direction: "e",
  });

  const { data: threads } = useQuery({
    queryKey: ["conversations"],
    queryFn: api.conversations,
    refetchInterval: 30_000,
  });

  const {
    data: thread,
    isLoading: threadLoading,
    error: threadError,
  } = useQuery({
    queryKey: ["conversation", active],
    queryFn: () => api.conversation(active!),
    enabled: active != null,
    // Polls quickly only while a turn is running: a research turn takes two
    // to three minutes and the stages are worth watching.
    refetchInterval: (q) => {
      const t = q.state.data?.turns?.at(-1);
      // Also poll fast while a question has been posted but the thread has
      // not yet come back carrying it: between those two moments the newest
      // turn still reads "done", and the slow interval left the operator
      // watching an unchanged screen for up to a minute after pressing enter.
      if (pending) return 1_000;
      return t && t.status === "running" ? 2_000 : 60_000;
    },
  });

  const ask = useMutation({
    mutationFn: (text: string) => api.ask(text, active ?? undefined, 12, mode),
    onMutate: (text) => {
      // The question appears the instant it is sent. A research turn runs for
      // one to two minutes, and without this the screen was identical before
      // and after pressing enter — which reads as the app having ignored you.
      setPending(text);
      setQuestion("");
    },
    onSuccess: (res) => {
      setActive(res.conversation_id);
      // The thread itself, not just the list of threads. Invalidating only
      // the list left the open conversation on its slow poll, so the running
      // turn did not appear until the next sixty-second refetch.
      qc.invalidateQueries({ queryKey: ["conversation", res.conversation_id] });
      qc.invalidateQueries({ queryKey: ["conversations"] });
    },
    onError: (_error, text) => {
      setPending(null);
      setQuestion(text);
    },
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.deleteConversation(id),
    onSuccess: () => {
      setActive(null);
      qc.invalidateQueries({ queryKey: ["conversations"] });
    },
  });

  const turns = thread?.turns ?? [];
  const busy =
    ask.isPending ||
    pending != null ||
    turns.some((t) => t.status === "running");
  const submit = (text: string) => {
    if (text.trim() && !busy) ask.mutate(text.trim());
  };

  // The optimistic question is cleared once the thread comes back carrying a
  // turn that asks it, so the two never render at the same time.
  useEffect(() => {
    if (pending && !ask.isPending && turns.at(-1)?.question === pending)
      setPending(null);
  }, [turns, pending, ask.isPending]);

  useEffect(() => {
    if (turns.length > 0 || pending)
      bottom.current?.scrollIntoView({ behavior: "smooth" });
  }, [turns.length, pending]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b border-border-subtle bg-bg-panel px-5 py-4">
        <div className="flex items-center gap-3">
          <button
            type="button"
            aria-label="Toggle research history"
            aria-expanded={showThreads}
            onClick={() => setShowThreads(!showThreads)}
            className="p-2 text-text-muted hover:text-brand"
          >
            <PanelLeft size={16} />
          </button>
          <div>
            <h1 className="text-emphasis font-semibold text-text-primary">
              Research desk
            </h1>
            <p className="mt-1 text-meta text-text-muted">
              Sources first. A clearer view of what changed.
            </p>
          </div>
        </div>
        <button
          type="button"
          disabled={ask.isPending}
          onClick={() => {
            setActive(null);
            setPending(null);
            ask.reset();
          }}
          className="flex items-center gap-2 border border-border-focus px-3 py-2 text-meta text-text-secondary hover:border-brand hover:text-brand"
        >
          <Plus size={13} /> New research
        </button>
      </header>
      <div className="relative flex min-h-0 flex-1">
        {showThreads && (
          <>
            <Panel
              title="Threads"
              collapseKey="research.threads"
              scroll
              className="absolute bottom-0 left-0 top-0 z-20 max-w-[85%] shrink-0 border-r border-border-subtle bg-bg-panel md:relative"
              style={{ width: threadsPane.size }}
              action={
                <button
                  type="button"
                  onClick={() => setActive(null)}
                  className="text-text-muted hover:text-brand"
                  title="New thread"
                >
                  <Plus size={13} />
                </button>
              }
            >
              <div className="divide-y divide-border-subtle">
                {(threads?.conversations ?? []).map((c) => (
                  <div
                    key={c.id}
                    className={
                      "group flex items-start gap-1 px-3.5 py-2.5 " +
                      (c.id === active
                        ? "bg-brand-muted"
                        : "hover:bg-bg-panel-hover")
                    }
                  >
                    <button
                      type="button"
                      onClick={() => {
                        setActive(c.id);
                        setShowThreads(false);
                      }}
                      className="min-w-0 flex-1 text-left"
                    >
                      <span className="block truncate text-meta leading-snug text-text-primary">
                        {c.title}
                      </span>
                      <span className="mt-0.5 block font-mono text-micro text-text-muted">
                        {formatAgo(c.updated_at)} · {c.turn_count}q
                      </span>
                    </button>
                    <button
                      type="button"
                      onClick={() => remove.mutate(c.id)}
                      className="opacity-0 transition-opacity group-hover:opacity-100"
                      title="Delete thread"
                    >
                      <Trash2
                        size={11}
                        className="text-text-muted hover:text-semantic-down"
                      />
                    </button>
                  </div>
                ))}
              </div>
            </Panel>

            <Divider resize={threadsPane} orientation="vertical" />
          </>
        )}

        <div className="flex min-w-0 flex-1 flex-col bg-bg-panel">
          <div className="min-h-0 flex-1 overflow-y-auto">
            {threadError && (
              <p role="alert" className="p-4 text-meta text-semantic-down">
                Could not load this thread. {threadError.message}
              </p>
            )}
            {threadLoading && (
              <p className="p-4 text-meta text-text-muted">Loading research…</p>
            )}
            {turns.length === 0 && !pending && !threadLoading ? (
              <div className="mx-auto max-w-3xl px-6 py-12 lg:py-20">
                <span className="mb-5 inline-flex items-center gap-2 border border-brand/30 bg-brand-muted px-3 py-1.5 font-mono text-micro uppercase tracking-wider text-brand">
                  <Search size={12} /> Evidence workspace
                </span>
                <h2 className="max-w-xl text-hero font-semibold leading-tight tracking-tight text-text-primary">
                  Follow the question.
                  <br />
                  Check the evidence.
                </h2>
                <p className="mt-4 max-w-xl text-ui leading-relaxed text-text-secondary">
                  Investigate a company, a catalyst, or a policy change. Search
                  filings, official releases, the collected archive, and the
                  web. See which documents were actually readable before drawing
                  a conclusion.
                </p>
                <div className="mt-7 grid gap-3 sm:grid-cols-2">
                  {SUGGESTIONS.map((suggestion) => (
                    <button
                      key={suggestion}
                      type="button"
                      disabled={busy}
                      onClick={() => setQuestion(suggestion)}
                      className="flex items-start gap-3 border border-border-subtle bg-bg-base p-4 text-left text-ui leading-relaxed text-text-secondary hover:border-border-focus hover:text-text-primary"
                    >
                      <FileText
                        size={15}
                        className="mt-1 shrink-0 text-brand"
                      />
                      {suggestion}
                    </button>
                  ))}
                </div>
                <p className="mt-5 font-mono text-meta text-text-muted">
                  {coverage
                    ? `${coverage.scrapers.length} configured search providers`
                    : "Loading source coverage…"}{" "}
                  · Availability is checked during each search
                </p>
              </div>
            ) : (
              <div className="mx-auto max-w-4xl divide-y divide-border-subtle">
                {turns.map((t) => (
                  <Turn key={t.id} turn={t} onRetry={submit} />
                ))}
                {pending && <PendingTurn question={pending} />}
              </div>
            )}
            <div ref={bottom} />
          </div>

          <form
            onSubmit={(e) => {
              e.preventDefault();
              submit(question);
            }}
            className="shrink-0 border-t border-border-subtle bg-bg-base px-5 py-4"
          >
            <div className="mx-auto max-w-4xl">
              <div className="mb-3 flex flex-wrap items-center gap-2">
                {(["evidence", "brief"] as const).map((value) => (
                  <button
                    key={value}
                    type="button"
                    aria-pressed={mode === value}
                    disabled={busy}
                    onClick={() => setMode(value)}
                    className={
                      "flex items-center gap-2 border px-3 py-1.5 text-meta " +
                      (mode === value
                        ? "border-brand/40 bg-brand-muted text-brand"
                        : "border-border-subtle text-text-muted hover:text-text-primary")
                    }
                  >
                    {value === "evidence" ? (
                      <FileText size={12} />
                    ) : (
                      <Sparkles size={12} />
                    )}
                    {value === "evidence"
                      ? "Evidence only · no AI"
                      : "Evidence + AI brief"}
                  </button>
                ))}
              </div>
              {ask.isError && (
                <p role="alert" className="mb-3 text-ui text-semantic-down">
                  {ask.error.message}
                </p>
              )}
              <div className="flex items-center gap-2">
                <input
                  aria-label="Research question"
                  maxLength={500}
                  value={question}
                  onChange={(e) => setQuestion(e.target.value)}
                  placeholder={
                    turns.length
                      ? "follow up…"
                      : "ask about a company, sector, or event…"
                  }
                  className="min-w-0 flex-1 border border-border-subtle bg-bg-base px-2 py-1.5 text-ui outline-none focus:border-brand"
                />
                <button
                  type="submit"
                  aria-label="Start research"
                  disabled={!question.trim() || busy}
                  className="border border-brand bg-brand-muted px-2 py-1.5 text-brand transition-colors hover:bg-brand hover:text-bg-base disabled:opacity-40"
                >
                  {busy ? (
                    <RotateCw size={16} className="animate-spin" />
                  ) : (
                    <ArrowUp size={16} />
                  )}
                </button>
              </div>
              <p className="mt-2 text-meta text-text-muted">
                {busy
                  ? "Research continues if you leave this page. You can return to it from history."
                  : mode === "evidence"
                    ? "Retrieves sources and market measurements without spending AI tokens."
                    : "One brief from selected readable passages. Source links remain available if AI is unavailable."}
              </p>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}

/**
 * The citation markers on a claim.
 *
 * The numbers refer to the source list at the foot of the answer, and clicking
 * one scrolls to it. Without these the report asserts things with no way to
 * check them — which is the difference between research and an opinion, and
 * the numbers were being computed and then thrown away.
 */
function Cite({ sources, turnId }: { sources?: number[]; turnId: number }) {
  if (!sources?.length) return null;
  return (
    <span className="ml-1 inline-flex gap-0.5 align-baseline">
      {sources.map((n) => (
        <a
          key={n}
          href={`#src-${turnId}-${n}`}
          onClick={(e) => {
            e.preventDefault();
            const target = document.getElementById(`src-${turnId}-${n}`);
            const details = target?.closest("details");
            if (details) details.open = true;
            target?.scrollIntoView({ behavior: "smooth", block: "center" });
          }}
          className="font-mono text-micro text-text-muted transition-colors hover:text-brand"
          title={`Source ${n}`}
        >
          [{n}]
        </a>
      ))}
    </span>
  );
}

/**
 * Everything the answer was built from.
 *
 * Collapsed by default because eighty entries would bury the report, but never
 * absent: a reader who wants to check a claim needs the link, the publisher and
 * whether the article was actually read or only its headline retrieved. That
 * last distinction matters enough to show — a source that was read is far
 * stronger evidence than one that was merely listed.
 */
function Sources({ turn }: { turn: ResearchTurn }) {
  const sources = turn.sources ?? [];
  if (!sources.length) return null;
  const read = sources.filter((source) => (source.words ?? 0) > 0).length;
  return (
    <details
      open={!turn.model || undefined}
      className="mt-5 border border-border-subtle bg-bg-base"
    >
      <summary className="cursor-pointer px-4 py-3 font-mono text-meta text-text-secondary">
        Evidence library · {sources.length} documents · {read} readable ·{" "}
        {sources.filter((source) => source.trust === 100).length} official
      </summary>
      <ol className="divide-y divide-border-subtle border-t border-border-subtle">
        {sources.map((source, i) => (
          <li
            key={source.url + i}
            id={`src-${turn.id}-${i + 1}`}
            className="flex scroll-mt-8 gap-3 px-4 py-3"
          >
            <span className="mt-0.5 w-6 shrink-0 font-mono text-meta text-brand">
              {i + 1}
            </span>
            <div className="min-w-0 flex-1">
              <a
                href={/^https?:\/\//i.test(source.url) ? source.url : undefined}
                target="_blank"
                rel="noreferrer noopener"
                className="text-ui leading-relaxed text-text-primary hover:text-brand"
              >
                {source.title}
              </a>
              <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 font-mono text-micro text-text-muted">
                <span>{source.publisher}</span>
                <span>
                  {source.published_at &&
                  !source.published_at.startsWith("0001")
                    ? formatAgo(source.published_at)
                    : "Publication date unknown"}
                </span>
                {source.trust === 100 && (
                  <span className="text-brand">Official source</span>
                )}
                <span className={source.words ? "text-semantic-up" : ""}>
                  {source.words
                    ? `${source.words} words extracted`
                    : "Link only · text unavailable"}
                </span>
                {source.cached && <span>Cached text</span>}
              </div>
              {source.read_error && (
                <p className="mt-1 text-meta text-text-muted">
                  {source.read_error}
                </p>
              )}
            </div>
          </li>
        ))}
      </ol>
    </details>
  );
}

/**
 * A question that has been sent but has not come back yet.
 *
 * Rendered in the same shape as a real turn so the thread does not visibly
 * reflow when the server's version replaces it. It exists purely so that
 * pressing enter changes the screen: the work behind it takes minutes, and an
 * interface that stays identical for the first several seconds is one the
 * operator will press enter on twice.
 */
function PendingTurn({ question }: { question: string }) {
  return (
    <article className="px-5 py-4">
      <h3 className="text-emphasis text-text-primary">{question}</h3>
      <p className="mt-2 flex items-center gap-2 font-mono text-meta text-text-muted">
        <span className="h-1.5 w-1.5 animate-pulse bg-brand" />
        sending…
      </p>
    </article>
  );
}

function Turn({
  turn,
  onRetry,
}: {
  turn: ResearchTurn;
  onRetry: (q: string) => void;
}) {
  const read = (turn.sources ?? []).filter((s) => (s.words ?? 0) > 0).length;
  return (
    <article className="px-5 py-4">
      <h3 className="text-ui font-medium leading-snug text-text-primary">
        {turn.question}
      </h3>
      <p className="mt-0.5 font-mono text-micro text-text-muted">
        {formatAgo(turn.created_at)}
        {turn.elapsed_ms > 0 && ` · ${(turn.elapsed_ms / 1000).toFixed(0)}s`}
        {turn.sources?.length
          ? ` · ${turn.sources.length} sources, ${read} with extracted text`
          : ""}
      </p>

      {turn.status === "running" && (
        <div role="status" className="mt-3 border-l-2 border-brand pl-3">
          <p className="font-mono text-meta text-brand">
            {turn.stage ?? "working"}…
          </p>
          {turn.progress?.slice(-3).map((step, i) => (
            <p key={i} className="mt-1 text-meta text-text-muted">
              {step.detail || step.stage}
            </p>
          ))}
        </div>
      )}

      {/* A research turn runs for two or three minutes, so it is exposed to
          every restart and every transient upstream failure in that window.
          Without a way back, a failed turn is a dead entry in the thread and
          the question has to be retyped. */}
      {turn.status === "failed" && (
        <div className="mt-2 flex flex-wrap items-center gap-3">
          <p className="font-mono text-meta text-semantic-down">
            {turn.error ===
            "the process handling this request stopped before it finished"
              ? "This search was interrupted — the server restarted while it was running."
              : (turn.error ?? "This search did not finish.")}
          </p>
          <button
            type="button"
            onClick={() => onRetry(turn.question)}
            className="flex items-center gap-1 border border-border-focus px-2 py-0.5 font-mono text-micro uppercase tracking-wider text-text-secondary transition-colors hover:border-brand hover:text-brand"
          >
            <RotateCw size={10} /> ask again
          </button>
        </div>
      )}

      {turn.note && (
        <p className="mt-2 border-l border-semantic-down pl-2 text-meta leading-relaxed text-text-muted">
          {turn.note}
        </p>
      )}

      {turn.model && (
        <p className="mt-2 font-mono text-micro text-brand">
          AI-generated brief · {turn.model} · verify the cited evidence
        </p>
      )}
      {!!turn.providers?.length && (
        <details className="mt-3 text-meta text-text-muted">
          <summary className="cursor-pointer">
            Source coverage ·{" "}
            {turn.providers.filter((provider) => provider.error).length}{" "}
            unavailable
          </summary>
          <ul className="mt-2 grid gap-1 sm:grid-cols-2">
            {turn.providers.map((provider) => (
              <li key={provider.name}>
                {provider.name}:{" "}
                {provider.error ? provider.error : `${provider.count} results`}
              </li>
            ))}
          </ul>
        </details>
      )}
      {!!turn.measurements?.length && <Measured stats={turn.measurements} />}

      {turn.answer && (
        <p className="mt-3 whitespace-pre-wrap text-ui leading-relaxed text-text-secondary">
          {turn.answer}
        </p>
      )}

      {(turn.sections ?? []).map((sec, i) => (
        <section key={i} className="mt-4">
          {sec.heading && (
            <h4 className="mb-1 text-ui font-medium text-text-primary">
              {sec.heading}
            </h4>
          )}
          {sec.body.split(/\n\n+/).map((p, j) => (
            <p
              key={j}
              className="mb-2 text-ui leading-relaxed text-text-secondary"
            >
              {p}
              {j === sec.body.split(/\n\n+/).length - 1 && (
                <Cite sources={sec.sources} turnId={turn.id} />
              )}
            </p>
          ))}
        </section>
      ))}

      {!!turn.findings?.length && (
        <ul className="mt-3 space-y-1 border-t border-border-subtle pt-3">
          {turn.findings.map((f, i) => (
            <li
              key={i}
              className="flex gap-2 text-ui leading-relaxed text-text-secondary"
            >
              <span className="mt-[7px] h-px w-2 shrink-0 bg-text-muted" />
              <span>
                {f.claim}
                <Cite sources={f.sources} turnId={turn.id} />
              </span>
            </li>
          ))}
        </ul>
      )}

      <Sources turn={turn} />
      {!!turn.followups?.length && (
        <div className="mt-4 flex flex-wrap gap-2">
          {turn.followups.map((query) => (
            <button
              type="button"
              key={query}
              onClick={() => onRetry(query)}
              className="border border-border-subtle px-3 py-2 text-left text-meta text-text-secondary hover:border-brand hover:text-brand"
            >
              {query} →
            </button>
          ))}
        </div>
      )}

      {!!turn.gaps?.length && (
        <div className="mt-3 border-t border-border-subtle pt-2">
          <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
            not established
          </p>
          <ul className="mt-1 space-y-0.5">
            {turn.gaps.map((g, i) => (
              <li key={i} className="text-meta leading-relaxed text-text-muted">
                {g}
              </li>
            ))}
          </ul>
        </div>
      )}
    </article>
  );
}

/**
 * Measured market data, set apart from the prose.
 *
 * Everything else in an answer is something a source claimed; this is
 * arithmetic over the price series. Conflating the two is how a reader ends up
 * trusting a headline's "surges" over a computed return.
 */
function Measured({ stats }: { stats: MarketStats[] }) {
  return (
    <div className="mt-3 border border-border-subtle bg-bg-base px-2.5 py-2">
      <p className="mb-1.5 font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
        measured from the price series
      </p>
      <div className="space-y-1.5">
        {stats.map((m) => (
          <div key={m.symbol} className="font-mono text-meta leading-relaxed">
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-text-primary">{m.symbol}</span>
              <span className="text-text-secondary">{m.close.toFixed(2)}</span>
              <span className="text-text-muted">
                {m.as_of.slice(0, 10)} · {m.bars} bars
              </span>
            </div>
            {!!m.returns?.length && (
              <div className="flex flex-wrap gap-x-2.5">
                {m.returns.map((r) => (
                  <span key={r.horizon}>
                    <span className="text-text-muted">{r.horizon}</span>{" "}
                    <span
                      className={
                        r.percent >= 0
                          ? "text-semantic-up"
                          : "text-semantic-down"
                      }
                    >
                      {r.percent >= 0 ? "+" : ""}
                      {r.percent.toFixed(2)}%
                    </span>
                  </span>
                ))}
              </div>
            )}
            <div className="text-text-muted">
              volatility {m.volatility_percent.toFixed(1)}% · worst drawdown{" "}
              {m.max_drawdown_percent.toFixed(1)}% ·{" "}
              {m.pct_from_52w_high.toFixed(1)}% from 52w high
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
