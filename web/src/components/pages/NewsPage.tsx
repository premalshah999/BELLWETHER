import { useQuery } from "@tanstack/react-query";
import { Newspaper, Search, ShieldCheck, Sparkles, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, type EventQuery, type MarketEvent } from "../../lib/api";
import { dayOf, formatClock, formatDateTime, formatDay } from "../../lib/format";
import { useUrlList, useUrlState } from "../../lib/url";
import { EventDrawer, importanceLabel, typeLabel } from "../EventDrawer";
import { MultiSelect, PageHeader, Segmented, Select, SkeletonRows, Switch } from "../ui/controls";

const WINDOWS = [
  { value: "6h", label: "6h", hours: 6, words: "6 hours" },
  { value: "24h", label: "24h", hours: 24, words: "24 hours" },
  { value: "3d", label: "3d", hours: 72, words: "3 days" },
  { value: "7d", label: "7d", hours: 168, words: "7 days" },
  { value: "30d", label: "30d", hours: 720, words: "30 days" },
] as const;

const LEVELS = [
  { value: "any", label: "Any importance", min: 0 },
  { value: "notable", label: "Notable and up", min: 4 },
  { value: "significant", label: "Significant and up", min: 6 },
  { value: "major", label: "Major only", min: 8 },
] as const;

const UNIVERSES = [
  { value: "index", label: "S&P 1500 companies" },
  { value: "all", label: "Every listed company" },
] as const;

const ORDERS = [
  { value: "published", label: "Newest published" },
  { value: "arrival", label: "Newest found" },
] as const;

/** The server's ceiling for one request; "Show more" asks beyond it. */
const PAGE = 500;

