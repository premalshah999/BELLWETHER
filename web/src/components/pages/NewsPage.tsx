import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Newspaper, Search, Sparkles, X } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type EventQuery, type MarketEvent } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { Empty } from "../ui/Empty";
import { Panel } from "../ui/Panel";
import { Pill } from "../ui/Pill";

const WINDOWS = [
  { label: "6h", hours: 6 },
  { label: "24h", hours: 24 },
  { label: "3d", hours: 72 },
  { label: "7d", hours: 168 },
  { label: "30d", hours: 720 },
];
const LEVELS = [
  { label: "all", min: 0 },
  { label: "4+", min: 4 },
  { label: "6+", min: 6 },
  { label: "8+", min: 8 },
];
/**
 * How many items one page of the feed holds.
 *
 * 500, which is also the server's ceiling (see ListEvents in
 * internal/storage/postgres/events_read.go) -- so one page is the most the
 * API will return and "load more" genuinely fetches beyond it rather than
 * walking up to a limit the backend would have allowed all along.
 */
const PAGE = 500;

const UNIVERSES = [
  { label: "index", value: "index" as const },
  { label: "all listed", value: "all" as const },
];

/**
 * A short note on what one item means.
 *
 * Asked for rather than generated on arrival. The feed takes several thousand
 * items a day and the overwhelming majority are procedural filings; briefing
 * all of them would spend a month's model budget explaining shareholding
 * patterns. Once written it is kept, so it appears immediately for everyone
 * afterwards.
 */
function Brief({ event }: { event: MarketEvent }) {
  const qc = useQueryClient();
  const [text, setText] = useState(event.brief ?? "");

  const write = useMutation({
    mutationFn: () => api.briefEvent(event.id),
    onSuccess: (r) => {
      setText(r.brief);
      qc.invalidateQueries({ queryKey: ["events"] });
    },
  });

  if (text) {
    return (
      <p className="mt-1 w-full text-ui leading-relaxed text-text-secondary">
        <Sparkles size={11} className="mr-1.5 inline text-brand" />
        {text}
      </p>
    );
  }
  return (
    <>
      <button
        type="button"
        onClick={() => write.mutate()}
        disabled={write.isPending}
        className="flex items-center gap-1 font-mono text-meta text-text-muted transition-colors hover:text-brand disabled:opacity-50"
      >
        <Sparkles size={10} />
        {write.isPending ? "writing…" : "brief"}
      </button>
      {write.isError && (
        <span className="text-meta text-semantic-down">
          {(write.error as Error).message}
        </span>
      )}
    </>
  );
}

