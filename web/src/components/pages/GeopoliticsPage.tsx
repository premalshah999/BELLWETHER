import { useQuery } from "@tanstack/react-query";
import { Globe2 } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type MarketEvent } from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { venueOf } from "../../lib/symbol";
import { Empty } from "../ui/Empty";
import { Panel } from "../ui/Panel";
import { Pill } from "../ui/Pill";

/**
 * World events, policy and commodities -- and which of your holdings each
 * one actually touches.
 *
 * A tariff notice or a Fed statement never names a company, so nothing else
 * in this app surfaces it against a portfolio: the news feed is built around
 * entities, and an event with none looks like it is about nothing. It is
 * about a sector, which is a fact this page can act on because every event
 * here already carries the sectors it reaches (internal/events/sector.go --
 * NSE industries and, prefixed "US: ", GICS ones) and every held or watched
 * symbol has a known industry to compare them against.
 *
 * Positions, not just the watchlist, are what "touches my holdings" checks
 * first -- a hit against real money is a different fact from a hit against
 * a name someone is merely tracking, and the two are shown differently
 * rather than folded into one undifferentiated list.
 */
const TYPES = [
  { label: "all", value: "" },
  { label: "geopolitical", value: "GEOPOLITICAL_EVENT" },
  { label: "policy", value: "REGULATORY_POLICY" },
  { label: "macro", value: "MACRO_EVENT" },
  { label: "commodity", value: "COMMODITY_EVENT" },
  { label: "sector", value: "SECTOR_EVENT" },
] as const;

const ALL_TYPES = TYPES.slice(1)
  .map((t) => t.value)
  .join(",");

function money(n: number, symbol: string) {
  const cur = venueOf(symbol) === "NSE" ? "₹" : "$";
  return `${cur}${Math.abs(n).toLocaleString(undefined, { maximumFractionDigits: 0 })}`;
}

export function GeopoliticsPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const [type, setType] = useState<(typeof TYPES)[number]["value"]>("");
  const [onlyMine, setOnlyMine] = useState(false);

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
    queryFn: api.watchlist,
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

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <div className="flex h-10 shrink-0 items-center gap-3 border-b border-border-subtle px-4">
        <span className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
          Geopolitics &amp; Policy
        </span>
        <div className="flex border border-border-subtle">
          {TYPES.map((t) => (
            <button
              key={t.value}
              type="button"
              onClick={() => setType(t.value)}
              className={
                "px-2 py-1 font-mono text-micro uppercase tracking-wider transition-colors " +
                (type === t.value
                  ? "bg-brand-muted text-brand"
                  : "text-text-muted hover:text-text-primary")
              }
            >
              {t.label}
            </button>
          ))}
        </div>
        <button
          type="button"
          onClick={() => setOnlyMine((v) => !v)}
          disabled={holdingSymbols.length === 0}
          className={
            "border px-2 py-1 font-mono text-micro uppercase tracking-wider transition-colors disabled:opacity-40 " +
            (onlyMine
              ? "border-brand/40 bg-brand-muted text-brand"
              : "border-border-subtle text-text-muted hover:text-text-primary")
          }
          title={
            holdingSymbols.length === 0
              ? "Add a position or a watchlist symbol to filter by it"
              : "Show only events that reach a sector you hold or watch"
          }
        >
          touches my holdings
        </button>
        <span className="ml-auto font-mono text-meta text-text-muted">{shown.length} events</span>
      </div>

      {isLoading ? (
        <div className="flex flex-1 items-center justify-center text-meta text-text-muted">loading…</div>
      ) : shown.length === 0 ? (
        <Empty
          icon={Globe2}
          title={onlyMine ? "Nothing here touches your holdings right now." : "No events in this window."}
          hint={onlyMine ? "Turn off the holdings filter to see everything." : "Try a different type."}
        />
      ) : (
        <Panel scroll className="min-h-0 flex-1 border-0">
          <ul className="divide-y divide-border-subtle">
            {shown.map((e) => {
              const hits = touching(e);
              return (
                <li key={e.id} className="px-4 py-3">
                  <a
                    href={e.primary_url || undefined}
                    target={e.primary_url ? "_blank" : undefined}
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
                      {e.timestamp_trust === "observed" ? "seen " : ""}
                      {formatAgo(e.published_at || e.discovered_at)}
                    </span>
                    {e.event_type && e.event_type !== "UNCLASSIFIED" && (
                      <Pill tone="muted">{e.event_type.replace(/_/g, " ").toLowerCase()}</Pill>
                    )}
                    {e.official && <Pill tone="brand">official</Pill>}
                    {(e.importance ?? 0) >= 7 && <Pill tone="down">high</Pill>}
                    {(e.sectors ?? []).map((s) => (
                      <Pill key={s} tone="neutral">
                        {s.replace(/^US: /, "")}
                      </Pill>
                    ))}
                  </div>
                  {hits.length > 0 && (
                    <div className="mt-2 flex flex-wrap items-center gap-1.5 border-l-2 border-brand/40 pl-2">
                      <span className="font-mono text-micro uppercase tracking-wider text-brand">
                        touches
                      </span>
                      {hits.map((s) => {
                        const value = valueBySymbol.get(s);
                        return (
                          <button
                            key={s}
                            type="button"
                            onClick={() => onSelect(s)}
                            className={
                              "font-mono text-meta " +
                              (value != null
                                ? "text-text-primary hover:text-brand"
                                : "text-text-muted hover:text-brand")
                            }
                            title={value != null ? "A real position -- not just watched" : "On your watchlist"}
                          >
                            {s}
                            {value != null && (
                              <span className="ml-1 text-text-muted">{money(value, s)}</span>
                            )}
                          </button>
                        );
                      })}
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        </Panel>
      )}
    </div>
  );
}