export function NewsPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const navigate = useNavigate();
  const [win, setWin] = useUrlState<(typeof WINDOWS)[number]["value"]>("window", "24h");
  const [level, setLevel] = useUrlState<(typeof LEVELS)[number]["value"]>("importance", "notable");
  const [universe, setUniverse] = useUrlState<(typeof UNIVERSES)[number]["value"]>("universe", "index");
  const [order, setOrder] = useUrlState<(typeof ORDERS)[number]["value"]>("order", "published");
  const [official, setOfficial] = useUrlState<"" | "1">("official", "");
  const [types, setTypes] = useUrlList("types");
  const [q, setQ] = useUrlState("q", "");
  const [openId, setOpenId] = useUrlState("event", "");
  const [draft, setDraft] = useState(q);
  const [limit, setLimit] = useState(PAGE);
  const [cursor, setCursor] = useState(-1);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);

  useEffect(() => setDraft(q), [q]);

  const catalogue = useQuery({ queryKey: ["event-types"], queryFn: api.eventTypes, staleTime: Infinity });
  const typeOptions = useMemo(
    () =>
      (catalogue.data?.types ?? [])
        .map((t) => ({ value: t.type, label: typeLabel(t.type) }))
        .sort((a, b) => a.label.localeCompare(b.label)),
    [catalogue.data],
  );

  const window_ = WINDOWS.find((w) => w.value === win) ?? WINDOWS[1];
  const query = useMemo<EventQuery>(
    () => ({
      q: q || undefined,
      hours: window_.hours,
      minImportance: LEVELS.find((l) => l.value === level)?.min || undefined,
      official: official === "1" || undefined,
      universe,
      type: types.length ? types.join(",") : undefined,
      order,
      limit,
    }),
    [q, window_, level, official, universe, types, order, limit],
  );

  const { data, isLoading, isFetching } = useQuery({
    queryKey: ["events", query],
    queryFn: () => api.events(query),
    refetchInterval: 60_000,
    placeholderData: (p) => p,
  });
  const events = data?.events ?? [];

  const open = useCallback((id: number) => setOpenId(String(id)), [setOpenId]);
  const toChart = useCallback(
    (symbol: string) => {
      onSelect(symbol);
      navigate("/charts");
    },
    [onSelect, navigate],
  );

  // j / k to move, Enter to open, / to search. A feed read every morning
  // should not need the mouse.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (openId || t.closest("input, textarea, select, [role=dialog]") || e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === "/") {
        e.preventDefault();
        searchRef.current?.focus();
      } else if (e.key === "j" || e.key === "k") {
        e.preventDefault();
        setCursor((c) => {
          const n = Math.max(0, Math.min(events.length - 1, c + (e.key === "j" ? 1 : -1)));
          listRef.current?.querySelector<HTMLElement>(`[data-row="${n}"] [data-open]`)?.focus();
          return n;
        });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [events.length, openId]);

  const filtered = types.length > 0 || official === "1" || level !== "notable" || universe !== "index" || !!q;
  const clear = () => {
    setTypes([]);
    setOfficial("");
    setLevel("notable");
    setUniverse("index");
    setQ("");
  };

  const seed = openId ? events.find((e) => String(e.id) === openId) : undefined;

  let lastDay = "";
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="News"
        subtitle={
          isLoading
            ? "Loading the feed…"
            : `${events.length.toLocaleString()}${events.length >= limit ? "+" : ""} ${events.length === 1 ? "event" : "events"} in the last ${window_.words}${official === "1" ? ", official sources only" : ""}.`
        }
        actions={
          <>
          <Select label="Order" value={order} onChange={setOrder} options={ORDERS} align="right" />
          <form
            role="search"
            onSubmit={(e) => {
              e.preventDefault();
              setQ(draft.trim());
            }}
            className="relative w-full sm:w-72"
          >
            <Search size={15} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
            <input
              ref={searchRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Search headlines…"
              aria-label="Search headlines"
              className="h-9 w-full rounded-md border border-border-subtle bg-bg-base pl-9 pr-9 text-ui outline-none transition-colors placeholder:text-text-muted hover:border-border-focus focus:border-brand"
            />
            {draft ? (
              <button
                type="button"
                aria-label="Clear search"
                onClick={() => {
                  setDraft("");
                  setQ("");
                }}
                className="absolute right-2 top-1/2 flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-text-muted hover:text-text-primary"
              >
                <X size={14} />
              </button>
            ) : (
              <kbd className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 rounded border border-border-subtle px-1.5 text-micro text-text-muted max-sm:hidden">
                /
              </kbd>
            )}
          </form>
          </>
        }
      >
        <Segmented label="Time window" options={WINDOWS} value={win} onChange={setWin} />
        <Select label="Importance" value={level} onChange={setLevel} options={LEVELS} />
        <MultiSelect label="Type" allLabel="All types" options={typeOptions} value={types} onChange={setTypes} />
        <Select label="Companies" value={universe} onChange={setUniverse} options={UNIVERSES} />
        <Switch
          checked={official === "1"}
          onChange={(v) => setOfficial(v ? "1" : "")}
          label={
            <span className="inline-flex items-center gap-1">
              <ShieldCheck size={14} className="text-brand" /> Official only
            </span>
          }
        />
        {isFetching && !isLoading && <span className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-brand" title="Refreshing" />}
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <SkeletonRows count={9} height={60} />
        ) : events.length === 0 ? (
          <div className="flex max-w-md flex-col items-start gap-3 px-6 py-12">
            <Newspaper size={22} className="text-text-muted" />
            <p className="font-reading text-display text-text-primary">Nothing matches these filters.</p>
            <p className="text-ui text-text-secondary">
              Try a longer time window, a lower importance, or every listed company rather than the S&P 1500.
            </p>
            {filtered && (
              <button type="button" onClick={clear} className="action-secondary mt-1">
                Clear filters
              </button>
            )}
          </div>
        ) : (
          <ul ref={listRef} className="mx-5 mb-8 overflow-clip rounded-xl border border-border-subtle bg-bg-card md:mx-8">
            {events.map((e, i) => {
              const at = e.published_at || e.discovered_at;
              const day = dayOf(at);
              const header = day !== lastDay;
              lastDay = day;
              return (
                <li key={e.id} data-row={i}>
                  {header && (
                    <h2 className="sticky top-0 z-10 border-b border-border-subtle bg-bg-card/95 px-5 py-2 text-meta font-semibold text-text-secondary backdrop-blur md:px-6">
                      {formatDay(at)}
                    </h2>
                  )}
                  <EventRow e={e} active={cursor === i || String(e.id) === openId} onOpen={open} onSymbol={toChart} />
                </li>
              );
            })}
            {events.length >= limit && (
              <li className="px-6 py-5">
                <button type="button" onClick={() => setLimit((n) => n + PAGE)} className="action-secondary">
                  Show {PAGE} more
                </button>
              </li>
            )}
          </ul>
        )}
      </div>

      <EventDrawer
        id={openId ? Number(openId) : null}
        seed={seed}
        onClose={() => setOpenId("")}
        onSymbol={(s) => {
          setOpenId("");
          toChart(s);
        }}
      />
    </div>
  );
}

