import { useQuery } from "@tanstack/react-query";
import { BookOpen } from "lucide-react";
import { useMemo } from "react";
import { api, type JournalEntry } from "../../lib/api";
import { venueOf } from "../../lib/symbol";
import { Empty } from "../ui/Empty";
import { Panel } from "../ui/Panel";
import { Pill } from "../ui/Pill";

function money(n: number, symbol: string) {
  const cur = venueOf(symbol) === "NSE" ? "₹" : "$";
  return `${n >= 0 ? "" : "−"}${cur}${Math.abs(n).toLocaleString(undefined, { maximumFractionDigits: 0 })}`;
}

type Bucket = "official" | "unofficial" | "none";

function bucketOf(e: JournalEntry): Bucket {
  if (!e.catalyst) return "none";
  return e.catalyst.official ? "official" : "unofficial";
}

/**
 * Of the trades that actually closed, which were preceded by something the
 * event archive classified -- and did having one, especially an official
 * one, correlate with a better outcome?
 *
 * No trading journal attributes against a catalyst archive (they only have
 * the trades), and no alt-data platform attributes against real trades
 * (they only have the events). This page is the one place that holds both
 * halves. See internal/storage/postgres/journal.go for the attribution
 * method -- document-level, closest-preceding-event, never a future one.
 */
