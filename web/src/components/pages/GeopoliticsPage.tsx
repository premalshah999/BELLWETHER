import { useQuery } from "@tanstack/react-query";
import { Globe2 } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type MarketEvent } from "../../lib/api";
import { formatAgo } from "../../lib/format";
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
 * NSE industries and, prefixed "US: ", GICS ones) and every symbol on the
 * watchlist has a known industry to compare them against.
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

export function GeopoliticsPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const [type, setType] = useState<(typeof TYPES)[number]["value"]>("");
  const [onlyMine, setOnlyMine] = useState(false);

  const { data, isLoading } = useQuery({
    queryKey: ["geopolitics-events", type],
    queryFn: () => api.events({ type: type || ALL_TYPES, universe: "all", limit: 150, order: "arrival" }),
    refetchInterval: 60_000,
  });
  const events = useMemo(() => data?.events ?? [], [data]);

  const { data: watchlist } = useQuery({
    queryKey: ["watchlist-for-geopolitics"],
    queryFn: api.watchlist,
    staleTime: 60_000,
  });
  const holdings = useMemo(() => watchlist?.items ?? [], [watchlist]);

  const { data: sectorData } = useQuery({
    queryKey: ["holding-sectors", holdings.map((h) => h.symbol).join(",")],
    queryFn: () => api.symbolSectors(holdings.map((h) => h.symbol)),
    enabled: holdings.length > 0,
    staleTime: 5 * 60_000,
  });
  // symbol -> its industry, already spelled to match an event's own Sectors.
  const bySymbol = sectorData?.sectors ?? {};
  // industry -> which held symbols sit in it, the reverse index a card needs.
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
    return [...hit];
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
          disabled={holdings.length === 0}
          className={
            "border px-2 py-1 font-mono text-micro uppercase tracking-wider transition-colors disabled:opacity-40 " +
            (onlyMine
              ? "border-brand/40 bg-brand-muted text-brand"
              : "border-border-subtle text-text-muted hover:text-text-primary")
          }
          title={
            holdings.length === 0
              ? "Add something to your watchlist to filter by it"
              : "Show only events that reach a sector you hold"
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
                      {hits.map((s) => (
                        <button
                          key={s}
                          type="button"
                          onClick={() => onSelect(s)}
                          className="font-mono text-meta text-text-secondary hover:text-brand"
                        >
                          {s}
                        </button>
                      ))}
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