function EventRow({
  e,
  active,
  onOpen,
  onSymbol,
}: {
  e: MarketEvent;
  active: boolean;
  onOpen: (id: number) => void;
  onSymbol: (symbol: string) => void;
}) {
  const at = e.published_at || e.discovered_at;
  const others = Math.max(0, (e.source_count ?? 1) - 1);
  const tickers = (e.entities ?? []).filter((en) => en.relationship !== "sector").slice(0, 4);
  const major = importanceLabel(e.importance);
  return (
    <div
      className={
        "group relative flex gap-4 border-b border-border-subtle px-5 py-3.5 transition-colors [contain-intrinsic-size:auto_76px] [content-visibility:auto] hover:bg-bg-panel-hover md:px-6 " +
        (active ? "bg-bg-panel-hover" : "")
      }
    >
      <time
        dateTime={at}
        title={formatDateTime(at)}
        className="w-12 shrink-0 pt-0.5 font-num text-[12px] text-text-muted max-sm:hidden"
      >
        {formatClock(at)}
      </time>
      <div className="min-w-0 flex-1">
        <button
          type="button"
          data-open
          onClick={() => onOpen(e.id)}
          className="text-left text-[15px] font-semibold leading-snug tracking-[-0.005em] text-text-primary outline-none after:absolute after:inset-0 after:content-[''] focus-visible:after:rounded-md focus-visible:after:ring-2 focus-visible:after:ring-brand"
        >
          {e.headline}
        </button>
        <p className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-text-muted">
          <span className="inline-flex items-center gap-1 text-text-secondary">
            {e.official && <ShieldCheck size={13} className="text-brand" aria-label="Official source" />}
            <span className="font-medium">{e.source || "Unknown source"}</span>
            {others > 0 && <span className="text-text-muted" title={`${others + 1} independent sources`}>+{others}</span>}
          </span>
          {tickers.length > 0 && (
            <span className="relative z-10 inline-flex gap-1.5">
              {tickers.map((en) => (
                <button
                  key={en.symbol}
                  type="button"
                  onClick={() => onSymbol(en.symbol)}
                  title={`Open ${en.symbol} on the chart`}
                  className="inline-flex h-6 items-center rounded-md bg-bg-chip px-2 font-num text-[12px] font-medium text-text-primary transition-colors hover:bg-brand-muted hover:text-accent-text"
                >
                  {en.symbol}
                </button>
              ))}
            </span>
          )}
          {e.event_type && e.event_type !== "UNCLASSIFIED" && <span className="rounded-full border border-border-subtle px-2 py-px">{typeLabel(e.event_type)}</span>}
          {major && (major === "Major" || major === "Significant") && (
            <span className="rounded-full bg-brand-muted px-2 py-px font-semibold text-accent-text">{major}</span>
          )}
          {e.brief && (
            <span className="inline-flex items-center gap-1 text-brand" title="Has an AI brief">
              <Sparkles size={12} /> Brief
            </span>
          )}
          <span className="sm:hidden">{formatClock(at)}</span>
        </p>
      </div>
    </div>
  );
}
