import { useQuery } from "@tanstack/react-query";
import { ScrollText, ShieldCheck } from "lucide-react";
import { useMemo } from "react";
import { useNavigate } from "react-router-dom";
import { api, type MarketEvent } from "../../lib/api";
import { dayOf, formatClock, formatDateTime, formatDay } from "../../lib/format";
import { useUrlState } from "../../lib/url";
import { usSectors } from "../../lib/signals";
import { EventDrawer, importanceLabel, typeLabel } from "../EventDrawer";
import { PageHeader, Segmented, SkeletonRows, Switch } from "../ui/controls";

/**
 * World events, policy and commodities -- and which of your holdings each
 * one actually touches.
 *
 * A tariff notice or a Fed statement never names a company, so nothing else
 * in this app surfaces it against a portfolio: the news feed is built around
 * entities, and an event with none looks like it is about nothing. It is
 * about a sector, which is a fact this page can act on because every event
 * here already carries the sectors it reaches (internal/events/sector.go --
 * GICS sectors, prefixed "US: ") and every held or watched
 * symbol has a known industry to compare them against.
 *
 * Positions, not just the watchlist, are what "touches my holdings" checks
 * first -- a hit against real money is a different fact from a hit against
 * a name someone is merely tracking, and the two are shown differently
 * rather than folded into one undifferentiated list.
 */
const TYPES = [
  { label: "Everything", value: "" },
  { label: "Policy", value: "REGULATORY_POLICY" },
  { label: "Macro", value: "MACRO_EVENT" },
  { label: "Geopolitics", value: "GEOPOLITICAL_EVENT" },
  { label: "Commodities", value: "COMMODITY_EVENT" },
  { label: "Sectors", value: "SECTOR_EVENT" },
] as const;

const ALL_TYPES = TYPES.slice(1)
  .map((t) => t.value)
  .join(",");

function money(n: number) {
  return `$${Math.abs(n).toLocaleString("en-US", { maximumFractionDigits: 0 })}`;
}

