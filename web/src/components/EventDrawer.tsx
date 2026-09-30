import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDownRight, ArrowUpRight, ExternalLink, Link2, Minus, ShieldCheck, Sparkles, X } from "lucide-react";
import { useState } from "react";
import { api, type EventEntity, type Evidence, type MarketEvent } from "../lib/api";
import { formatAgo, formatDateTime, formatDuration } from "../lib/format";
import { usSectors } from "../lib/signals";
import { Drawer } from "./ui/controls";

/** INSIDER_TRANSACTION → "Insider transaction". */
export function typeLabel(t: string | undefined): string {
  if (!t || t === "UNCLASSIFIED") return "Unclassified";
  const s = t.replace(/_/g, " ").toLowerCase();
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function importanceLabel(n: number | undefined): string | null {
  if (n == null) return null;
  if (n >= 8) return "Major";
  if (n >= 6) return "Significant";
  if (n >= 4) return "Notable";
  return null;
}

/**
 * One event, in full, with everything that supports it.
 *
 * The list row says what happened. This says how we know: who reported it,
 * when each of them did, when this system found it, and what it was read to
 * mean for each company — the receipts the rest of the product is built on.
 */
export function EventDrawer({
  id,
  seed,
  onClose,
  onSymbol,
}: {
  id: number | null;
  seed?: MarketEvent;
  onClose: () => void;
  onSymbol: (symbol: string) => void;
}) {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["event", id],
    queryFn: () => api.event(id!),
    enabled: id != null,
    staleTime: 60_000,
  });
  const e = data ?? seed;

  return (
    <Drawer open={id != null} onClose={onClose} label={e?.headline ?? "Event"}>
      <div className="flex h-12 shrink-0 items-center gap-2 border-b border-border-subtle px-5">
        <span className="min-w-0 flex-1 truncate text-meta text-text-muted">
          {e ? typeLabel(e.event_type) : "Loading"}
        </span>
        {e && <CopyLink id={e.id} />}
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="flex h-8 w-8 items-center justify-center rounded-md text-text-muted transition-colors hover:bg-bg-panel-hover hover:text-text-primary"
        >
          <X size={17} />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
        {!e && isLoading && (
          <div className="flex flex-col gap-3 p-5">
            <span className="skeleton h-7 w-4/5" />
            <span className="skeleton h-4 w-3/5" />
            <span className="skeleton mt-4 h-24 w-full" />
          </div>
        )}
        {!e && isError && <p className="p-5 text-ui text-text-secondary">This event could not be loaded. It may have moved to the archive.</p>}
        {e && (
          <article className="flex flex-col gap-7 px-5 pb-10 pt-5">
            <header className="flex flex-col gap-3">
              <h2 className="font-reading text-[22px] font-semibold leading-snug text-text-primary">{e.headline}</h2>
              {e.summary && e.summary !== e.headline && (
                <p className="text-ui leading-relaxed text-text-secondary">{e.summary}</p>
              )}
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-meta text-text-secondary">
                <span className="inline-flex items-center gap-1.5">
                  {e.official && <ShieldCheck size={14} className="text-brand" aria-label="Official source" />}
                  <span className="font-medium text-text-primary">{e.source || "Unknown source"}</span>
                </span>
                {(e.source_count ?? 1) > 1 && <span>{e.source_count} independent sources</span>}
                {importanceLabel(e.importance) && (
                  <span className="rounded-full bg-brand-muted px-2 py-px font-semibold text-accent-text">{importanceLabel(e.importance)}</span>
                )}
              </div>
              <div className="mt-1 flex flex-wrap gap-2">
                {e.primary_url && (
                  <a href={e.primary_url} target="_blank" rel="noreferrer noopener" className="action-primary">
                    Open original <ExternalLink size={14} />
                  </a>
                )}
                <BriefButton event={e} />
              </div>
            </header>

            <BriefText event={e} />

            <Timeline e={e} />

            {(e.entities?.length ?? 0) > 0 && (
              <section>
                <SectionTitle>Companies</SectionTitle>
                <ul className="flex flex-col divide-y divide-border-subtle rounded-lg border border-border-subtle">
                  {e.entities!.map((en) => (
                    <EntityRow key={en.symbol + en.relationship} en={en} onSymbol={onSymbol} />
                  ))}
                </ul>
              </section>
            )}

            {usSectors(e.sectors).length > 0 && (
              <section>
                <SectionTitle>Sectors it reaches</SectionTitle>
                <p className="text-ui text-text-secondary">{usSectors(e.sectors).join(", ")}</p>
              </section>
            )}

            {e.facts && Object.keys(e.facts).length > 0 && (
              <section>
                <SectionTitle>Facts extracted</SectionTitle>
                <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-ui">
                  {Object.entries(e.facts).map(([k, v]) => (
                    <div key={k} className="contents">
                      <dt className="text-text-muted">{k.replace(/_/g, " ")}</dt>
                      <dd className="text-text-primary">{v}</dd>
                    </div>
                  ))}
                </dl>
              </section>
            )}

            <section>
              <SectionTitle>
                Evidence{e.evidence ? ` · ${e.evidence.length} ${e.evidence.length === 1 ? "item" : "items"}` : ""}
              </SectionTitle>
              {!e.evidence && isLoading && <span className="skeleton block h-16 w-full" />}
              <ul className="flex flex-col gap-2">
                {(e.evidence ?? []).map((ev) => (
                  <EvidenceRow key={ev.id} ev={ev} />
                ))}
              </ul>
            </section>

            {e.classified_at && (
              <p className="text-micro text-text-muted">
                Classified {formatAgo(e.classified_at)}
                {e.model ? ` by ${e.model}` : ""}
                {e.confidence != null ? `, ${Math.round(e.confidence * 100)}% confident` : ""}.
              </p>
            )}
          </article>
        )}
      </div>
    </Drawer>
  );
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return <h3 className="mb-2.5 text-meta font-semibold text-text-muted">{children}</h3>;
}