export function JournalPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const { data, isLoading } = useQuery({
    queryKey: ["journal"],
    queryFn: () => api.journal(200),
    staleTime: 60_000,
  });
  const trades = useMemo(() => data?.trades ?? [], [data]);

  const stats = useMemo(() => {
    const byBucket: Record<Bucket, { n: number; wins: number; pnl: number }> = {
      official: { n: 0, wins: 0, pnl: 0 },
      unofficial: { n: 0, wins: 0, pnl: 0 },
      none: { n: 0, wins: 0, pnl: 0 },
    };
    // Realized P&L is grouped by currency, never summed across them --
    // adding dollars to rupees is a wrong number, not a rounding shortcut
    // (see PositionsPage for the same rule applied to unrealized P&L).
    const pnlByCurrency = new Map<string, number>();
    let totalWins = 0;
    for (const t of trades) {
      const b = byBucket[bucketOf(t)];
      b.n++;
      b.pnl += t.realized_pnl;
      if (t.realized_pnl > 0) b.wins++;
      const cur = venueOf(t.symbol) === "NSE" ? "₹" : "$";
      pnlByCurrency.set(cur, (pnlByCurrency.get(cur) ?? 0) + t.realized_pnl);
      if (t.realized_pnl > 0) totalWins++;
    }
    return { byBucket, pnlByCurrency, totalWins, total: trades.length };
  }, [trades]);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <div className="flex h-10 shrink-0 items-center gap-3 border-b border-border-subtle px-4">
        <span className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
          Trade Journal
        </span>
        <span className="ml-auto font-mono text-meta text-text-muted">{trades.length} closed trades</span>
      </div>

      {trades.length === 0 && !isLoading ? (
        <Empty
          icon={BookOpen}
          title="No closed trades yet."
          hint="Close a position on the Positions page and it will appear here, with whatever the event archive can attribute to it."
        />
      ) : (
        <>
          <div className="grid grid-cols-2 divide-x divide-y divide-border-subtle border-b border-border-subtle sm:grid-cols-4 sm:divide-y-0">
            <div className="bg-bg-panel px-4 py-4">
              <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
                Total realized
              </p>
              {stats.pnlByCurrency.size === 0 ? (
                <p className="mt-1 font-mono text-hero leading-none text-text-primary">—</p>
              ) : (
                <div className="mt-1 flex flex-wrap items-baseline gap-x-2">
                  {[...stats.pnlByCurrency.entries()].map(([cur, pnl]) => (
                    <span
                      key={cur}
                      className={
                        "font-mono text-hero leading-none tabular-nums " +
                        (pnl > 0 ? "text-semantic-up" : pnl < 0 ? "text-semantic-down" : "text-text-primary")
                      }
                    >
                      {pnl >= 0 ? "" : "−"}
                      {cur}
                      {Math.abs(pnl).toLocaleString(undefined, { maximumFractionDigits: 0 })}
                    </span>
                  ))}
                </div>
              )}
              <p className="mt-1.5 max-w-[36ch] text-meta leading-relaxed text-text-muted">
                {stats.total} closed trade(s), overall
              </p>
            </div>
            <Metric
              label="Win rate"
              value={stats.total > 0 ? `${((stats.totalWins / stats.total) * 100).toFixed(0)}%` : "—"}
              hint="Share of closed trades with positive realized P&L"
            />
            <BucketMetric
              label="With an official catalyst"
              bucket={stats.byBucket.official}
              hint="Entered within 5 days of an exchange or regulatory filing this archive classified"
            />
            <BucketMetric
              label="With no catalyst found"
              bucket={stats.byBucket.none}
              hint="Nothing in the archive named this symbol in the 5 days before entry"
            />
          </div>

          <Panel scroll className="min-h-0 flex-1 border-0">
            {isLoading ? (
              <p className="px-4 py-6 font-mono text-meta text-text-muted">loading…</p>
            ) : (
              <ul className="divide-y divide-border-subtle">
                {trades.map((t) => (
                  <li key={t.id} className="px-4 py-3">
                    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                      <button
                        type="button"
                        onClick={() => onSelect(t.symbol)}
                        className="text-ui text-text-primary hover:text-brand"
                      >
                        {t.symbol}
                      </button>
                      <span className="font-mono text-meta text-text-muted">
                        {t.opened_at} → {t.closed_at}
                      </span>
                      <span className="font-mono text-meta text-text-muted">
                        {t.quantity.toLocaleString()} @ {money(t.entry_price, t.symbol)} → {money(t.exit_price, t.symbol)}
                      </span>
                      <span
                        className={
                          "ml-auto font-mono text-ui " +
                          (t.realized_pnl >= 0 ? "text-semantic-up" : "text-semantic-down")
                        }
                      >
                        {money(t.realized_pnl, t.symbol)}
                      </span>
                    </div>
                    {t.notes && (
                      <p className="mt-1 text-meta text-text-muted">{t.notes}</p>
                    )}
                    <div className="mt-2 flex flex-wrap items-start gap-1.5 border-l-2 border-border-subtle pl-2">
                      {t.catalyst ? (
                        <>
                          <Pill tone={t.catalyst.official ? "brand" : "muted"}>
                            {t.catalyst.official ? "official" : "unofficial"} catalyst
                          </Pill>
                          <Pill tone="neutral">{t.catalyst.event_type.replace(/_/g, " ").toLowerCase()}</Pill>
                          <span className="font-mono text-meta text-text-muted">
                            {t.catalyst.days_before_entry === 0
                              ? "same day"
                              : `${t.catalyst.days_before_entry}d before entry`}
                          </span>
                          <span className="w-full text-meta text-text-secondary sm:w-auto">
                            {t.catalyst.headline}
                          </span>
                        </>
                      ) : (
                        <span className="font-mono text-meta text-text-muted">
                          no catalyst found in the archive
                        </span>
                      )}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </>
      )}
    </div>
  );
}

function Metric({
  label,
  value,
  hint,
  tone,
}: {
  label: string;
  value: string;
  hint: string;
  tone?: "up" | "down";
}) {
  return (
    <div className="bg-bg-panel px-4 py-4">
      <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">{label}</p>
      <p
        className={
          "mt-1 font-mono text-hero leading-none tabular-nums " +
          (tone === "up" ? "text-semantic-up" : tone === "down" ? "text-semantic-down" : "text-text-primary")
        }
      >
        {value}
      </p>
      <p className="mt-1.5 max-w-[36ch] text-meta leading-relaxed text-text-muted">{hint}</p>
    </div>
  );
}

function BucketMetric({
  label,
  bucket,
  hint,
}: {
  label: string;
  bucket: { n: number; wins: number; pnl: number };
  hint: string;
}) {
  const winRate = bucket.n > 0 ? (bucket.wins / bucket.n) * 100 : null;
  return (
    <div className="bg-bg-panel px-4 py-4">
      <p className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">{label}</p>
      <p className="mt-1 font-mono text-hero leading-none tabular-nums text-text-primary">
        {winRate == null ? "—" : `${winRate.toFixed(0)}%`}
      </p>
      <p className="mt-1 font-mono text-meta text-text-muted">
        {bucket.n} trade{bucket.n === 1 ? "" : "s"}
      </p>
      <p className="mt-1.5 max-w-[36ch] text-meta leading-relaxed text-text-muted">{hint}</p>
    </div>
  );
}