export function GeopoliticsPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const navigate = useNavigate();
  const [type, setType] = useUrlState<(typeof TYPES)[number]["value"]>("type", "");
  const [mine, setMine] = useUrlState<"" | "1">("mine", "");
  const onlyMine = mine === "1";
  const [openId, setOpenId] = useUrlState("event", "");

  const { data, isLoading } = useQuery({
    queryKey: ["geopolitics-events", type],
    queryFn: () => api.events({ type: type || ALL_TYPES, universe: "all", limit: 150, order: "arrival" }),
    refetchInterval: 60_000,
  });
  const events = useMemo(() => data?.events ?? [], [data]);

  const { data: positionsData } = useQuery({
    queryKey: ["positions-for-geopolitics"],
    queryFn: api.positions,
    staleTime: 60_000,
  });
  const positions = useMemo(() => positionsData?.positions ?? [], [positionsData]);
  // A symbol can appear in more than one account; what a hit card needs is
  // the combined exposure, not one row per account.
  const valueBySymbol = useMemo(() => {
    const out = new Map<string, number>();
    for (const p of positions) {
      if (p.market_value == null) continue;
      out.set(p.symbol, (out.get(p.symbol) ?? 0) + p.market_value);
    }
    return out;
  }, [positions]);

  const { data: watchlist } = useQuery({
    queryKey: ["watchlist-for-geopolitics"],
    queryFn: api.watchlistCached,
    staleTime: 60_000,
  });
  const watched = useMemo(() => watchlist?.items ?? [], [watchlist]);

  // Real positions and watched-but-not-held symbols both need a sector
  // lookup, but they answer different questions -- so the union goes to the
  // API, and which bucket each symbol came from is kept separately.
  const holdingSymbols = useMemo(() => {
    const s = new Set<string>();
    for (const p of positions) s.add(p.symbol);
    for (const w of watched) s.add(w.symbol);
    return [...s];
  }, [positions, watched]);

  const { data: sectorData } = useQuery({
    queryKey: ["holding-sectors", holdingSymbols.join(",")],
    queryFn: () => api.symbolSectors(holdingSymbols),
    enabled: holdingSymbols.length > 0,
    staleTime: 5 * 60_000,
  });
  // symbol -> its industry, already spelled to match an event's own Sectors.
  const bySymbol = sectorData?.sectors ?? {};
  // industry -> which held/watched symbols sit in it, the reverse index a
  // card needs.
  const holdingsBySector = useMemo(() => {
    const out = new Map<string, string[]>();
    for (const [symbol, sector] of Object.entries(bySymbol)) {
      out.set(sector, [...(out.get(sector) ?? []), symbol]);
    }
    return out;
  }, [bySymbol]);

  const touching = (event: MarketEvent): string[] => {
    const hit = new Set<string>();
    for (const sector of event.sectors ?? []) {
      for (const symbol of holdingsBySector.get(sector) ?? []) hit.add(symbol);
    }
    // Real money first, so the most consequential hit on a card is never
    // pushed off-screen by a longer tail of merely-watched names.
    return [...hit].sort((a, b) => (valueBySymbol.has(b) ? 1 : 0) - (valueBySymbol.has(a) ? 1 : 0));
  };

  const shown = onlyMine ? events.filter((e) => touching(e).length > 0) : events;

  const toChart = (symbol: string) => {
    onSelect(symbol);
    navigate("/charts");
  };
  const touchingCount = events.filter((e) => touching(e).length > 0).length;
  let lastDay = "";

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="Policy & macro"
        subtitle={
          isLoading
            ? "Loading policy, macro and world events…"
            : holdingSymbols.length === 0
              ? "Rules, rates, trade and world events, and the sectors each one reaches."
              : `Rules, rates, trade and world events. ${touchingCount} of the latest ${events.length} reach a sector you hold or watch.`
        }
      >
        <Segmented label="Kind of event" options={TYPES} value={type} onChange={setType} />
        <Switch
          checked={onlyMine}
          onChange={(v) => setMine(v ? "1" : "")}
          label={holdingSymbols.length === 0 ? "Touches my holdings (add a position or watchlist first)" : "Touches my holdings"}
        />
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <SkeletonRows count={8} height={64} />
        ) : shown.length === 0 ? (
          <div className="flex max-w-md flex-col items-start gap-3 px-6 py-12">
            <ScrollText size={22} className="text-text-muted" />
            <p className="font-reading text-display text-text-primary">
              {onlyMine ? "Nothing here reaches your holdings right now." : "No events of this kind yet."}
            </p>
            <p className="text-ui text-text-secondary">
              {onlyMine ? "Turn off the holdings filter to see every event." : "Choose another kind of event above."}
            </p>
          </div>
        ) : (
          <ul className="mx-5 mb-8 overflow-clip rounded-xl border border-border-subtle bg-bg-card md:mx-8">
            {shown.map((e) => {
              const hits = touching(e);
              // Sorted by arrival, so grouped and timed by arrival too: grouping
              // by publication date made day headings repeat out of order.
              const at = e.discovered_at;
              const day = dayOf(at);
              const header = day !== lastDay;
              lastDay = day;
              const major = importanceLabel(e.importance);
              return (
                <li key={e.id}>
                  {header && (
                    <h2 className="sticky top-0 z-10 border-b border-border-subtle bg-bg-card/95 px-5 py-2 text-meta font-semibold text-text-secondary backdrop-blur md:px-6">
                      {formatDay(at)}
                    </h2>
                  )}
                  <div
                    className={
                      "group relative flex gap-4 border-b border-border-subtle px-5 py-3.5 transition-colors [contain-intrinsic-size:auto_84px] [content-visibility:auto] hover:bg-bg-panel-hover md:px-6 " +
                      (hits.length ? "shadow-[inset_3px_0_0_var(--brass)]" : "")
                    }
                  >
                    <time dateTime={at} title={formatDateTime(at)} className="w-14 shrink-0 pt-0.5 font-num text-[12px] leading-[1.35] text-text-muted max-sm:hidden">
                      {formatClock(at)}
                    </time>
                    <div className="min-w-0 flex-1">
                      <button
                        type="button"
                        onClick={() => setOpenId(String(e.id))}
                        className="text-left text-[15px] font-semibold leading-snug tracking-[-0.005em] text-text-primary outline-none after:absolute after:inset-0 after:content-[''] focus-visible:after:rounded-md focus-visible:after:ring-2 focus-visible:after:ring-brand"
                      >
                        {e.headline}
                      </button>
                      <p className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-text-muted">
                        <span className="inline-flex items-center gap-1 font-medium text-text-secondary">
                          {e.official && <ShieldCheck size={13} className="text-brand" aria-label="Official source" />}
                          {e.source || "Unknown source"}
                        </span>
                        <span className="rounded-full border border-border-subtle px-2 py-px">{typeLabel(e.event_type)}</span>
                        {usSectors(e.sectors).length > 0 && <span>Reaches {usSectors(e.sectors).join(", ")}</span>}
                        {(major === "Major" || major === "Significant") && (
                          <span className="rounded-full bg-brand-muted px-2 py-px font-semibold text-accent-text">{major}</span>
                        )}
                      </p>
                      {hits.length > 0 && (
                        <p className="relative z-10 mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-meta">
                          <span className="font-medium text-brand">Touches</span>
                          {hits.map((sym) => {
                            const value = valueBySymbol.get(sym);
                            return (
                              <button
                                key={sym}
                                type="button"
                                onClick={() => toChart(sym)}
                                title={value != null ? "A position you hold" : "On your watchlist"}
                                className="inline-flex h-6 items-center rounded-md bg-bg-chip px-2 font-num text-[12px] font-medium text-text-primary transition-colors hover:bg-brand-muted hover:text-accent-text"
                              >
                                {sym}
                                {value != null && <span className="ml-1 font-normal text-text-muted">{money(value)}</span>}
                              </button>
                            );
                          })}
                        </p>
                      )}
                    </div>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      <EventDrawer
        id={openId ? Number(openId) : null}
        seed={openId ? events.find((e) => String(e.id) === openId) : undefined}
        onClose={() => setOpenId("")}
        onSymbol={(sym) => {
          setOpenId("");
          toChart(sym);
        }}
      />
    </div>
  );
}