/**
 * When it happened, when it was said, when we knew.
 *
 * Only the last one is ever used to reason about what was knowable at a
 * given moment, which is why it is drawn as the anchor.
 */
function Timeline({ e }: { e: MarketEvent }) {
  const steps = [
    { label: "Happened", at: e.occurred_at, note: undefined as string | undefined },
    {
      label: e.timestamp_trust === "observed" ? "First seen by an aggregator" : "Published",
      at: e.published_at,
      note: undefined,
    },
    {
      label: "Bellwether found it",
      at: e.discovered_at,
      note: e.latency_seconds != null && e.latency_seconds > 0 ? `${formatDuration(e.latency_seconds)} after publication` : undefined,
    },
  ].filter((s) => s.at);
  return (
    <section>
      <SectionTitle>Timeline</SectionTitle>
      <ol className="relative flex flex-col gap-3 pl-5">
        <span className="absolute bottom-2 left-[5px] top-2 w-px bg-border-subtle" aria-hidden="true" />
        {steps.map((s, i) => {
          const anchor = i === steps.length - 1;
          return (
            <li key={s.label} className="relative">
              <span
                className={
                  "absolute -left-5 top-1 h-[11px] w-[11px] rounded-full border-2 " +
                  (anchor ? "border-brand bg-brand" : "border-border-focus bg-bg-raised")
                }
                aria-hidden="true"
              />
              <p className="text-ui text-text-primary">
                {s.label} <span className="text-text-secondary">{formatDateTime(s.at)}</span>
              </p>
              {s.note && <p className="text-meta text-text-muted">{s.note}</p>}
              {anchor && (
                <p className="text-meta text-text-muted">The time every backtest and event study uses — nothing here could be known earlier.</p>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}

const RELATION: Record<EventEntity["relationship"], string> = {
  primary: "Subject",
  mentioned: "Mentioned",
  peer: "Peer",
  sector: "Sector exposure",
};

function EntityRow({ en, onSymbol }: { en: EventEntity; onSymbol: (s: string) => void }) {
  const dir = en.direction;
  const Icon = dir === "positive" ? ArrowUpRight : dir === "negative" ? ArrowDownRight : Minus;
  return (
    <li className="flex items-start gap-3 px-3.5 py-3">
      <button
        type="button"
        onClick={() => onSymbol(en.symbol)}
        className="min-w-16 shrink-0 text-left text-ui font-semibold text-text-primary underline-offset-4 hover:text-brand hover:underline"
        title={`Open ${en.symbol} on the chart`}
      >
        {en.symbol}
      </button>
      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-center gap-x-2 text-meta text-text-muted">
          <span>{RELATION[en.relationship] ?? en.relationship}</span>
          {dir && (
            <span
              className={
                "inline-flex items-center gap-0.5 font-medium " +
                (dir === "positive" ? "text-semantic-up" : dir === "negative" ? "text-semantic-down" : "text-text-muted")
              }
            >
              <Icon size={13} />
              {dir === "unclear" ? "Direction unclear" : dir === "positive" ? "Likely positive" : "Likely negative"}
            </span>
          )}
        </p>
        {en.rationale && <p className="mt-1 text-meta leading-relaxed text-text-secondary">{en.rationale}</p>}
      </div>
    </li>
  );
}

function EvidenceRow({ ev }: { ev: Evidence }) {
  let host = ev.publisher;
  try {
    host = ev.publisher || new URL(ev.url).hostname.replace(/^www\./, "");
  } catch {
    /* keep publisher */
  }
  const lag =
    ev.published_at && ev.discovered_at
      ? (new Date(ev.discovered_at).getTime() - new Date(ev.published_at).getTime()) / 1000
      : null;
  return (
    <li>
      <a
        href={ev.url}
        target="_blank"
        rel="noreferrer noopener"
        className="group block rounded-lg border border-border-subtle px-3.5 py-3 transition-colors hover:border-border-focus hover:bg-bg-panel-hover"
      >
        <p className="flex items-center gap-2 text-meta text-text-muted">
          <span className="font-medium text-text-secondary">{host}</span>
          <span>{formatAgo(ev.published_at || ev.discovered_at)}</span>
          {ev.words ? <span>{ev.words.toLocaleString()} words read</span> : <span>headline only</span>}
          <ExternalLink size={12} className="ml-auto opacity-0 transition-opacity group-hover:opacity-100" />
        </p>
        <p className="mt-1 text-ui text-text-primary">{ev.title}</p>
        {ev.description && <p className="mt-1 line-clamp-2 text-meta text-text-secondary">{ev.description}</p>}
        {lag != null && lag > 60 && (
          <p className="mt-1.5 text-micro text-text-muted">Found {formatDuration(lag)} after it was published.</p>
        )}
      </a>
    </li>
  );
}

function BriefText({ event }: { event: MarketEvent }) {
  if (!event.brief) return null;
  return (
    <section className="rounded-lg bg-brand-muted px-4 py-3.5">
      <p className="mb-1.5 flex items-center gap-1.5 text-meta font-semibold text-brand">
        <Sparkles size={13} /> AI brief
      </p>
      <p className="text-ui leading-relaxed text-text-primary">{event.brief}</p>
      <p className="mt-2 text-micro text-text-muted">Model-written from the evidence below. Check it against the sources.</p>
    </section>
  );
}

function BriefButton({ event }: { event: MarketEvent }) {
  const qc = useQueryClient();
  const write = useMutation({
    mutationFn: () => api.briefEvent(event.id),
    onSuccess: (r) => {
      qc.setQueryData<MarketEvent>(["event", event.id], (old) => (old ? { ...old, brief: r.brief } : old));
      qc.invalidateQueries({ queryKey: ["events"] });
    },
  });
  if (event.brief) return null;
  return (
    <>
      <button type="button" onClick={() => write.mutate()} disabled={write.isPending} className="action-secondary">
        <Sparkles size={14} className="text-brand" />
        {write.isPending ? "Writing brief…" : "Write a brief"}
      </button>
      {write.isError && <p className="w-full text-meta text-semantic-down">{(write.error as Error).message}</p>}
    </>
  );
}

function CopyLink({ id }: { id: number }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        const url = new URL(window.location.href);
        url.searchParams.set("event", String(id));
        try {
          await navigator.clipboard.writeText(url.toString());
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        } catch {
          /* clipboard unavailable */
        }
      }}
      className="action-ghost text-meta"
      aria-live="polite"
    >
      <Link2 size={14} />
      {done ? "Copied" : "Copy link"}
    </button>
  );
}
