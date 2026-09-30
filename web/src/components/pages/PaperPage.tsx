import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, Pause, Play, Plus, Settings2, Trash2, Wallet, X } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  api,
  ApiError,
  paperApi,
  type Cents,
  type EquityPoint,
  type OrderType,
  type PaperAgent,
  type PaperOrder,
  type PaperOrderRequest,
  type PaperSettings,
  type PaperWallet,
} from "../../lib/api";
import { formatDateTime } from "../../lib/format";
import { useUrlState } from "../../lib/url";
import { Drawer, PageHeader, Segmented, Select, SkeletonRows, Switch } from "../ui/controls";
import { Pill } from "../ui/Pill";

const TABS = [
  { value: "overview", label: "Overview" },
  { value: "trade", label: "Trade" },
  { value: "autopilot", label: "Autopilot" },
  { value: "performance", label: "Performance" },
  { value: "activity", label: "Activity" },
] as const;
type Tab = (typeof TABS)[number]["value"];

export const usd = (c: Cents | undefined, digits = 2) =>
  c === undefined
    ? "—"
    : `${c < 0 ? "−" : ""}$${(Math.abs(c) / 100).toLocaleString(undefined, { minimumFractionDigits: digits, maximumFractionDigits: digits })}`;
const pct = (n: number | undefined, digits = 2) => (n === undefined ? "—" : `${n > 0 ? "+" : n < 0 ? "−" : ""}${Math.abs(n).toFixed(digits)}%`);
const tone = (n: number | undefined) => (n === undefined || n === 0 ? "text-text-primary" : n > 0 ? "text-semantic-up" : "text-semantic-down");
const key = () => (crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`);
const errText = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback);
const inputCls = "h-[34px] w-full rounded-md border border-border-subtle bg-bg-field px-3 text-ui outline-none focus:border-brand";
const labelCls = "mb-1 block text-meta text-text-muted";

/**
 * Paper trading: simulated money, real prices, real rules. The point is to
 * find out whether a rule, a strategy or an AI makes money before any real
 * money is involved, so it is as strict as a real account.
 */
export function PaperPage({ onSelect }: { onSelect: (s: string) => void }) {
  const navigate = useNavigate();
  const { tab: routeTab } = useParams();
  const tab: Tab = (TABS.find((t) => t.value === routeTab)?.value ?? "overview") as Tab;
  const [walletParam, setWalletParam] = useUrlState("w", "");
  const list = useQuery({ queryKey: ["paper-wallets"], queryFn: paperApi.wallets, refetchInterval: 30_000 });
  const active = (list.data?.wallets ?? []).filter((x) => x.status === "active");
  const wallet = active.find((x) => String(x.id) === walletParam) ?? active[0];
  const [funding, setFunding] = useState<"deposit" | "withdrawal" | null>(null);
  const [settings, setSettings] = useState(false);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="Paper trading"
        subtitle="Simulated money at real market prices, with slippage, fees and a broker's rules — to learn whether a strategy makes money before any real money does."
        actions={
          wallet ? (
            <div className="flex flex-wrap items-center gap-2">
              {active.length > 1 && (
                <Select
                  label="Wallet"
                  value={String(wallet.id)}
                  onChange={setWalletParam}
                  options={active.map((x) => ({ value: String(x.id), label: x.name }))}
                  align="right"
                />
              )}
              <button type="button" onClick={() => setFunding("deposit")} className="action-primary">
                <Plus size={15} /> Add funds
              </button>
              <button type="button" onClick={() => setFunding("withdrawal")} className="action-secondary">
                Withdraw
              </button>
              <button type="button" onClick={() => setSettings(true)} className="action-secondary" aria-label="Wallet settings">
                <Settings2 size={15} />
              </button>
            </div>
          ) : undefined
        }
      >
        {wallet && (
          <Segmented label="View" value={tab} onChange={(v) => navigate(`/paper/${v}?w=${wallet.id}`)} options={TABS} />
        )}
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {list.isLoading ? (
          <SkeletonRows count={6} height={64} />
        ) : !wallet ? (
          <OpenWallet onOpened={(id) => setWalletParam(String(id))} />
        ) : (
          <>
            <Summary wallet={wallet} />
            {tab === "overview" && <Overview wallet={wallet} onSymbol={onSelect} goTrade={() => navigate(`/paper/trade?w=${wallet.id}`)} />}
            {tab === "trade" && <Ticket wallet={wallet} />}
            {tab === "autopilot" && <Autopilot wallet={wallet} />}
            {tab === "performance" && <PerformanceTab wallet={wallet} />}
            {tab === "activity" && <Activity wallet={wallet} />}
          </>
        )}
      </div>

      {wallet && funding && <Funding wallet={wallet} direction={funding} onClose={() => setFunding(null)} />}
      {wallet && settings && <SettingsDrawer wallet={wallet} onClose={() => setSettings(false)} />}
    </div>
  );
}

function Card({ title, sub, children, actions }: { title?: string; sub?: ReactNode; children: ReactNode; actions?: ReactNode }) {
  return (
    <section className="min-w-0 rounded-xl border border-border-subtle bg-bg-card">
      {(title || actions) && (
        <header className="flex flex-wrap items-start justify-between gap-2 px-5 pb-2 pt-4">
          <div>
            {title && <h2 className="text-[15px] font-semibold text-text-primary">{title}</h2>}
            {sub && <p className="mt-0.5 text-meta text-text-muted">{sub}</p>}
          </div>
          {actions}
        </header>
      )}
      {children}
    </section>
  );
}

function OpenWallet({ onOpened }: { onOpened: (id: number) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("My paper account");
  const [amount, setAmount] = useState(25_000);
  const open = useMutation({
    mutationFn: () => paperApi.createWallet({ name, deposit: amount }),
    onSuccess: (w) => {
      qc.invalidateQueries({ queryKey: ["paper-wallets"] });
      onOpened(w.id);
    },
  });
  return (
    <div className="mx-auto flex max-w-xl flex-col gap-4 px-6 py-12">
      <Wallet size={24} className="text-brand" />
      <h2 className="text-[22px] font-semibold text-text-primary">Open a paper wallet</h2>
      <p className="text-ui leading-relaxed text-text-secondary">
        The money is fictional; everything else is real. Orders fill during market hours at prices the market actually
        traded, pay slippage and the SEC and FINRA fees a broker passes on, and accounts under $25,000 are held to the
        pattern-day-trader rule. Trade it yourself, or let an agent trade it inside limits you set.
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          open.mutate();
        }}
        className="flex flex-col gap-3"
      >
        <div>
          <label className={labelCls} htmlFor="wallet-name">Name</label>
          <input id="wallet-name" value={name} onChange={(e) => setName(e.target.value)} className={inputCls} maxLength={60} />
        </div>
        <div>
          <span className={labelCls}>Starting balance</span>
          <div className="flex flex-wrap gap-2">
            {[10_000, 25_000, 100_000, 1_000_000].map((v) => (
              <button
                key={v}
                type="button"
                onClick={() => setAmount(v)}
                className={"rounded-md border px-3 py-1.5 text-ui " + (amount === v ? "border-brand bg-brand-muted text-text-primary" : "border-border-subtle text-text-secondary")}
              >
                ${v.toLocaleString()}
              </button>
            ))}
          </div>
        </div>
        <button type="submit" disabled={open.isPending || !name.trim()} className="action-primary w-fit">
          {open.isPending ? "Opening…" : "Open wallet"}
        </button>
        {open.isError && <p className="text-meta text-semantic-down">{errText(open.error, "Could not open the wallet.")}</p>}
      </form>
    </div>
  );
}

function Summary({ wallet: w }: { wallet: PaperWallet }) {
  const tiles: { term: string; value: string; note: string; cls?: string }[] = [
    { term: "Equity", value: usd(w.equity_cents), note: `${usd(w.market_value_cents, 0)} invested` },
    { term: "Buying power", value: usd(w.buying_power_cents), note: w.reserved_cents > 0 ? `${usd(w.reserved_cents)} held for open orders` : "Cash not held for orders" },
    { term: "Today", value: pct(w.day_change_pct), note: w.session_open ? `${w.minutes_to_close} min to the close` : "Market closed", cls: tone(w.day_change_pct) },
    { term: "Total return", value: pct(w.total_return_pct), note: `on ${usd(w.net_deposits_cents, 0)} deposited`, cls: tone(w.total_return_pct) },
    { term: "Below peak", value: w.drawdown_pct > 0 ? `−${w.drawdown_pct.toFixed(2)}%` : "0%", note: `limit ${w.settings.max_drawdown_pct}% for agents` },
  ];
  return (
    <dl className="grid grid-cols-2 gap-x-6 gap-y-4 border-b border-border-subtle px-5 py-5 sm:grid-cols-3 md:px-8 lg:grid-cols-5">
      {tiles.map((t) => (
        <div key={t.term}>
          <dt className="text-meta text-text-muted">{t.term}</dt>
          <dd className={"mt-0.5 font-num text-[22px] font-semibold tabular-nums " + (t.cls ?? "text-text-primary")}>{t.value}</dd>
          <dd className="text-micro text-text-muted">{t.note}</dd>
        </div>
      ))}
    </dl>
  );
}

function Overview({ wallet: w, onSymbol, goTrade }: { wallet: PaperWallet; onSymbol: (s: string) => void; goTrade: () => void }) {
  const qc = useQueryClient();
  const orders = useQuery({ queryKey: ["paper-orders", w.id, "open"], queryFn: () => paperApi.orders(w.id, "open"), refetchInterval: 20_000 });
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["paper-wallets"] });
    qc.invalidateQueries({ queryKey: ["paper-orders", w.id] });
  };
  const sell = useMutation({
    mutationFn: (h: { symbol: string; qty: number }) =>
      paperApi.placeOrder(w.id, { symbol: h.symbol, side: "sell", type: "market", qty: h.qty, tif: "day", client_order_id: key() }),
    onSuccess: refresh,
  });
  const cancel = useMutation({ mutationFn: (id: number) => paperApi.cancelOrder(w.id, id), onSuccess: refresh });
  const open = orders.data?.orders ?? [];

  return (
    <div className="flex flex-col gap-5 px-5 py-6 md:px-8">
      <Card title="Holdings" sub={`${w.holdings.length} position${w.holdings.length === 1 ? "" : "s"} · unrealized ${usd(w.unrealized_cents)} · realized ${usd(w.realized_cents)} · fees paid ${usd(w.fees_cents)}`}>
        {w.holdings.length === 0 ? (
          <div className="flex flex-wrap items-center gap-3 px-5 pb-5 text-ui text-text-secondary">
            Nothing held yet.
            <button type="button" onClick={goTrade} className="action-primary">
              Place an order
            </button>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] text-ui">
              <thead>
                <tr className="border-y border-border-subtle text-left text-meta text-text-muted">
                  <th className="px-5 py-2 font-medium">Symbol</th>
                  <th className="px-3 py-2 text-right font-medium">Shares</th>
                  <th className="px-3 py-2 text-right font-medium">Avg cost</th>
                  <th className="px-3 py-2 text-right font-medium">Price</th>
                  <th className="px-3 py-2 text-right font-medium">Value</th>
                  <th className="px-3 py-2 text-right font-medium">Unrealized</th>
                  <th className="px-3 py-2 text-right font-medium">Weight</th>
                  <th className="px-5 py-2" />
                </tr>
              </thead>
              <tbody className="font-num tabular-nums">
                {w.holdings.map((h) => (
                  <tr key={h.symbol} className="border-b border-border-subtle last:border-0">
                    <td className="px-5 py-2.5">
                      <button type="button" onClick={() => onSymbol(h.symbol)} className="font-medium text-text-primary hover:text-brand">
                        {h.symbol}
                      </button>
                    </td>
                    <td className="px-3 py-2.5 text-right">{h.qty.toLocaleString()}</td>
                    <td className="px-3 py-2.5 text-right text-text-secondary">{usd(h.avg_cost_cents)}</td>
                    <td className="px-3 py-2.5 text-right">{usd(h.price_cents)}</td>
                    <td className="px-3 py-2.5 text-right">{usd(h.value_cents)}</td>
                    <td className={"px-3 py-2.5 text-right " + tone(h.unrealized_cents)}>
                      {usd(h.unrealized_cents)} <span className="text-meta">({pct(h.unrealized_pct, 1)})</span>
                    </td>
                    <td className="px-3 py-2.5 text-right text-text-secondary">{h.weight_pct.toFixed(1)}%</td>
                    <td className="px-5 py-2.5 text-right">
                      <button
                        type="button"
                        disabled={sell.isPending || !w.session_open}
                        title={w.session_open ? "Sell the whole position at market" : "The market is closed"}
                        onClick={() => sell.mutate({ symbol: h.symbol, qty: h.qty - h.reserved_qty })}
                        className="action-secondary h-7 px-2.5 text-meta disabled:opacity-40"
                      >
                        Sell all
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {sell.isError && <p className="px-5 pb-3 text-meta text-semantic-down">{errText(sell.error, "Could not sell.")}</p>}
      </Card>

      <Card title="Open orders" sub="Buys hold cash until they fill; stops and targets attached to a buy cancel each other.">
        {open.length === 0 ? (
          <p className="px-5 pb-5 text-ui text-text-muted">No open orders.</p>
        ) : (
          <OrderTable orders={open} onCancel={(id) => cancel.mutate(id)} />
        )}
      </Card>
    </div>
  );
}

const STATUS_TONE: Record<PaperOrder["status"], "up" | "down" | "muted" | "brand" | "neutral"> = {
  accepted: "brand",
  filled: "up",
  canceled: "muted",
  rejected: "down",
  expired: "muted",
};

function OrderTable({ orders, onCancel }: { orders: PaperOrder[]; onCancel?: (id: number) => void }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[760px] text-ui">
        <thead>
          <tr className="border-y border-border-subtle text-left text-meta text-text-muted">
            <th className="px-5 py-2 font-medium">Placed</th>
            <th className="px-3 py-2 font-medium">Order</th>
            <th className="px-3 py-2 font-medium">Status</th>
            <th className="px-3 py-2 text-right font-medium">Fill</th>
            <th className="px-3 py-2 font-medium">By</th>
            <th className="px-5 py-2" />
          </tr>
        </thead>
        <tbody>
          {orders.map((o) => (
            <tr key={o.id} className="border-b border-border-subtle align-top last:border-0">
              <td className="px-5 py-2.5 text-meta text-text-muted">{formatDateTime(o.created_at)}</td>
              <td className="px-3 py-2.5">
                <span className={o.side === "buy" ? "text-semantic-up" : "text-semantic-down"}>{o.side}</span>{" "}
                <span className="font-num">{o.qty.toLocaleString()}</span> <span className="font-medium text-text-primary">{o.symbol}</span>{" "}
                <span className="text-meta text-text-muted">
                  {o.type.replace("_", "-")}
                  {o.limit_cents ? ` ${usd(o.limit_cents)}` : ""}
                  {o.stop_cents ? ` stop ${usd(o.stop_cents)}` : ""} · {o.tif}
                </span>
                {(o.reason || o.reject_reason) && <p className="mt-0.5 max-w-[48ch] text-meta text-text-muted">{o.reject_reason || o.reason}</p>}
              </td>
              <td className="px-3 py-2.5">
                <Pill tone={STATUS_TONE[o.status]}>{o.status === "accepted" ? "open" : o.status}</Pill>
              </td>
              <td className="px-3 py-2.5 text-right font-num tabular-nums">{o.avg_fill_cents ? usd(o.avg_fill_cents) : "—"}</td>
              <td className="px-3 py-2.5 text-meta text-text-secondary">{o.source === "protective" ? "exit" : o.source}</td>
              <td className="px-5 py-2.5 text-right">
                {onCancel && o.status === "accepted" && (
                  <button type="button" onClick={() => onCancel(o.id)} className="text-meta text-text-muted hover:text-semantic-down">
                    Cancel
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The order ticket, with the cost and fees worked out before it is sent. */
function Ticket({ wallet: w }: { wallet: PaperWallet }) {
  const qc = useQueryClient();
  const [symbol, setSymbol] = useUrlState("symbol", "");
  const [draft, setDraft] = useState(symbol);
  const [side, setSide] = useState<"buy" | "sell">("buy");
  const [type, setType] = useState<OrderType>("market");
  const [qty, setQty] = useState("10");
  const [limit, setLimit] = useState("");
  const [stop, setStop] = useState("");
  const [tif, setTif] = useState<"day" | "gtc">("day");
  const [exits, setExits] = useState(true);
  const [sl, setSl] = useState(String(w.settings.stop_loss_pct || 4));
  const [tp, setTp] = useState(String(w.settings.take_profit_pct || 8));
  const [note, setNote] = useState<string | null>(null);
  const quote = useQuery({ queryKey: ["quote", symbol], queryFn: () => api.quote(symbol), enabled: !!symbol, refetchInterval: 20_000 });
  const price = quote.data?.quote.price;
  const held = w.holdings.find((h) => h.symbol === symbol);
  const n = Math.max(0, Math.floor(Number(qty) || 0));
  const ref = type === "limit" || type === "stop_limit" ? Number(limit) || price : type === "stop" ? Number(stop) || price : price;
  const gross = ref ? ref * n : 0;
  const s = w.settings;
  const fees = side === "sell" && ref ? Math.ceil(((gross * s.sec_fee_per_million) / 1e6) * 100) / 100 + Math.min(Math.ceil(n * s.taf_per_share * 100), s.taf_max_cents) / 100 : 0;
  const slip = type === "market" || type === "stop" ? (gross * s.slippage_bps) / 1e4 : 0;

  useEffect(() => setDraft(symbol), [symbol]);

  const place = useMutation({
    mutationFn: () => {
      const body: PaperOrderRequest = { client_order_id: key(), symbol, side, type, qty: n, tif };
      if (type === "limit" || type === "stop_limit") body.limit_price = Number(limit);
      if (type === "stop" || type === "stop_limit") body.stop_price = Number(stop);
      if (side === "buy" && exits) {
        body.stop_loss_pct = Number(sl) || undefined;
        body.take_profit_pct = Number(tp) || undefined;
      }
      return paperApi.placeOrder(w.id, body);
    },
    onSuccess: (r) => {
      setNote(
        r.fill
          ? `Filled ${r.fill.qty} ${r.fill.symbol} at ${usd(r.fill.price_cents)}${r.fill.fee_cents ? ` plus ${usd(r.fill.fee_cents)} in fees` : ""}.`
          : w.session_open
            ? "Order placed. It fills when the market reaches your price."
            : "Order placed. The market is closed, so it fills at the next open.",
      );
      qc.invalidateQueries({ queryKey: ["paper-wallets"] });
      qc.invalidateQueries({ queryKey: ["paper-orders", w.id] });
    },
    onError: (e) => setNote(errText(e, "The order was not placed.")),
  });

  return (
    <div className="grid gap-5 px-5 py-6 md:px-8 lg:grid-cols-[minmax(0,420px)_minmax(0,1fr)]">
      <Card title="Order ticket" sub={w.session_open ? `Market open · ${w.minutes_to_close} min to the close` : "Market closed · orders queue for the next open"}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            setNote(null);
            place.mutate();
          }}
          className="flex flex-col gap-3 px-5 pb-5"
        >
          <div>
            <label className={labelCls} htmlFor="ticket-symbol">Symbol</label>
            <input
              id="ticket-symbol"
              value={draft}
              onChange={(e) => setDraft(e.target.value.toUpperCase())}
              onBlur={() => setSymbol(draft.trim())}
              onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), setSymbol(draft.trim()))}
              placeholder="AAPL"
              className={inputCls}
            />
            <p className="mt-1 h-4 text-meta text-text-muted">
              {symbol && (quote.isLoading ? "Pricing…" : price ? `Last $${price.toFixed(2)}${held ? ` · you hold ${held.qty}` : ""}` : "No price for that symbol.")}
            </p>
          </div>
          <Segmented label="Side" value={side} onChange={setSide} options={[{ value: "buy", label: "Buy" }, { value: "sell", label: "Sell" }]} />
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className={labelCls} htmlFor="ticket-qty">Shares</label>
              <input id="ticket-qty" value={qty} onChange={(e) => setQty(e.target.value)} inputMode="numeric" className={inputCls} />
            </div>
            <Select
              label="Type"
              value={type}
              onChange={(v) => setType(v as OrderType)}
              options={[
                { value: "market", label: "Market" },
                { value: "limit", label: "Limit" },
                { value: "stop", label: "Stop" },
                { value: "stop_limit", label: "Stop-limit" },
              ]}
            />
          </div>
          {(type === "stop" || type === "stop_limit") && (
            <div>
              <label className={labelCls} htmlFor="ticket-stop">Stop price</label>
              <input id="ticket-stop" value={stop} onChange={(e) => setStop(e.target.value)} inputMode="decimal" className={inputCls} />
            </div>
          )}
          {(type === "limit" || type === "stop_limit") && (
            <div>
              <label className={labelCls} htmlFor="ticket-limit">Limit price</label>
              <input id="ticket-limit" value={limit} onChange={(e) => setLimit(e.target.value)} inputMode="decimal" className={inputCls} />
            </div>
          )}
          <Segmented label="Lasts" value={tif} onChange={setTif} options={[{ value: "day", label: "Today" }, { value: "gtc", label: "Until canceled" }]} />
          {side === "buy" && (
            <div className="flex flex-col gap-2 rounded-md border border-border-subtle p-3">
              <Switch checked={exits} onChange={setExits} label="Attach a stop loss and a target" />
              {exits && (
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <label className={labelCls} htmlFor="ticket-sl">Stop loss %</label>
                    <input id="ticket-sl" value={sl} onChange={(e) => setSl(e.target.value)} inputMode="decimal" className={inputCls} />
                  </div>
                  <div>
                    <label className={labelCls} htmlFor="ticket-tp">Take profit %</label>
                    <input id="ticket-tp" value={tp} onChange={(e) => setTp(e.target.value)} inputMode="decimal" className={inputCls} />
                  </div>
                </div>
              )}
            </div>
          )}
          <dl className="grid grid-cols-2 gap-y-1 rounded-md bg-bg-base px-3 py-2.5 text-meta">
            <dt className="text-text-muted">Estimated {side === "buy" ? "cost" : "proceeds"}</dt>
            <dd className="text-right font-num">{gross ? `$${gross.toFixed(2)}` : "—"}</dd>
            <dt className="text-text-muted">Slippage (est.)</dt>
            <dd className="text-right font-num">{slip ? `$${slip.toFixed(2)}` : "—"}</dd>
            <dt className="text-text-muted">SEC + FINRA fees</dt>
            <dd className="text-right font-num">{side === "sell" ? `$${fees.toFixed(2)}` : "$0.00"}</dd>
            <dt className="text-text-muted">Buying power</dt>
            <dd className="text-right font-num">{usd(w.buying_power_cents)}</dd>
          </dl>
          <button type="submit" disabled={place.isPending || !symbol || n <= 0} className={side === "buy" ? "action-primary" : "action-secondary"}>
            {place.isPending ? "Placing…" : `${side === "buy" ? "Buy" : "Sell"} ${n || ""} ${symbol}`}
          </button>
          {note && <p className="text-meta text-text-secondary">{note}</p>}
        </form>
      </Card>
      <Card title="How fills work" sub="The same rules a real account follows.">
        <ul className="flex list-disc flex-col gap-2 px-9 pb-5 text-ui leading-relaxed text-text-secondary">
          <li>Market orders fill at the latest traded price plus {s.slippage_bps} basis points of slippage, only between 9:30 and 16:00 New York time. Placed while the market is closed, they fill at the next open.</li>
          <li>Limit orders fill only when the market trades at your price or better; stops trigger when touched and then fill like a market order, at the open if the price gaps through them.</li>
          <li>Sales pay the SEC Section 31 fee (${s.sec_fee_per_million} per million) and FINRA's trading activity fee, as a broker would pass them on.</li>
          <li>An account under $25,000 may make only three day trades in five business days (FINRA's pattern-day-trader rule). No short selling or margin.</li>
          <li>Day orders expire at the close. Good-till-canceled orders last ninety days.</li>
        </ul>
      </Card>
    </div>
  );
}

function Funding({ wallet: w, direction, onClose }: { wallet: PaperWallet; direction: "deposit" | "withdrawal"; onClose: () => void }) {
  const qc = useQueryClient();
  const [amount, setAmount] = useState(direction === "deposit" ? "10000" : "");
  // One key per dialog: a double click or a retried request moves money once.
  const [idem] = useState(key);
  const pay = useMutation({
    mutationFn: () => (direction === "deposit" ? paperApi.deposit : paperApi.withdraw)(w.id, Number(amount), idem),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["paper-wallets"] });
      qc.invalidateQueries({ queryKey: ["paper-payments", w.id] });
      onClose();
    },
  });
  return (
    <Drawer open onClose={onClose} label={direction === "deposit" ? "Add funds" : "Withdraw"} width={420}>
      <div className="flex h-12 shrink-0 items-center justify-between border-b border-border-subtle px-5">
        <span className="text-ui font-medium text-text-primary">{direction === "deposit" ? "Add funds" : "Withdraw"}</span>
        <button type="button" onClick={onClose} aria-label="Close" className="text-text-muted hover:text-text-primary">
          <X size={17} />
        </button>
      </div>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          pay.mutate();
        }}
        className="flex flex-col gap-4 px-5 py-5"
      >
        <div>
          <label className={labelCls} htmlFor="fund-amount">Amount (USD)</label>
          <input id="fund-amount" autoFocus value={amount} onChange={(e) => setAmount(e.target.value)} inputMode="decimal" className={inputCls} />
          {direction === "withdrawal" && <p className="mt-1 text-meta text-text-muted">Up to {usd(w.buying_power_cents)}: the rest is invested or held for open orders.</p>}
        </div>
        <p className="rounded-md bg-bg-base px-3 py-2.5 text-meta leading-relaxed text-text-secondary">
          Payment method: <span className="font-medium text-text-primary">simulated</span>. The money is fictional and settles at once. The
          payment still goes through a real lifecycle — created, then settled once, with a key that stops a retry paying twice — so a
          card processor can replace the simulator later without changing the books.
        </p>
        <button type="submit" disabled={pay.isPending || !(Number(amount) > 0)} className="action-primary">
          {pay.isPending ? "Processing…" : direction === "deposit" ? `Add $${Number(amount || 0).toLocaleString()}` : `Withdraw $${Number(amount || 0).toLocaleString()}`}
        </button>
        {pay.isError && <p className="text-meta text-semantic-down">{errText(pay.error, "The payment did not go through.")}</p>}
      </form>
    </Drawer>
  );
}

function SettingsDrawer({ wallet: w, onClose }: { wallet: PaperWallet; onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(w.name);
  const [s, setS] = useState<PaperSettings>(w.settings);
  const save = useMutation({
    mutationFn: () => paperApi.updateWallet(w.id, { name, settings: s }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["paper-wallets"] });
      onClose();
    },
  });
  const close = useMutation({
    mutationFn: () => paperApi.closeWallet(w.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["paper-wallets"] });
      onClose();
    },
  });
  const num = (k: keyof PaperSettings, label: string, hint?: string) => (
    <div>
      <label className={labelCls} htmlFor={`set-${k}`}>{label}</label>
      <input
        id={`set-${k}`}
        value={String(s[k])}
        onChange={(e) => setS({ ...s, [k]: Number(e.target.value) })}
        inputMode="decimal"
        className={inputCls}
      />
      {hint && <p className="mt-1 text-micro text-text-muted">{hint}</p>}
    </div>
  );
  return (
    <Drawer open onClose={onClose} label="Wallet settings" width={480}>
      <div className="flex h-12 shrink-0 items-center justify-between border-b border-border-subtle px-5">
        <span className="text-ui font-medium text-text-primary">Wallet settings</span>
        <button type="button" onClick={onClose} aria-label="Close" className="text-text-muted hover:text-text-primary">
          <X size={17} />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-4 px-5 py-5">
          <div>
            <label className={labelCls} htmlFor="set-name">Name</label>
            <input id="set-name" value={name} onChange={(e) => setName(e.target.value)} className={inputCls} />
          </div>
          <h3 className="text-meta font-semibold uppercase tracking-wide text-text-muted">Risk limits for agents</h3>
          <div className="grid grid-cols-2 gap-3">
            {num("max_position_pct", "Max position, % of equity")}
            {num("max_daily_loss_pct", "Daily loss limit %", "No new positions after this.")}
            {num("max_drawdown_pct", "Drawdown limit %", "Agents halt past this.")}
            {num("max_trades_per_day", "Trades a day")}
            {num("stop_loss_pct", "Default stop loss %")}
            {num("take_profit_pct", "Default target %")}
            {num("min_price", "Minimum share price $")}
            {num("weekly_goal_pct", "Weekly goal %", "Tracked, never chased.")}
          </div>
          <h3 className="text-meta font-semibold uppercase tracking-wide text-text-muted">Execution realism</h3>
          <div className="grid grid-cols-2 gap-3">
            {num("slippage_bps", "Slippage, basis points")}
            {num("commission_cents", "Commission per order, cents")}
          </div>
          <Switch
            checked={s.pattern_day_trader}
            onChange={(v) => setS({ ...s, pattern_day_trader: v })}
            label="Apply the pattern-day-trader rule under $25,000"
          />
          <button type="button" onClick={() => save.mutate()} disabled={save.isPending} className="action-primary w-fit">
            {save.isPending ? "Saving…" : "Save settings"}
          </button>
          {save.isError && <p className="text-meta text-semantic-down">{errText(save.error, "Could not save.")}</p>}
          <div className="mt-6 border-t border-border-subtle pt-4">
            <button type="button" onClick={() => confirm(`Close ${w.name}? Open orders are canceled and agents stop. Its history stays.`) && close.mutate()} className="text-meta text-semantic-down hover:underline">
              Close this wallet
            </button>
          </div>
        </div>
      </div>
    </Drawer>
  );
}

// ---- autopilot -----------------------------------------------------------------

const BLANK_AGENT: PaperAgent = {
  kind: "ai",
  name: "",
  enabled: false,
  config: { use_scanner: true, every_minutes: 30, max_positions: 5, intraday: false, symbols: [] },
};

function Autopilot({ wallet: w }: { wallet: PaperWallet }) {
  const qc = useQueryClient();
  const agents = useQuery({ queryKey: ["paper-agents", w.id], queryFn: () => paperApi.agents(w.id) });
  const decisions = useQuery({ queryKey: ["paper-decisions", w.id], queryFn: () => paperApi.decisions(w.id), refetchInterval: 30_000 });
  const [editing, setEditing] = useState<PaperAgent | null>(null);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["paper-agents", w.id] });
    qc.invalidateQueries({ queryKey: ["paper-decisions", w.id] });
    qc.invalidateQueries({ queryKey: ["paper-wallets"] });
  };
  const toggle = useMutation({ mutationFn: (a: PaperAgent) => paperApi.saveAgent(w.id, { ...a, enabled: !a.enabled }), onSuccess: refresh });
  const run = useMutation({ mutationFn: (a: PaperAgent) => paperApi.runAgent(w.id, a.id!), onSuccess: refresh });
  const drop = useMutation({ mutationFn: (a: PaperAgent) => paperApi.deleteAgent(w.id, a.id!), onSuccess: refresh });
  const list = agents.data?.agents ?? [];
  const names = Object.fromEntries(list.map((a) => [a.id, a.name]));

  return (
    <div className="flex flex-col gap-5 px-5 py-6 md:px-8">
      <Card
        title="Agents"
        sub="An agent trades this wallet on its own, on a schedule during market hours, inside the wallet's risk limits. Every run is logged with what it saw and why it acted."
        actions={
          <button type="button" onClick={() => setEditing(BLANK_AGENT)} className="action-primary">
            <Plus size={15} /> New agent
          </button>
        }
      >
        {list.length === 0 ? (
          <p className="px-5 pb-5 text-ui text-text-muted">No agents yet.</p>
        ) : (
          <ul className="divide-y divide-border-subtle border-t border-border-subtle">
            {list.map((a) => (
              <li key={a.id} className="flex flex-wrap items-center gap-3 px-5 py-3">
                <Bot size={16} className="text-text-muted" />
                <div className="min-w-0 flex-1">
                  <p className="text-ui font-medium text-text-primary">
                    {a.name} <span className="text-meta font-normal text-text-muted">· {KIND_LABEL[a.kind]} · every {a.config.every_minutes} min{a.config.intraday ? " · intraday" : ""}</span>
                  </p>
                  <p className="text-meta text-text-muted">
                    {a.halted_reason ? <span className="text-semantic-down">{a.halted_reason}</span> : a.last_run_at ? `Last ran ${formatDateTime(a.last_run_at)}` : "Has not run yet"}
                  </p>
                </div>
                <Pill tone={a.enabled ? "up" : "muted"}>{a.enabled ? "on" : "off"}</Pill>
                <button type="button" onClick={() => toggle.mutate(a)} className="action-secondary h-8 px-2.5 text-meta">
                  {a.enabled ? <Pause size={13} /> : <Play size={13} />} {a.enabled ? "Pause" : "Start"}
                </button>
                <button type="button" disabled={run.isPending} onClick={() => run.mutate(a)} className="action-secondary h-8 px-2.5 text-meta">
                  Run now
                </button>
                <button type="button" onClick={() => setEditing(a)} className="text-meta text-brand hover:underline">
                  Edit
                </button>
                <button type="button" onClick={() => confirm(`Delete ${a.name} and its decision log?`) && drop.mutate(a)} aria-label={`Delete ${a.name}`} className="text-text-muted hover:text-semantic-down">
                  <Trash2 size={14} />
                </button>
              </li>
            ))}
          </ul>
        )}
        {run.isError && <p className="px-5 pb-3 text-meta text-semantic-down">{errText(run.error, "The run failed.")}</p>}
      </Card>

      <Card title="Decision log" sub="Newest first. Refusals are the risk limits doing their job.">
        {(decisions.data?.decisions ?? []).length === 0 ? (
          <p className="px-5 pb-5 text-ui text-text-muted">No decisions yet.</p>
        ) : (
          <ul className="divide-y divide-border-subtle border-t border-border-subtle">
            {(decisions.data?.decisions ?? []).map((d) => (
              <li key={d.id} className="px-5 py-3">
                <p className="text-meta text-text-muted">
                  {formatDateTime(d.at)} · {names[d.agent_id] ?? `agent ${d.agent_id}`}
                  {d.model ? ` · ${d.model}` : ""}
                </p>
                {d.summary && <p className="mt-1 text-ui leading-relaxed text-text-primary">{d.summary}</p>}
                {d.error && <p className="mt-1 text-meta text-semantic-down">{d.error}</p>}
                {(d.intents ?? []).length > 0 && (
                  <ul className="mt-1.5 flex flex-col gap-1">
                    {(d.intents ?? []).map((i, k) => (
                      <li key={k} className="text-meta text-text-secondary">
                        <span className={i.side === "buy" ? "text-semantic-up" : "text-semantic-down"}>{i.side}</span> <span className="font-medium text-text-primary">{i.symbol}</span>
                        {i.size_pct ? ` · ${i.size_pct}% of equity` : ""} — {i.reason}
                      </li>
                    ))}
                  </ul>
                )}
                {(d.rejected ?? []).length > 0 && (
                  <ul className="mt-1 flex flex-col gap-0.5">
                    {(d.rejected ?? []).map((r, k) => (
                      <li key={k} className="text-meta text-text-muted">
                        refused {r.side} {r.symbol}: {r.reason}
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        )}
      </Card>

      {editing && <AgentEditor wallet={w} agent={editing} aiAvailable={agents.data?.ai_available ?? false} onClose={() => setEditing(null)} onSaved={refresh} />}
    </div>
  );
}

const KIND_LABEL: Record<PaperAgent["kind"], string> = { ai: "AI", algorithm: "Algorithm", webhook: "Your own code" };

function AgentEditor({ wallet: w, agent, aiAvailable, onClose, onSaved }: { wallet: PaperWallet; agent: PaperAgent; aiAvailable: boolean; onClose: () => void; onSaved: () => void }) {
  const [a, setA] = useState<PaperAgent>(agent);
  const [symbols, setSymbols] = useState((agent.config.symbols ?? []).join(", "));
  const lists = useQuery({ queryKey: ["watchlists"], queryFn: api.watchlists });
  const algos = useQuery({ queryKey: ["algorithms"], queryFn: api.algorithms });
  const cfg = a.config;
  const set = (c: Partial<typeof cfg>) => setA({ ...a, config: { ...cfg, ...c } });
  const save = useMutation({
    mutationFn: () =>
      paperApi.saveAgent(w.id, {
        ...a,
        config: { ...cfg, symbols: symbols.split(/[\s,]+/).map((x) => x.trim().toUpperCase()).filter(Boolean) },
      }),
    onSuccess: () => {
      onSaved();
      onClose();
    },
  });
  return (
    <Drawer open onClose={onClose} label="Agent" width={560}>
      <div className="flex h-12 shrink-0 items-center justify-between border-b border-border-subtle px-5">
        <span className="text-ui font-medium text-text-primary">{a.id ? "Edit agent" : "New agent"}</span>
        <button type="button" onClick={onClose} aria-label="Close" className="text-text-muted hover:text-text-primary">
          <X size={17} />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-4 px-5 py-5">
          <div>
            <label className={labelCls} htmlFor="agent-name">Name</label>
            <input id="agent-name" value={a.name} onChange={(e) => setA({ ...a, name: e.target.value })} className={inputCls} placeholder="Momentum bot" />
          </div>
          {!a.id && (
            <Segmented
              label="Who decides"
              value={a.kind}
              onChange={(k) => setA({ ...a, kind: k })}
              options={[
                { value: "ai", label: "AI" },
                { value: "algorithm", label: "An algorithm" },
                { value: "webhook", label: "Your own code" },
              ]}
            />
          )}
          {a.kind === "ai" && !aiAvailable && <p className="text-meta text-semantic-down">No text model is configured, so an AI agent cannot run. Set LLM_BASE_URL and LLM_API_KEY.</p>}

          <h3 className="text-meta font-semibold uppercase tracking-wide text-text-muted">Where it looks</h3>
          <div>
            <label className={labelCls} htmlFor="agent-symbols">Symbols</label>
            <input id="agent-symbols" value={symbols} onChange={(e) => setSymbols(e.target.value.toUpperCase())} placeholder="AAPL, MSFT, NVDA" className={inputCls} />
          </div>
          {(lists.data?.watchlists ?? []).length > 0 && (
            <div className="flex flex-wrap gap-2">
              {(lists.data?.watchlists ?? []).map((l) => {
                const on = (cfg.watchlist_ids ?? []).includes(l.id);
                return (
                  <button
                    key={l.id}
                    type="button"
                    onClick={() => set({ watchlist_ids: on ? (cfg.watchlist_ids ?? []).filter((x) => x !== l.id) : [...(cfg.watchlist_ids ?? []), l.id] })}
                    className={"rounded-md border px-2.5 py-1 text-meta " + (on ? "border-brand bg-brand-muted text-text-primary" : "border-border-subtle text-text-secondary")}
                  >
                    {l.name} · {l.count}
                  </button>
                );
              })}
            </div>
          )}
          <Switch checked={cfg.use_scanner} onChange={(v) => set({ use_scanner: v })} label="Also consider the scanner's latest unusual movers" />

          <h3 className="text-meta font-semibold uppercase tracking-wide text-text-muted">How it trades</h3>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className={labelCls} htmlFor="agent-every">Decide every (minutes)</label>
              <input id="agent-every" value={cfg.every_minutes} onChange={(e) => set({ every_minutes: Number(e.target.value) })} inputMode="numeric" className={inputCls} />
            </div>
            <div>
              <label className={labelCls} htmlFor="agent-max">Max positions</label>
              <input id="agent-max" value={cfg.max_positions} onChange={(e) => set({ max_positions: Number(e.target.value) })} inputMode="numeric" className={inputCls} />
            </div>
          </div>
          <Switch checked={cfg.intraday} onChange={(v) => set({ intraday: v })} label="Intraday: be flat before every close" />

          {a.kind === "ai" && (
            <div>
              <label className={labelCls} htmlFor="agent-instr">Instructions</label>
              <textarea
                id="agent-instr"
                value={cfg.instructions ?? ""}
                onChange={(e) => set({ instructions: e.target.value })}
                rows={4}
                maxLength={2000}
                placeholder="e.g. Favour large caps with news behind the move. Avoid holding through earnings."
                className="w-full rounded-md border border-border-subtle bg-bg-field px-3 py-2 text-ui outline-none focus:border-brand"
              />
            </div>
          )}
          {a.kind === "algorithm" && (
            <div className="grid grid-cols-2 gap-3">
              <Select
                label="Enter when"
                value={String(cfg.algorithm_id ?? "")}
                onChange={(v) => set({ algorithm_id: Number(v) })}
                options={[{ value: "", label: "Choose a saved rule" }, ...(algos.data?.algorithms ?? []).map((x) => ({ value: String(x.id), label: x.name }))]}
              />
              <Select
                label="Exit when"
                value={String(cfg.exit_algorithm_id ?? "")}
                onChange={(v) => set({ exit_algorithm_id: Number(v) || undefined })}
                options={[{ value: "", label: "Stop or target only" }, ...(algos.data?.algorithms ?? []).map((x) => ({ value: String(x.id), label: x.name }))]}
              />
              <div>
                <label className={labelCls} htmlFor="agent-size">Size, % of equity</label>
                <input id="agent-size" value={cfg.size_pct ?? ""} onChange={(e) => set({ size_pct: Number(e.target.value) })} inputMode="decimal" className={inputCls} placeholder="10" />
              </div>
            </div>
          )}
          {a.kind === "webhook" && (
            <div className="flex flex-col gap-3">
              <div>
                <label className={labelCls} htmlFor="agent-url">Webhook URL</label>
                <input id="agent-url" value={cfg.webhook_url ?? ""} onChange={(e) => set({ webhook_url: e.target.value })} placeholder="https://my-strategy.example.com/decide" className={inputCls} />
              </div>
              <div>
                <label className={labelCls} htmlFor="agent-secret">Signing secret</label>
                <input id="agent-secret" value={cfg.webhook_secret ?? ""} onChange={(e) => set({ webhook_secret: e.target.value })} className={inputCls} />
              </div>
              <p className="rounded-md bg-bg-base px-3 py-2.5 text-meta leading-relaxed text-text-secondary">
                Each run POSTs the account, holdings and candidates as JSON, signed with HMAC-SHA256 in <code>X-Bellwether-Signature</code>. Reply with{" "}
                <code>{`{"summary": "...", "intents": [{"symbol": "AAPL", "side": "buy", "size_pct": 10, "reason": "..."}]}`}</code>. The wallet's risk limits apply to
                every intent. Plain-HTTP and private addresses must be listed in PAPER_WEBHOOK_ALLOW.
              </p>
            </div>
          )}
          <Switch checked={a.enabled} onChange={(v) => setA({ ...a, enabled: v })} label="Enabled" />
          <button type="button" onClick={() => save.mutate()} disabled={save.isPending || !a.name.trim()} className="action-primary w-fit">
            {save.isPending ? "Saving…" : "Save agent"}
          </button>
          {save.isError && <p className="text-meta text-semantic-down">{errText(save.error, "Could not save the agent.")}</p>}
        </div>
      </div>
    </Drawer>
  );
}

// ---- performance ---------------------------------------------------------------

function PerformanceTab({ wallet: w }: { wallet: PaperWallet }) {
  const { data, isLoading } = useQuery({ queryKey: ["paper-perf", w.id], queryFn: () => paperApi.performance(w.id), refetchInterval: 120_000 });
  if (isLoading || !data) return <SkeletonRows count={6} height={60} />;
  const p = data.performance;
  const stats: { term: string; value: string; note: string; cls?: string }[] = [
    { term: "Return", value: pct(p.return_pct), note: p.since ? `since ${formatDateTime(p.since)}` : "not marked yet", cls: tone(p.return_pct) },
    { term: "S&P 500", value: pct(p.benchmark_pct), note: p.excess_pct !== undefined ? `${pct(p.excess_pct)} against it` : "same period" },
    { term: "Sharpe", value: p.sharpe?.toFixed(2) ?? "—", note: p.volatility_pct !== undefined ? `${p.volatility_pct.toFixed(1)}% annual volatility` : "needs five sessions" },
    { term: "Worst drawdown", value: p.max_drawdown_pct ? `−${p.max_drawdown_pct.toFixed(2)}%` : "0%", note: "peak to trough" },
    { term: "Closed trades", value: String(p.trades), note: p.trades ? `${p.win_rate.toFixed(0)}% won · avg ${pct(p.avg_win_pct, 1)} / ${pct(p.avg_loss_pct, 1)}` : "none yet" },
    { term: "Profit factor", value: p.profit_factor?.toFixed(2) ?? "—", note: `fees paid ${usd(p.fees_cents)}` },
  ];
  return (
    <div className="flex flex-col gap-5 px-5 py-6 md:px-8">
      <Card title="Skill or luck?" sub="The question paper trading exists to answer.">
        <div className="flex flex-col gap-3 px-5 pb-5">
          <p className="text-[17px] leading-relaxed text-text-primary">{p.verdict}</p>
          {p.luck && (
            <div className="flex flex-col gap-2">
              <LuckBar beat={p.luck.beat_pct} />
              <p className="text-meta text-text-muted">
                {p.luck.simulations.toLocaleString()} random traders each made the same {p.luck.trades} trades — same holding periods, random stocks from
                the universe, random days in the same period. Median random result {pct(p.luck.median_pct)}; the best tenth did {pct(p.luck.p90_pct)} or
                better; this account {pct(p.luck.actual_pct)}.
              </p>
            </div>
          )}
        </div>
      </Card>

      <dl className="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-3 lg:grid-cols-6">
        {stats.map((t) => (
          <div key={t.term}>
            <dt className="text-meta text-text-muted">{t.term}</dt>
            <dd className={"mt-0.5 font-num text-[20px] font-semibold tabular-nums " + (t.cls ?? "text-text-primary")}>{t.value}</dd>
            <dd className="text-micro text-text-muted">{t.note}</dd>
          </div>
        ))}
      </dl>

      <Card title="Account against the S&P 500" sub="Both as the change since the first mark. Marked every 15 minutes in the session and at each close.">
        <div className="px-5 pb-5">
          <EquityChart points={data.equity} />
        </div>
      </Card>

      <Card
        title={`Weekly returns against the ${p.weekly_goal_pct}% goal`}
        sub={`${p.goal_hit_weeks} of ${p.weeks.length} week${p.weeks.length === 1 ? "" : "s"} reached it. No strategy reliably makes ${p.weekly_goal_pct}% a week — compounded that is over ${Math.round((Math.pow(1 + p.weekly_goal_pct / 100, 52) - 1) * 100).toLocaleString()}% a year — so the goal is measured against, never chased.`}
      >
        <div className="px-5 pb-5">
          <WeekBars weeks={p.weeks} goal={p.weekly_goal_pct} />
        </div>
      </Card>
    </div>
  );
}

function LuckBar({ beat }: { beat: number }) {
  return (
    <div className="max-w-xl">
      <div className="relative h-3 rounded-full bg-bg-chip">
        <span className="absolute inset-y-0 left-0 rounded-full bg-brand" style={{ width: `${Math.max(2, beat)}%` }} />
        <span className="absolute inset-y-[-3px] w-px bg-text-muted" style={{ left: "95%" }} title="95%: the bar for evidence of skill" />
      </div>
      <p className="mt-1 text-meta text-text-secondary">
        Beat <span className="font-semibold text-text-primary">{beat.toFixed(0)}%</span> of random traders · 95% is the bar for evidence of skill
      </p>
    </div>
  );
}

/** Account and S&P 500 as % change since the first mark, one axis. */
function EquityChart({ points }: { points: EquityPoint[] }) {
  const [hover, setHover] = useState<number | null>(null);
  const series = useMemo(() => {
    const first = points[0];
    if (!first) return [];
    return points.map((p) => ({
      at: p.at,
      acct: (p.equity_cents / first.equity_cents - 1) * 100,
      bench: first.benchmark && p.benchmark ? (p.benchmark / first.benchmark - 1) * 100 : undefined,
    }));
  }, [points]);
  if (series.length < 2) return <p className="py-8 text-center text-ui text-text-muted">The chart starts after the account's second mark.</p>;
  const W = 760, H = 240, pad = { t: 12, r: 64, b: 24, l: 48 };
  const vals = series.flatMap((s) => [s.acct, s.bench ?? s.acct]);
  const lo = Math.min(0, ...vals), hi = Math.max(0, ...vals);
  const span = Math.max(hi - lo, 0.5);
  const y = (v: number) => pad.t + ((hi + span * 0.08 - v) / (span * 1.16)) * (H - pad.t - pad.b);
  const x = (i: number) => pad.l + (i / (series.length - 1)) * (W - pad.l - pad.r);
  const path = (get: (s: (typeof series)[number]) => number | undefined) =>
    series.map((s, i) => (get(s) === undefined ? "" : `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(get(s)!).toFixed(1)}`)).join(" ");
  const ticks = [lo, (lo + hi) / 2, hi].map((v) => Math.round(v * 10) / 10);
  const last = series[series.length - 1]!;
  const h = hover !== null ? series[hover] : undefined;
  return (
    <div>
      <div className="mb-2 flex flex-wrap gap-4 text-meta text-text-secondary">
        <span className="flex items-center gap-1.5"><span className="h-0.5 w-4 rounded bg-brand" /> This account</span>
        <span className="flex items-center gap-1.5"><span className="h-0.5 w-4 rounded" style={{ background: "var(--bench)" }} /> S&amp;P 500</span>
      </div>
      <p className="h-5 text-meta text-text-secondary" aria-live="polite">
        {h ? `${formatDateTime(h.at)} · account ${pct(h.acct)} · S&P 500 ${pct(h.bench)}` : ""}
      </p>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="w-full"
        role="img"
        aria-label="Account and S&P 500 change since the first mark"
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          const px = ((e.clientX - r.left) / r.width) * W;
          setHover(Math.max(0, Math.min(series.length - 1, Math.round(((px - pad.l) / (W - pad.l - pad.r)) * (series.length - 1)))));
        }}
      >
        {ticks.map((v) => (
          <g key={v}>
            <line x1={pad.l} x2={W - pad.r} y1={y(v)} y2={y(v)} className={v === 0 ? "stroke-text-muted" : "stroke-border-subtle"} strokeWidth={1} />
            <text x={pad.l - 8} y={y(v)} dy="0.32em" textAnchor="end" className="fill-text-muted text-[11px]">
              {pct(v, 1)}
            </text>
          </g>
        ))}
        <path d={path((s) => s.bench)} fill="none" stroke="var(--bench)" strokeWidth={2} strokeLinejoin="round" />
        <path d={path((s) => s.acct)} fill="none" stroke="var(--brass)" strokeWidth={2} strokeLinejoin="round" />
        <text x={x(series.length - 1) + 6} y={y(last.acct)} dy="0.32em" className="fill-text-primary text-[11px]">
          {pct(last.acct, 1)}
        </text>
        {last.bench !== undefined && (
          <text x={x(series.length - 1) + 6} y={y(last.bench)} dy="0.32em" className="fill-text-secondary text-[11px]">
            {pct(last.bench, 1)}
          </text>
        )}
        {h && hover !== null && (
          <g>
            <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={H - pad.b} className="stroke-text-muted" strokeWidth={1} strokeDasharray="3 3" />
            <circle cx={x(hover)} cy={y(h.acct)} r={4} fill="var(--brass)" stroke="var(--card)" strokeWidth={2} />
            {h.bench !== undefined && <circle cx={x(hover)} cy={y(h.bench)} r={4} fill="var(--bench)" stroke="var(--card)" strokeWidth={2} />}
          </g>
        )}
      </svg>
    </div>
  );
}

function WeekBars({ weeks, goal }: { weeks: { week: string; return_pct: number }[]; goal: number }) {
  const [hover, setHover] = useState<number | null>(null);
  if (weeks.length === 0) return <p className="py-8 text-center text-ui text-text-muted">The first week's result appears after its last session.</p>;
  const W = 760, H = 200, pad = { t: 12, r: 12, b: 24, l: 48 };
  const vals = weeks.map((wk) => wk.return_pct);
  const hi = Math.max(goal, ...vals, 0) * 1.1, lo = Math.min(0, ...vals) * 1.1;
  const y = (v: number) => pad.t + ((hi - v) / (hi - lo || 1)) * (H - pad.t - pad.b);
  const band = (W - pad.l - pad.r) / weeks.length;
  const bw = Math.min(28, band * 0.6);
  const hw = hover !== null ? weeks[hover] : undefined;
  return (
    <div>
      <p className="h-5 text-meta text-text-secondary" aria-live="polite">{hw ? `Week of ${hw.week}: ${pct(hw.return_pct)}` : ""}</p>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label="Weekly returns against the goal">
        <line x1={pad.l} x2={W - pad.r} y1={y(0)} y2={y(0)} className="stroke-text-muted" strokeWidth={1} />
        <line x1={pad.l} x2={W - pad.r} y1={y(goal)} y2={y(goal)} className="stroke-text-muted" strokeWidth={1} strokeDasharray="4 4" />
        <text x={pad.l - 8} y={y(goal)} dy="0.32em" textAnchor="end" className="fill-text-muted text-[11px]">
          goal {goal}%
        </text>
        <text x={pad.l - 8} y={y(0)} dy="0.32em" textAnchor="end" className="fill-text-muted text-[11px]">
          0%
        </text>
        {weeks.map((wk, i) => {
          const cx = pad.l + band * i + band / 2;
          const top = Math.min(y(wk.return_pct), y(0));
          return (
            <g key={wk.week} onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)}>
              <rect x={cx - band / 2} y={pad.t} width={band} height={H - pad.t - pad.b} className={hover === i ? "fill-bg-panel-hover" : "fill-transparent"} />
              <rect x={cx - bw / 2} y={top} width={bw} height={Math.max(1, Math.abs(y(wk.return_pct) - y(0)))} rx={4} className={wk.return_pct >= 0 ? "fill-semantic-up" : "fill-semantic-down"} />
            </g>
          );
        })}
      </svg>
    </div>
  );
}

// ---- activity -------------------------------------------------------------------

function Activity({ wallet: w }: { wallet: PaperWallet }) {
  const orders = useQuery({ queryKey: ["paper-orders", w.id, "all"], queryFn: () => paperApi.orders(w.id) });
  const act = useQuery({ queryKey: ["paper-activity", w.id], queryFn: () => paperApi.activity(w.id) });
  const payments = useQuery({ queryKey: ["paper-payments", w.id], queryFn: () => paperApi.payments(w.id) });
  const [view, setView] = useState<"orders" | "money" | "ledger">("orders");
  return (
    <div className="flex flex-col gap-4 px-5 py-6 md:px-8">
      <Segmented
        label="Show"
        value={view}
        onChange={setView}
        options={[
          { value: "orders", label: "Orders" },
          { value: "money", label: "Deposits & withdrawals" },
          { value: "ledger", label: "Ledger" },
        ]}
      />
      {view === "orders" && <Card>{(orders.data?.orders ?? []).length === 0 ? <p className="px-5 py-5 text-ui text-text-muted">No orders yet.</p> : <OrderTable orders={orders.data?.orders ?? []} />}</Card>}
      {view === "money" && (
        <Card>
          <table className="w-full text-ui">
            <thead>
              <tr className="border-b border-border-subtle text-left text-meta text-text-muted">
                <th className="px-5 py-2 font-medium">When</th>
                <th className="px-3 py-2 font-medium">Type</th>
                <th className="px-3 py-2 text-right font-medium">Amount</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-5 py-2 font-medium">Method</th>
              </tr>
            </thead>
            <tbody>
              {(payments.data?.payments ?? []).map((p) => (
                <tr key={p.id} className="border-b border-border-subtle last:border-0">
                  <td className="px-5 py-2.5 text-meta text-text-muted">{formatDateTime(p.created_at)}</td>
                  <td className="px-3 py-2.5">{p.direction}</td>
                  <td className="px-3 py-2.5 text-right font-num">{usd(p.direction === "deposit" ? p.amount_cents : -p.amount_cents)}</td>
                  <td className="px-3 py-2.5">
                    <Pill tone={p.status === "succeeded" ? "up" : p.status === "failed" ? "down" : "muted"}>{p.status}</Pill>
                    {p.failure_reason && <span className="ml-2 text-meta text-text-muted">{p.failure_reason}</span>}
                  </td>
                  <td className="px-5 py-2.5 text-meta text-text-secondary">{p.provider}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}
      {view === "ledger" && (
        <Card title="Double-entry ledger" sub="Every movement of value, as postings that sum to zero. Cash and securities are the wallet's; external is the outside world; realized P&L is shown as a credit.">
          <ul className="divide-y divide-border-subtle border-t border-border-subtle">
            {(act.data?.ledger ?? []).map((t) => (
              <li key={t.id} className="grid gap-2 px-5 py-2.5 sm:grid-cols-[180px_minmax(0,1fr)_minmax(0,1.2fr)]">
                <span className="text-meta text-text-muted">{formatDateTime(t.created_at)}</span>
                <span className="text-ui text-text-primary">{t.memo || t.kind}</span>
                <span className="flex flex-wrap gap-x-4 gap-y-0.5 font-num text-meta tabular-nums">
                  {t.postings.map((p) => (
                    <span key={p.account} className={p.amount_cents >= 0 ? "text-text-secondary" : "text-text-muted"}>
                      {p.account} {usd(p.amount_cents)}
                    </span>
                  ))}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