export function NewsPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const [search, setSearch] = useState("");
  const [submitted, setSubmitted] = useState("");
  const [win, setWin] = useState(1);
  const [level, setLevel] = useState(1);
  const [universe, setUniverse] = useState(0);
  const [officialOnly, setOfficialOnly] = useState(false);
  const [order, setOrder] = useState<"published" | "arrival">("published");
  const [types, setTypes] = useState<string[]>([]);
  // The feed was capped at a flat 300 with no way past it, so an archive of
  // two and a half million items ended at whatever the three-hundredth was.
  // It grows on request instead.
  const [limit, setLimit] = useState(PAGE);

  const catalogue = useQuery({ queryKey: ["event-types"], queryFn: api.eventTypes });

  const query = useMemo<EventQuery>(
    () => ({
      q: submitted || undefined,
      hours: WINDOWS[win]!.hours,
      minImportance: LEVELS[level]!.min || undefined,
      official: officialOnly || undefined,
      universe: UNIVERSES[universe]!.value,
      type: types.length ? types.join(",") : undefined,
      order,
      limit,
    }),
    [submitted, win, level, officialOnly, universe, types, order, limit],
  );

  const { data } = useQuery({
    queryKey: ["events", query],
    queryFn: () => api.events(query),
    refetchInterval: 60_000,
    placeholderData: (p) => p,
  });

  const events = data?.events ?? [];

  return (
    <Panel
      title={`Feed · ${events.length}`}
      scroll
      className="min-w-0 flex-1"
      action={
        <div className="flex items-center gap-2">
          <Seg options={UNIVERSES.map((u) => u.label)} value={universe} onChange={setUniverse} />
          <Seg options={WINDOWS.map((w) => w.label)} value={win} onChange={setWin} />
          <Seg options={LEVELS.map((l) => l.label)} value={level} onChange={setLevel} />
          <button
            type="button"
            onClick={() => setOfficialOnly((v) => !v)}
            className={
              "border px-1.5 py-0.5 font-mono text-meta " +
              (officialOnly
                ? "border-brand bg-brand-muted text-brand"
                : "border-border-subtle text-text-muted hover:text-text-primary")
            }
          >
            official
          </button>
          <button
            type="button"
            onClick={() => setOrder(order === "published" ? "arrival" : "published")}
            title={
              order === "published"
                ? "Sorted by when the publisher dated each item."
                : "Sorted by when we found each item — an audit of ingestion, not a reading order."
            }
            className="border border-border-subtle px-1.5 py-0.5 font-mono text-meta text-text-muted transition-colors hover:text-text-primary"
          >
            {order === "published" ? "by published" : "by arrival"}
          </button>
        </div>
      }
    >
      {/* Type filter. Fifty-three kinds of event share this feed, and an
          operator looking for results or ratings actions had no way to say
          so. */}
      <div className="flex flex-wrap items-center gap-1 border-b border-border-subtle px-3 py-2">
        <button
          type="button"
          onClick={() => setTypes([])}
          className={
            "border px-1.5 py-0.5 font-mono text-meta transition-colors " +
            (types.length === 0
              ? "border-brand bg-brand-muted text-brand"
              : "border-border-subtle text-text-muted hover:text-text-primary")
          }
        >
          every type
        </button>
        {(catalogue.data?.types ?? []).map((t) => {
          const on = types.includes(t.type);
          return (
            <button
              key={t.type}
              type="button"
              onClick={() =>
                setTypes(on ? types.filter((x) => x !== t.type) : [...types, t.type])
              }
              className={
                "border px-1.5 py-0.5 font-mono text-meta transition-colors " +
                (on
                  ? "border-brand bg-brand-muted text-brand"
                  : "border-border-subtle text-text-muted hover:text-text-primary")
              }
            >
              {(t.label || t.type).toLowerCase().replace(/_/g, " ")}
            </button>
          );
        })}
      </div>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(search.trim());
        }}
        className="relative border-b border-border-subtle"
      >
        <Search size={12} className="pointer-events-none absolute left-3 top-2.5 text-text-muted" />
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="search headlines and summaries…"
          className="w-full bg-transparent py-2 pl-8 pr-8 text-ui outline-none placeholder:text-text-muted"
        />
        {(search || submitted) && (
          <button
            type="button"
            onClick={() => {
              setSearch("");
              setSubmitted("");
            }}
            className="absolute right-3 top-2.5 text-text-muted hover:text-text-primary"
          >
            <X size={12} />
          </button>
        )}
      </form>

      {events.length === 0 ? (
        <Empty
          icon={Newspaper}
          title="Nothing matches."
          hint="Widen the window, lower the importance floor, or switch the universe to all listed companies."
        />
      ) : (
        <ul className="divide-y divide-border-subtle">
          {events.map((e) => (
            <li key={e.id} className="px-4 py-3 hover:bg-bg-panel-hover">
              <a
                href={e.primary_url || undefined}
                target="_blank"
                rel="noreferrer noopener"
                className={
                  "block text-ui leading-relaxed " +
                  (e.primary_url ? "text-text-primary hover:text-brand" : "cursor-default")
                }
              >
                {e.headline}
              </a>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <span className="font-mono text-meta text-text-muted">
                  {/* "seen" where the timestamp is an aggregator's surfacing
                      time rather than a publication time. */}
                  {e.timestamp_trust === "observed" ? "seen " : ""}
                  {formatAgo(e.published_at || e.discovered_at)}
                </span>
                {(e.entities ?? []).slice(0, 3).map((en) => (
                  <button
                    key={en.symbol}
                    type="button"
                    onClick={() => onSelect(en.symbol)}
                    className="font-mono text-meta text-text-secondary hover:text-brand"
                  >
                    {en.symbol}
                  </button>
                ))}
                {e.event_type && e.event_type !== "UNCLASSIFIED" && (
                  <Pill tone="muted">{e.event_type.replace(/_/g, " ").toLowerCase()}</Pill>
                )}
                {e.official && <Pill tone="brand">official</Pill>}
                {(e.importance ?? 0) >= 7 && <Pill tone="down">high</Pill>}
                <Brief event={e} />
              </div>
            </li>
          ))}
          {/* The feed ends where the request ended, not where the archive
              does. Saying so, and offering the next page, is the difference
              between "that is all there is" and "that is all I asked for". */}
          {events.length >= limit && (
            <li className="px-4 py-3">
              <button
                type="button"
                onClick={() => setLimit((n) => n + PAGE)}
                className="font-mono text-meta text-text-muted transition-colors hover:text-brand"
              >
                load {PAGE} more · showing {events.length}
              </button>
            </li>
          )}
        </ul>
      )}
    </Panel>
  );
}

function Seg({
  options,
  value,
  onChange,
}: {
  options: string[];
  value: number;
  onChange: (i: number) => void;
}) {
  return (
    <div className="flex border border-border-subtle">
      {options.map((label, i) => (
        <button
          key={label}
          type="button"
          onClick={() => onChange(i)}
          className={
            "border-r border-border-subtle px-1.5 py-0.5 font-mono text-meta last:border-r-0 " +
            (i === value ? "bg-brand-muted text-brand" : "text-text-muted hover:text-text-primary")
          }
        >
          {label}
        </button>
      ))}
    </div>
  );
}
