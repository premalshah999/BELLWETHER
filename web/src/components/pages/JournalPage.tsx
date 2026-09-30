import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen, Plus, Trash2, X } from "lucide-react";
import { useMemo, useState } from "react";
import { api, ApiError, type JournalEntry } from "../../lib/api";
import { inputClass, labelClass } from "./PositionsPage";
import { Empty } from "../ui/Empty";
import { PageHeader } from "../ui/controls";
import { Panel } from "../ui/Panel";
import { Pill } from "../ui/Pill";

function money(n: number, _symbol?: string) {
  const cur = "$";
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
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);
  const del = useMutation({
    mutationFn: (id: number) => api.deleteTrade(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["journal"] }),
  });

  const stats = useMemo(() => {
    const byBucket: Record<Bucket, { n: number; wins: number; pnl: number }> = {
      official: { n: 0, wins: 0, pnl: 0 },
      unofficial: { n: 0, wins: 0, pnl: 0 },
      none: { n: 0, wins: 0, pnl: 0 },
    };
    // Realized P&L is grouped by currency, never summed across them --
    // (see PositionsPage for the same rule applied to unrealized P&L).
    const pnlByCurrency = new Map<string, number>();
    let totalWins = 0;
    for (const t of trades) {
      const b = byBucket[bucketOf(t)];
      b.n++;
      b.pnl += t.realized_pnl;
      if (t.realized_pnl > 0) b.wins++;
      const cur = "$";
      pnlByCurrency.set(cur, (pnlByCurrency.get(cur) ?? 0) + t.realized_pnl);
      if (t.realized_pnl > 0) totalWins++;
    }
    return { byBucket, pnlByCurrency, totalWins, total: trades.length };
  }, [trades]);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="Journal"
        subtitle={
          trades.length === 0
            ? "Closed trades, with whatever the event archive can attribute to each."
            : `${trades.length} closed ${trades.length === 1 ? "trade" : "trades"}, each with the events that surrounded it.`
        }
        actions={
          <button type="button" onClick={() => setAdding(!adding)} className="action-primary">
            {adding ? <X size={15} /> : <Plus size={15} />} {adding ? "Cancel" : "Record a trade"}
          </button>
        }
      />

      {adding && (
        <RecordTradeForm
          onDone={() => {
            setAdding(false);
            qc.invalidateQueries({ queryKey: ["journal"] });
          }}
        />
      )}

      {trades.length === 0 && !isLoading ? (
        <Empty
          icon={BookOpen}
          title="No closed trades yet."
          hint="Record a trade you closed, or close a position on the Positions page. Each one is set against the news that came before its entry."
        />
      ) : (
        <>
          <div className="grid grid-cols-2 divide-x divide-y divide-border-subtle border-b border-border-subtle sm:grid-cols-4 sm:divide-y-0">
            <div className="bg-bg-panel px-4 py-4">
              <p className="font-mono text-micro text-text-muted">
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
                        {t.quantity.toLocaleString()} @ ${t.entry_price.toFixed(2)} → ${t.exit_price.toFixed(2)} (
                        {(((t.exit_price - t.entry_price) / t.entry_price) * 100).toFixed(1)}%)
                      </span>
                      {t.account && <Pill tone="muted">{t.account}</Pill>}
                      <span
                        className={
                          "ml-auto font-mono text-ui " +
                          (t.realized_pnl >= 0 ? "text-semantic-up" : "text-semantic-down")
                        }
                      >
                        {money(t.realized_pnl, t.symbol)}
                      </span>
                      <button
                        type="button"
                        onClick={() => del.mutate(t.id)}
                        title="Delete this trade"
                        aria-label={`Delete the ${t.symbol} trade`}
                        className="text-text-muted hover:text-semantic-down"
                      >
                        <Trash2 size={13} />
                      </button>
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
      <p className="font-mono text-micro text-text-muted">{label}</p>
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
      <p className="font-mono text-micro text-text-muted">{label}</p>
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

function RecordTradeForm({ onDone }: { onDone: () => void }) {
  const today = new Date().toISOString().slice(0, 10);
  const [f, setF] = useState({ symbol: "", quantity: "", entry: "", exit: "", opened: "", closed: today, account: "", notes: "" });
  const [error, setError] = useState<string | null>(null);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const save = useMutation({
    mutationFn: () =>
      api.recordTrade({
        symbol: f.symbol.trim().toUpperCase(),
        quantity: Number(f.quantity),
        entry_price: Number(f.entry),
        exit_price: Number(f.exit),
        opened_at: f.opened,
        closed_at: f.closed,
        account: f.account.trim() || undefined,
        notes: f.notes.trim() || undefined,
      }),
    onSuccess: onDone,
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not record the trade."),
  });
  const field = (k: keyof typeof f, label: string, width: string, extra: Record<string, string> = {}) => (
    <div className={width}>
      <label className={labelClass}>{label}</label>
      <input value={f[k]} onChange={set(k)} className={inputClass} {...extra} />
    </div>
  );
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        setError(null);
        if (!f.symbol.trim() || !f.quantity || !f.entry || !f.exit || !f.opened || !f.closed) {
          setError("Symbol, quantity, both prices and both dates are required.");
          return;
        }
        save.mutate();
      }}
      className="border-b border-border-subtle bg-bg-base px-4 py-3"
    >
      <div className="flex flex-wrap items-end gap-3">
        {field("symbol", "symbol", "w-28", { placeholder: "AAPL", autoFocus: "true" })}
        {field("quantity", "quantity", "w-24", { inputMode: "decimal", placeholder: "100" })}
        {field("entry", "entry price", "w-28", { inputMode: "decimal", placeholder: "182.40" })}
        {field("exit", "exit price", "w-28", { inputMode: "decimal", placeholder: "195.10" })}
        {field("opened", "opened", "w-36", { type: "date" })}
        {field("closed", "closed", "w-36", { type: "date" })}
        {field("account", "account", "w-32", { placeholder: "optional" })}
        {field("notes", "why you took it", "min-w-[200px] flex-1", { placeholder: "optional" })}
        <button type="submit" disabled={save.isPending} className="action-primary">
          {save.isPending ? "Saving…" : "Record"}
        </button>
      </div>
      {error && <p className="mt-2 text-meta text-semantic-down">{error}</p>}
    </form>
  );
}
