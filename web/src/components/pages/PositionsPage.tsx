import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Briefcase, Plus, Trash2, X } from "lucide-react";
import { Fragment, useMemo, useState } from "react";
import { api, ApiError, type Position } from "../../lib/api";
import { venueOf } from "../../lib/symbol";
import { Empty } from "../ui/Empty";
import { PageHeader, SkeletonRows } from "../ui/controls";

function currencyOf(symbol: string): "₹" | "$" {
  return venueOf(symbol) === "NSE" ? "₹" : "$";
}

function money(n: number, currency: string) {
  return `${currency}${Math.abs(n).toLocaleString(undefined, { maximumFractionDigits: 2 })}`;
}

/**
 * What you actually hold, priced live.
 *
 * Everything else in this app -- the scanner, the news feed, Geopolitics --
 * answers questions about symbols the operator expressed interest in. None
 * of it knows how much of anything is actually owned, so "does this event
 * touch my money" has only ever been answerable against a watchlist, which
 * mixes real exposure with names someone is merely tracking. This page is
 * what makes that a real question with a real answer.
 *
 * Cost basis is average-cost, not FIFO lots -- see
 * internal/storage/postgres/positions.go for what that trades off. Totals
 * are grouped by currency rather than summed across them: adding dollars to
 * rupees is not a rounding shortcut, it is a wrong number.
 */
