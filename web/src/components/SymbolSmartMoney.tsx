import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import { money } from "./charts/BarList";

/**
 * Who has been trading one stock: open-market insider trades over the past
 * year, and followed funds' latest quarterly moves.
 */
export function SymbolSmartMoney({ symbol }: { symbol: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ["symbol-smartmoney", symbol],
    queryFn: () => api.symbolSmartMoney(symbol),
    staleTime: 10 * 60_000,
    retry: false,
  });
  if (isLoading) return <div className="skeleton mx-4 my-3 h-24" />;
  if (!data) return null;
  const trades = data.trades ?? [];
  const buys = trades.filter((t) => t.code === "P");
  const sells = trades.filter((t) => t.code === "S");
  const sum = (xs: typeof trades) => xs.reduce((a, t) => a + (t.value ?? 0), 0);
  const moves = (data.funds ?? []).filter((m) => m.kind !== "held");
  return (
    <section className="border-t border-border-subtle px-4 py-4">
      <div className="flex items-baseline justify-between">
        <h3 className="text-meta font-semibold text-text-secondary">Who's buying {symbol}</h3>
        <Link to={`/smartmoney/insiders?symbol=${encodeURIComponent(symbol)}&side=`} className="text-micro font-medium text-accent-text hover:underline">
          All trades
        </Link>
      </div>
      <div className="mt-2 grid grid-cols-2 gap-3">
        <div className="rounded-lg bg-bg-field px-3 py-2">
          <p className="text-micro text-text-muted">Insiders bought, 1y</p>
          <p className="font-num text-[15px] font-semibold text-semantic-up">{money(sum(buys))}</p>
          <p className="text-micro text-text-muted">{new Set(buys.map((t) => t.owner_name)).size} buyers</p>
        </div>
        <div className="rounded-lg bg-bg-field px-3 py-2">
          <p className="text-micro text-text-muted">Insiders sold, 1y</p>
          <p className="font-num text-[15px] font-semibold text-semantic-down">{money(sum(sells))}</p>
          <p className="text-micro text-text-muted">{new Set(sells.map((t) => t.owner_name)).size} sellers</p>
        </div>
      </div>
      {buys.slice(0, 3).map((t) => (
        <p key={t.accession + t.shares} className="mt-2 text-meta text-text-secondary">
          <span className="text-text-primary">{t.owner_name}</span> ({t.role}) bought {money(t.value)} on{" "}
          {new Date(t.tx_date).toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" })}
        </p>
      ))}
      {moves.slice(0, 4).map((m) => (
        <p key={m.fund_cik} className="mt-2 text-meta text-text-secondary">
          <span className="text-text-primary">{m.manager}</span>{" "}
          {m.kind === "new" ? "opened a position" : m.kind === "added" ? `added ${m.change_pct.toFixed(0)}%` : m.kind === "trimmed" ? `trimmed ${Math.abs(m.change_pct).toFixed(0)}%` : "sold out"} in {m.period}
        </p>
      ))}
      {trades.length === 0 && moves.length === 0 && (
        <p className="mt-2 text-meta text-text-muted">No insider trades or followed-fund moves on record.</p>
      )}
    </section>
  );
}