export function PositionsPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);
  const [closingID, setClosingID] = useState<number | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ["positions"],
    queryFn: api.positions,
    refetchInterval: 30_000,
  });
  const positions = useMemo(() => data?.positions ?? [], [data]);

  const refresh = () => qc.invalidateQueries({ queryKey: ["positions"] });

  const del = useMutation({
    mutationFn: (id: number) => api.deletePosition(id),
    onSuccess: refresh,
  });

  const totalsByCurrency = useMemo(() => {
    const out: Record<string, { value: number; pnl: number }> = {};
    for (const p of positions) {
      if (p.market_value == null) continue;
      const cur = currencyOf(p.symbol);
      const t = (out[cur] ??= { value: 0, pnl: 0 });
      t.value += p.market_value;
      t.pnl += p.unrealized_pnl ?? 0;
    }
    return out;
  }, [positions]);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <PageHeader
        title="Positions"
        subtitle={
          positions.length === 0 ? (
            "What you hold, so Bellwether can tell you when news reaches real money."
          ) : (
            <>
              {positions.length} {positions.length === 1 ? "position" : "positions"}
              {Object.entries(totalsByCurrency).map(([cur, t]) => (
                <span key={cur}>
                  , worth <span className="font-medium text-text-primary">{money(t.value, cur)}</span>,{" "}
                  <span className={t.pnl >= 0 ? "text-semantic-up" : "text-semantic-down"}>
                    {t.pnl >= 0 ? "up " : "down "}
                    {money(Math.abs(t.pnl), cur)}
                  </span>
                </span>
              ))}
              .
            </>
          )
        }
        actions={
          <button type="button" onClick={() => setAdding((v) => !v)} className={adding ? "action-secondary" : "action-primary"}>
            {adding ? <X size={15} /> : <Plus size={15} />} {adding ? "Cancel" : "Add a position"}
          </button>
        }
      />

      {adding && (
        <AddPositionForm
          onDone={() => {
            setAdding(false);
            refresh();
          }}
          onCancel={() => setAdding(false)}
        />
      )}

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <SkeletonRows count={5} height={48} />
        ) : positions.length === 0 ? (
          <Empty
            icon={Briefcase}
            title="No positions yet."
            hint="Add what you hold. Policy and news pages will then flag events that reach it, with the value at stake."
            action={
              !adding && (
                <button type="button" onClick={() => setAdding(true)} className="action-primary">
                  <Plus size={15} /> Add a position
                </button>
              )
            }
          />
        ) : (
          <table className="w-full max-w-[1180px] border-collapse">
            <thead className="sticky top-0 z-10 bg-bg-panel">
              <tr className="border-b border-border-subtle">
                <Th className="w-[140px]">symbol</Th>
                <Th className="w-[120px]">account</Th>
                <Th right className="w-[100px]">qty</Th>
                <Th right className="w-[100px]">cost</Th>
                <Th right className="w-[100px]">price</Th>
                <Th right className="w-[120px]">value</Th>
                <Th right className="w-[140px]">unrealized</Th>
                <Th className="w-[90px]"></Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border-subtle">
              {positions.map((p) => (
                <Fragment key={p.id}>
                  <Row
                    p={p}
                    onSelect={onSelect}
                    onClose={() => setClosingID(closingID === p.id ? null : p.id)}
                    onDelete={() => del.mutate(p.id)}
                    closing={closingID === p.id}
                  />
                  {closingID === p.id && (
                    <tr>
                      <td colSpan={8} className="bg-bg-base px-4 py-3">
                        <ClosePositionForm
                          position={p}
                          onDone={() => {
                            setClosingID(null);
                            refresh();
                          }}
                          onCancel={() => setClosingID(null)}
                        />
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function Row({
  p,
  onSelect,
  onClose,
  onDelete,
  closing,
}: {
  p: Position;
  onSelect: (symbol: string) => void;
  onClose: () => void;
  onDelete: () => void;
  closing: boolean;
}) {
  const cur = currencyOf(p.symbol);
  return (
    <tr className="group hover:bg-bg-panel-hover">
      <td className="px-3 py-2">
        <button
          type="button"
          onClick={() => onSelect(p.symbol)}
          className="text-ui text-text-primary hover:text-brand"
        >
          {p.symbol}
        </button>
      </td>
      <td className="px-3 py-2 font-mono text-meta text-text-muted">{p.account || "—"}</td>
      <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
        {p.quantity.toLocaleString()}
      </td>
      <td className="px-3 py-2 text-right font-mono text-ui text-text-muted">
        {money(p.cost_basis, cur)}
      </td>
      <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
        {p.price == null ? (
          <span title={p.price_error} className="text-text-muted">
            —
          </span>
        ) : (
          money(p.price, cur)
        )}
        {p.price_stale && <span className="ml-1 text-micro text-semantic-down">stale</span>}
      </td>
      <td className="px-3 py-2 text-right font-mono text-ui text-text-secondary">
        {p.market_value == null ? "—" : money(p.market_value, cur)}
      </td>
      <td
        className={
          "px-3 py-2 text-right font-mono text-ui " +
          (p.unrealized_pnl == null
            ? "text-text-muted"
            : p.unrealized_pnl >= 0
              ? "text-semantic-up"
              : "text-semantic-down")
        }
      >
        {p.unrealized_pnl == null
          ? "—"
          : `${p.unrealized_pnl >= 0 ? "+" : "−"}${money(p.unrealized_pnl, cur)}${
              p.unrealized_pct != null ? ` (${p.unrealized_pct >= 0 ? "+" : ""}${p.unrealized_pct.toFixed(1)}%)` : ""
            }`}
      </td>
      <td className="px-3 py-2">
        <div className="flex items-center justify-end gap-2 opacity-0 transition-opacity group-hover:opacity-100">
          <button
            type="button"
            onClick={onClose}
            className={
              "font-mono text-micro " +
              (closing ? "text-brand" : "text-text-muted hover:text-text-primary")
            }
          >
            close
          </button>
          <button type="button" onClick={onDelete} title="Delete (no trade recorded)">
            <Trash2 size={11} className="text-text-muted hover:text-semantic-down" />
          </button>
        </div>
      </td>
    </tr>
  );
}

const inputClass =
  "w-full border border-border-subtle bg-bg-panel px-2 py-1 text-meta outline-none focus:border-brand";
const labelClass = "block font-mono text-micro text-text-muted mb-1";

function AddPositionForm({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const [symbol, setSymbol] = useState("");
  const [quantity, setQuantity] = useState("");
  const [costBasis, setCostBasis] = useState("");
  const [openedAt, setOpenedAt] = useState("");
  const [account, setAccount] = useState("");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);

  const add = useMutation({
    mutationFn: () =>
      api.addPosition({
        symbol: symbol.trim().toUpperCase(),
        quantity: Number(quantity),
        cost_basis: Number(costBasis),
        opened_at: openedAt || undefined,
        account: account.trim() || undefined,
        notes: notes.trim() || undefined,
      }),
    onSuccess: onDone,
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not add the position."),
  });

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        setError(null);
        if (!symbol.trim() || !quantity || !costBasis) {
          setError("Symbol, quantity and cost basis are required.");
          return;
        }
        add.mutate();
      }}
      className="border-b border-border-subtle bg-bg-base px-4 py-3"
    >
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-28">
          <label className={labelClass}>symbol</label>
          <input
            autoFocus
            value={symbol}
            onChange={(e) => setSymbol(e.target.value)}
            placeholder="AAPL"
            className={inputClass}
          />
        </div>
        <div className="w-24">
          <label className={labelClass}>quantity</label>
          <input
            value={quantity}
            onChange={(e) => setQuantity(e.target.value)}
            inputMode="decimal"
            placeholder="100"
            className={inputClass}
          />
        </div>
        <div className="w-28">
          <label className={labelClass}>cost basis</label>
          <input
            value={costBasis}
            onChange={(e) => setCostBasis(e.target.value)}
            inputMode="decimal"
            placeholder="182.40"
            className={inputClass}
          />
        </div>
        <div className="w-36">
          <label className={labelClass}>opened</label>
          <input
            type="date"
            value={openedAt}
            onChange={(e) => setOpenedAt(e.target.value)}
            className={inputClass}
          />
        </div>
        <div className="w-32">
          <label className={labelClass}>account</label>
          <input
            value={account}
            onChange={(e) => setAccount(e.target.value)}
            placeholder="taxable"
            className={inputClass}
          />
        </div>
        <div className="min-w-[160px] flex-1">
          <label className={labelClass}>notes</label>
          <input value={notes} onChange={(e) => setNotes(e.target.value)} className={inputClass} />
        </div>
        <div className="flex gap-2">
          <button
            type="submit"
            disabled={add.isPending}
            className="border border-brand/40 bg-brand-muted px-3 py-1 font-mono text-micro text-brand disabled:opacity-50"
          >
            add
          </button>
          <button
            type="button"
            onClick={onCancel}
            className="border border-border-subtle px-3 py-1 font-mono text-micro text-text-muted hover:text-text-primary"
          >
            cancel
          </button>
        </div>
      </div>
      {error && <p className="mt-2 text-meta text-semantic-down">{error}</p>}
    </form>
  );
}

function ClosePositionForm({
  position,
  onDone,
  onCancel,
}: {
  position: Position;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [quantity, setQuantity] = useState(String(position.quantity));
  const [exitPrice, setExitPrice] = useState(position.price != null ? String(position.price) : "");
  const [closedAt, setClosedAt] = useState("");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const cur = currencyOf(position.symbol);

  const close = useMutation({
    mutationFn: () =>
      api.closePosition(position.id, {
        quantity: Number(quantity),
        exit_price: Number(exitPrice),
        closed_at: closedAt || undefined,
        notes: notes.trim() || undefined,
      }),
    onSuccess: onDone,
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not close the position."),
  });

  const qtyNum = Number(quantity);
  const priceNum = Number(exitPrice);
  const previewPnl =
    Number.isFinite(qtyNum) && Number.isFinite(priceNum) && qtyNum > 0
      ? (priceNum - position.cost_basis) * qtyNum
      : null;

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        setError(null);
        if (!quantity || !exitPrice) {
          setError("Quantity and exit price are required.");
          return;
        }
        close.mutate();
      }}
    >
      <p className="mb-2 font-mono text-micro text-text-muted">
        close {position.symbol} · {position.quantity.toLocaleString()} held at {money(position.cost_basis, cur)}
      </p>
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-28">
          <label className={labelClass}>quantity</label>
          <input
            autoFocus
            value={quantity}
            onChange={(e) => setQuantity(e.target.value)}
            inputMode="decimal"
            className={inputClass}
          />
        </div>
        <div className="w-28">
          <label className={labelClass}>exit price</label>
          <input
            value={exitPrice}
            onChange={(e) => setExitPrice(e.target.value)}
            inputMode="decimal"
            className={inputClass}
          />
        </div>
        <div className="w-36">
          <label className={labelClass}>closed</label>
          <input
            type="date"
            value={closedAt}
            onChange={(e) => setClosedAt(e.target.value)}
            className={inputClass}
          />
        </div>
        <div className="min-w-[160px] flex-1">
          <label className={labelClass}>notes</label>
          <input value={notes} onChange={(e) => setNotes(e.target.value)} className={inputClass} />
        </div>
        {previewPnl != null && (
          <span
            className={
              "font-mono text-meta " + (previewPnl >= 0 ? "text-semantic-up" : "text-semantic-down")
            }
          >
            {previewPnl >= 0 ? "+" : "−"}
            {money(previewPnl, cur)} realized
          </span>
        )}
        <div className="flex gap-2">
          <button
            type="submit"
            disabled={close.isPending}
            className="border border-semantic-down/40 bg-bg-panel px-3 py-1 font-mono text-micro text-semantic-down disabled:opacity-50"
          >
            confirm close
          </button>
          <button
            type="button"
            onClick={onCancel}
            className="border border-border-subtle px-3 py-1 font-mono text-micro text-text-muted hover:text-text-primary"
          >
            cancel
          </button>
        </div>
      </div>
      {error && <p className="mt-2 text-meta text-semantic-down">{error}</p>}
    </form>
  );
}

function Th({
  children,
  right,
  className = "",
}: {
  children?: React.ReactNode;
  right?: boolean;
  className?: string;
}) {
  return (
    <th
      className={
        "px-3 py-2 font-mono text-meta font-medium first-letter:uppercase text-text-muted " +
        (right ? "text-right " : "text-left ") +
        className
      }
    >
      {children}
    </th>
  );
}
