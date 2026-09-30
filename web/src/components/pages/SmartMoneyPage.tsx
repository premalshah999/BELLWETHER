import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, ExternalLink, Plus, Search, Users, X } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api, ApiError, type FundMove, type FundQuery, type FundSummary, type InsiderTrade } from "../../lib/api";
import { useUrlState } from "../../lib/url";
import { BarList, money } from "../charts/BarList";
import { PageHeader, Segmented, Select, SkeletonRows } from "../ui/controls";
import { CongressPage } from "./CongressPage";
import { formatDate } from "../../lib/format";

const TABS = [
  { value: "overview", label: "Overview" },
  { value: "insiders", label: "Insiders" },
  { value: "funds", label: "Funds" },
  { value: "congress", label: "Congress" },
] as const;
type Tab = (typeof TABS)[number]["value"];

const WINDOWS = [
  { value: "30", label: "Last 30 days" },
  { value: "90", label: "Last 90 days" },
  { value: "180", label: "Last 6 months" },
  { value: "365", label: "Last year" },
] as const;

const date = (iso: string | undefined) => formatDate(iso, { month: "short", day: "numeric" });

function secURL(t: InsiderTrade) {
  return `https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=${t.owner_cik}&type=4&dateb=&owner=include&count=40`;
}

/**
 * Who is buying: company insiders, famous investors and members of Congress,
 * from the filings each is legally required to make.
 */
export function SmartMoneyPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const navigate = useNavigate();
  const { tab: routeTab } = useParams();
  const tab: Tab = (TABS.find((t) => t.value === routeTab)?.value ?? "overview") as Tab;
  const [win, setWin] = useUrlState<(typeof WINDOWS)[number]["value"]>("days", "90");
  const [fundCIK, setFundCIK] = useUrlState("fund", "");
  const days = Number(win);
  const openFund = (f: FundSummary | { cik: string }) => navigate(`/smartmoney/funds?fund=${f.cik}`);

  const toChart = (s: string) => {
    onSelect(s);
    navigate("/charts");
  };

  const { data, isLoading } = useQuery({
    queryKey: ["smartmoney", days],
    queryFn: () => api.smartMoneyOverview(days),
    staleTime: 5 * 60_000,
    enabled: tab === "overview",
  });

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="Who's buying"
        subtitle="What company insiders, famous investors and members of Congress are trading, from their SEC and House filings."
        actions={tab !== "congress" && tab !== "funds" ? <Select label="Window" value={win} onChange={setWin} options={WINDOWS} align="right" /> : undefined}
      >
        <Segmented label="View" value={tab} onChange={(v) => navigate(`/smartmoney/${v}${v === "overview" || v === "insiders" ? `?days=${win}` : ""}`)} options={TABS} />
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {tab === "congress" ? (
          <CongressPage onSelect={onSelect} embedded />
        ) : tab === "insiders" ? (
          <InsidersTab days={days} onSymbol={toChart} />
        ) : tab === "funds" ? (
          fundCIK ? (
            <FundDetail cik={fundCIK} onBack={() => setFundCIK("")} onSymbol={toChart} />
          ) : (
            <FundsTab onOpen={openFund} />
          )
        ) : isLoading || !data ? (
          <SkeletonRows count={8} height={60} />
        ) : (
          <Overview data={data} onSymbol={toChart} onFund={openFund} />
        )}
      </div>
    </div>
  );
}

function Card({ title, sub, children, className = "" }: { title: string; sub?: string; children: ReactNode; className?: string }) {
  return (
    <section className={"min-w-0 overflow-hidden rounded-xl border border-border-subtle bg-bg-card " + className}>
      <header className="px-5 pb-2 pt-4">
        <h2 className="text-[15px] font-semibold text-text-primary">{title}</h2>
        {sub && <p className="mt-0.5 text-meta text-text-muted">{sub}</p>}
      </header>
      {children}
    </section>
  );
}

function Overview({
  data,
  onSymbol,
  onFund,
}: {
  data: NonNullable<ReturnType<typeof useOverviewType>>;
  onSymbol: (s: string) => void;
  onFund: (f: FundSummary) => void;
}) {
  const buys = data.top_buys.slice(0, 10).map((l) => ({
    key: l.symbol,
    label: l.symbol,
    value: l.buy_value,
    display: `${money(l.buy_value)} · ${l.buyers}`,
    sub: l.issuer,
    onClick: () => onSymbol(l.symbol),
  }));
  const sells = data.top_sells.slice(0, 10).map((l) => ({
    key: l.symbol,
    label: l.symbol,
    value: l.sell_value,
    display: `${money(l.sell_value)} · ${l.sellers}`,
    sub: l.issuer,
    onClick: () => onSymbol(l.symbol),
  }));
  const noData = data.insider_count === 0 && data.funds.every((f) => !f.period);
  if (noData) {
    return (
      <div className="flex max-w-lg flex-col items-start gap-3 px-8 py-12">
        <Users size={22} className="text-text-muted" />
        <p className="text-display font-semibold text-text-primary">Filings are still being collected.</p>
        <p className="text-ui text-text-secondary">
          Insider trades are read from SEC Form 4s every half hour, and fund holdings from 13Fs daily. The first pass takes a few
          minutes.
        </p>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-5 px-5 pb-10 md:px-8">
      <div className="grid gap-5 lg:grid-cols-2">
        <Card title="Where insiders are buying" sub="Open-market purchases by officers, directors and 10% owners. Value and number of buyers.">
          <BarList data={buys} tone="up" empty="No insider purchases in this window." />
        </Card>
        <Card title="Where insiders are selling" sub="Open-market sales. Many are scheduled 10b5-1 plan sales, so buying says more.">
          <BarList data={sells} tone="down" empty="No insider sales in this window." />
        </Card>
      </div>

      {data.cluster_buys.length > 0 && (
        <Card title="Several insiders buying together" sub="Two or more insiders buying the same stock is the strongest insider signal: one can be personal, several is a view.">
          <ul className="grid gap-px bg-border-subtle sm:grid-cols-2 xl:grid-cols-3">
            {data.cluster_buys.slice(0, 9).map((l) => (
              <li key={l.symbol} className="bg-bg-card">
                <button type="button" onClick={() => onSymbol(l.symbol)} className="block w-full px-5 py-3.5 text-left transition-colors hover:bg-bg-panel-hover">
                  <span className="flex items-baseline justify-between gap-3">
                    <span className="font-num text-[13px] font-semibold text-text-primary">{l.symbol}</span>
                    <span className="font-num text-[12px] text-semantic-up">{money(l.buy_value)}</span>
                  </span>
                  <span className="mt-0.5 block truncate text-meta text-text-secondary">{l.issuer}</span>
                  <span className="mt-1.5 block text-meta text-text-muted">
                    {l.buyers} insiders: {(l.buyer_names ?? []).slice(0, 3).join(", ")}
                    {(l.buyer_names?.length ?? 0) > 3 ? "…" : ""}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Card title="Largest insider purchases" sub="Each open-market buy, biggest first.">
        <TradeTable trades={data.largest_buys.slice(0, 15)} onSymbol={onSymbol} />
      </Card>

      <Card title="What famous investors bought last quarter" sub="New and enlarged positions from the latest 13F filings, largest additions first.">
        <MoveTable moves={data.fund_buys.slice(0, 15)} onSymbol={onSymbol} showFund />
      </Card>

      <Card title="Famous investors" sub="Portfolios from each fund's latest quarterly 13F.">
        <div className="px-5 pb-5">
          <FundCards funds={data.funds.slice(0, 6)} onOpen={onFund} />
        </div>
      </Card>
    </div>
  );
}

// Named type helper for the overview payload.
function useOverviewType() {
  return undefined as unknown as Awaited<ReturnType<typeof api.smartMoneyOverview>>;
}

function TradeTable({ trades, onSymbol }: { trades: InsiderTrade[]; onSymbol: (s: string) => void }) {
  if (trades.length === 0) return <p className="px-5 pb-6 text-ui text-text-muted">No trades in this window.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[760px] text-ui">
        <thead>
          <tr className="border-y border-border-subtle text-left text-meta text-text-muted">
            <th className="px-5 py-2 font-medium">Date</th>
            <th className="px-3 py-2 font-medium">Company</th>
            <th className="px-3 py-2 font-medium">Insider</th>
            <th className="px-3 py-2 text-right font-medium">Shares</th>
            <th className="px-3 py-2 text-right font-medium">Price</th>
            <th className="px-5 py-2 text-right font-medium">Value</th>
          </tr>
        </thead>
        <tbody>
          {trades.map((t) => {
            const buy = t.code === "P";
            return (
              <tr key={t.accession + t.tx_date + t.shares} className="border-b border-border-subtle last:border-0 hover:bg-bg-panel-hover">
                <td className="whitespace-nowrap px-5 py-2.5 font-num text-[12px] text-text-muted">{date(t.tx_date)}</td>
                <td className="px-3 py-2.5">
                  <button type="button" onClick={() => onSymbol(t.symbol)} className="inline-flex h-6 items-center rounded-md bg-bg-chip px-2 font-num text-[12px] font-medium text-text-primary hover:bg-brand-muted hover:text-accent-text">
                    {t.symbol}
                  </button>
                </td>
                <td className="px-3 py-2.5">
                  <a href={secURL(t)} target="_blank" rel="noreferrer noopener" className="group inline-flex items-center gap-1 font-medium text-text-primary hover:text-accent-text">
                    {titleCase(t.owner_name)}
                    <ExternalLink size={11} className="opacity-0 transition-opacity group-hover:opacity-100" />
                  </a>
                  <span className="block text-meta text-text-muted">
                    {t.role}
                    {t.plan_10b5_1 ? ", scheduled 10b5-1 plan" : ""}
                  </span>
                </td>
                <td className="px-3 py-2.5 text-right font-num text-[12.5px] text-text-secondary">{Math.round(t.shares).toLocaleString("en-US")}</td>
                <td className="px-3 py-2.5 text-right font-num text-[12.5px] text-text-secondary">{t.price ? `$${t.price.toFixed(2)}` : "—"}</td>
                <td className={"px-5 py-2.5 text-right font-num text-[12.5px] font-medium " + (buy ? "text-semantic-up" : "text-semantic-down")}>
                  {buy ? "+" : "−"}
                  {money(t.value)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function titleCase(name: string) {
  // Filers write names as "Jain Ajit": surname first, often in capitals.
  return name
    .toLowerCase()
    .replace(/\b([a-z])/g, (m) => m.toUpperCase())
    .replace(/\bIi\b/g, "II")
    .replace(/\bIii\b/g, "III");
}

const KIND: Record<FundMove["kind"], { label: string; cls: string }> = {
  new: { label: "New position", cls: "bg-semantic-up-soft text-semantic-up" },
  added: { label: "Added", cls: "bg-semantic-up-soft text-semantic-up" },
  trimmed: { label: "Trimmed", cls: "bg-semantic-down-soft text-semantic-down" },
  exited: { label: "Sold out", cls: "bg-semantic-down-soft text-semantic-down" },
  held: { label: "Unchanged", cls: "bg-bg-chip text-text-secondary" },
};

function MoveTable({ moves, onSymbol, showFund }: { moves: FundMove[]; onSymbol: (s: string) => void; showFund?: boolean }) {
  if (moves.length === 0) return <p className="px-5 pb-6 text-ui text-text-muted">No moves recorded yet.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[720px] text-ui">
        <thead>
          <tr className="border-y border-border-subtle text-left text-meta text-text-muted">
            {showFund && <th className="px-5 py-2 font-medium">Investor</th>}
            <th className={(showFund ? "px-3" : "px-5") + " py-2 font-medium"}>Holding</th>
            <th className="px-3 py-2 font-medium">Move</th>
            <th className="px-3 py-2 text-right font-medium">Change</th>
            <th className="px-3 py-2 text-right font-medium">Value now</th>
            <th className="px-5 py-2 text-right font-medium">Of portfolio</th>
          </tr>
        </thead>
        <tbody>
          {moves.map((m) => (
            <tr key={m.fund_cik + m.cusip} className="border-b border-border-subtle last:border-0 hover:bg-bg-panel-hover">
              {showFund && (
                <td className="px-5 py-2.5">
                  <span className="block font-medium text-text-primary">{m.manager}</span>
                  <span className="block text-meta text-text-muted">{m.fund_name}</span>
                </td>
              )}
              <td className={(showFund ? "px-3" : "px-5") + " py-2.5"}>
                {m.symbol ? (
                  <button type="button" onClick={() => onSymbol(m.symbol)} className="inline-flex h-6 items-center rounded-md bg-bg-chip px-2 font-num text-[12px] font-medium text-text-primary hover:bg-brand-muted hover:text-accent-text">
                    {m.symbol}
                  </button>
                ) : null}
                <span className="ml-2 text-meta text-text-muted">{m.issuer}</span>
              </td>
              <td className="px-3 py-2.5">
                <span className={"rounded-full px-2 py-px text-micro font-semibold " + KIND[m.kind].cls}>{KIND[m.kind].label}</span>
              </td>
              <td className="px-3 py-2.5 text-right font-num text-[12.5px] text-text-secondary">
                {m.kind === "new" ? "New" : m.kind === "exited" ? "−100%" : `${m.change_pct > 0 ? "+" : ""}${m.change_pct.toFixed(0)}%`}
              </td>
              <td className="px-3 py-2.5 text-right font-num text-[12.5px] text-text-primary">{money(m.kind === "exited" ? m.prev_value : m.value)}</td>
              <td className="px-5 py-2.5 text-right font-num text-[12.5px] text-text-secondary">{m.weight_pct ? `${m.weight_pct.toFixed(1)}%` : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function FundCards({ funds, onOpen, onUnfollow }: { funds: FundSummary[]; onOpen: (f: FundSummary) => void; onUnfollow?: (cik: string) => void }) {
  return (
    <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {funds.map((f) => (
        <li key={f.cik} className="group relative">
          {onUnfollow && (
            <button
              type="button"
              onClick={() => onUnfollow(f.cik)}
              title="Stop following"
              aria-label={`Stop following ${f.manager}`}
              className="absolute right-2 top-2 z-10 hidden h-6 w-6 items-center justify-center rounded-md text-text-muted hover:bg-bg-panel-hover hover:text-semantic-down group-hover:flex"
            >
              <X size={13} />
            </button>
          )}
          <button
            type="button"
            onClick={() => onOpen(f)}
            disabled={!f.period}
            className="flex h-full w-full flex-col rounded-lg border border-border-subtle bg-bg-field p-4 text-left transition-colors hover:border-border-focus disabled:opacity-60"
          >
            <span className="flex items-baseline justify-between gap-2">
              <span className="text-[14px] font-semibold text-text-primary">{f.manager}</span>
              <span className="font-num text-[12px] text-text-secondary">{f.period ? money(f.total_value) : "—"}</span>
            </span>
            <span className="text-meta text-text-muted">
              {f.name}, {f.style}
            </span>
            {f.period ? (
              <>
                <span className="mt-3 flex flex-col gap-1.5">
                  {f.top.map((m) => (
                    <span key={m.cusip} className="grid grid-cols-[3.5rem_minmax(0,1fr)_2.8rem] items-center gap-2">
                      <span className="truncate font-num text-[11.5px] text-text-primary">{m.symbol || m.issuer.slice(0, 6)}</span>
                      <span className="h-1.5 overflow-hidden rounded-full bg-bg-chip">
                        <span className="block h-full rounded-full bg-brand" style={{ width: `${Math.min(100, m.weight_pct * 2)}%` }} />
                      </span>
                      <span className="text-right font-num text-[11px] text-text-muted">{m.weight_pct.toFixed(1)}%</span>
                    </span>
                  ))}
                </span>
                <span className="mt-3 text-micro text-text-muted">
                  {f.positions} positions, {f.new_positions} new, {f.exited_positions} sold out
                </span>
              </>
            ) : (
              <span className="mt-3 text-meta text-text-muted">Waiting for its first 13F to be read.</span>
            )}
          </button>
        </li>
      ))}
    </ul>
  );
}

const STYLE_ORDER = ["All", "Value", "Growth", "Activist", "Macro", "Quant", "Multi-strategy", "Index", "Sovereign"];

/**
 * The fund directory: every manager followed, searchable, plus any 13F filer
 * on SEC found by name and followed with one click.
 */
function FundsTab({ onOpen }: { onOpen: (f: FundSummary | { cik: string }) => void }) {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({ queryKey: ["funds"], queryFn: api.funds, staleTime: 5 * 60_000 });
  const [filter, setFilter] = useState("");
  const [style, setStyle] = useState("All");
  const [sort, setSort] = useState<"aum" | "name" | "new">("aum");
  const funds = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    const list = (data?.funds ?? []).filter(
      (f) =>
        (style === "All" || f.style.toLowerCase().includes(style.toLowerCase())) &&
        (!needle || `${f.manager} ${f.name} ${f.style}`.toLowerCase().includes(needle)),
    );
    return [...list].sort((a, b) =>
      sort === "name" ? a.manager.localeCompare(b.manager) : sort === "new" ? b.new_positions - a.new_positions : b.total_value - a.total_value,
    );
  }, [data, filter, style, sort]);
  const unfollow = useMutation({
    mutationFn: (cik: string) => api.unfollowFund(cik),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["funds"] }),
  });

  return (
    <div className="flex flex-col gap-5 px-5 pb-10 md:px-8">
      <FindFiler followed={new Set((data?.funds ?? []).map((f) => f.cik))} onOpen={onOpen} />
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-64">
          <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={`Filter ${data?.funds.length ?? ""} followed funds…`}
            aria-label="Filter followed funds"
            className="h-[34px] w-full rounded-md border border-border-subtle bg-bg-field pl-8 pr-3 text-meta outline-none focus:border-brand"
          />
        </div>
        <Select label="Style" value={style} onChange={setStyle} options={STYLE_ORDER.map((v) => ({ value: v, label: v === "All" ? "All styles" : v }))} />
        <Select
          label="Sort"
          value={sort}
          onChange={(v) => setSort(v as typeof sort)}
          options={[
            { value: "aum", label: "Largest first" },
            { value: "new", label: "Most new positions" },
            { value: "name", label: "Name" },
          ]}
        />
        <span className="text-meta text-text-muted">{funds.length} shown</span>
      </div>
      {isLoading ? <SkeletonRows count={6} height={120} /> : <FundCards funds={funds} onOpen={onOpen} onUnfollow={(cik) => unfollow.mutate(cik)} />}
    </div>
  );
}

/** Search SEC for any 13F filer and follow it. */
function FindFiler({ followed, onOpen }: { followed: Set<string>; onOpen: (f: { cik: string }) => void }) {
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [asked, setAsked] = useState("");
  const [note, setNote] = useState<string | null>(null);
  const found = useQuery({
    queryKey: ["fund-search", asked],
    queryFn: () => api.searchFunds(asked),
    enabled: asked.length >= 2,
    staleTime: 10 * 60_000,
  });
  const follow = useMutation({
    mutationFn: (cik: string) => api.followFund({ cik }),
    onSuccess: (f) => {
      setNote(`Following ${f.name}. Its latest two 13Fs are being read; this takes a minute, longer for very large portfolios.`);
      qc.invalidateQueries({ queryKey: ["funds"] });
      qc.invalidateQueries({ queryKey: ["fund-search"] });
    },
    onError: (e) => setNote(e instanceof ApiError ? e.message : "Could not follow that filer."),
  });
  return (
    <section className="rounded-xl border border-border-subtle bg-bg-card px-5 py-4">
      <h2 className="text-[15px] font-semibold text-text-primary">Follow any investor</h2>
      <p className="mt-0.5 text-meta text-text-muted">
        Every institution managing over $100 million files a 13F with the SEC each quarter. Search by name — a hedge fund, a
        family office, an endowment, a bank.
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          setNote(null);
          setAsked(q.trim());
        }}
        className="mt-3 flex max-w-xl gap-2"
      >
        <div className="relative flex-1">
          <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="e.g. Greenlight Capital, Jana Partners, Yale"
            aria-label="Search SEC filers"
            className="h-[34px] w-full rounded-md border border-border-subtle bg-bg-field pl-8 pr-3 text-meta outline-none focus:border-brand"
          />
        </div>
        <button type="submit" className="action-primary">
          Search SEC
        </button>
      </form>
      {note && <p className="mt-2 text-meta text-text-secondary">{note}</p>}
      {found.isFetching && <p className="mt-2 text-meta text-text-muted">Searching…</p>}
      {found.isError && <p className="mt-2 text-meta text-semantic-down">{(found.error as Error).message}</p>}
      {found.data && (
        <ul className="mt-3 divide-y divide-border-subtle rounded-md border border-border-subtle">
          {found.data.results.length === 0 && <li className="px-3 py-2 text-meta text-text-muted">No SEC filer matches “{asked}”.</li>}
          {found.data.results.map((r) => (
            <li key={r.cik} className="flex items-center gap-3 px-3 py-2">
              <span className="min-w-0 flex-1 truncate text-ui text-text-primary">{r.name}</span>
              <span className="font-num text-micro text-text-muted">CIK {r.cik.replace(/^0+/, "")}</span>
              {followed.has(r.cik) || r.followed ? (
                <button type="button" onClick={() => onOpen(r)} className="text-meta text-brand hover:underline">
                  Open
                </button>
              ) : (
                <button type="button" disabled={follow.isPending} onClick={() => follow.mutate(r.cik)} className="action-secondary h-7 px-2.5 text-meta">
                  <Plus size={13} /> Follow
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

const MOVE_FILTERS: { value: NonNullable<FundQuery["kind"]>; label: string }[] = [
  { value: "all", label: "All" },
  { value: "new", label: "New" },
  { value: "added", label: "Added" },
  { value: "trimmed", label: "Trimmed" },
  { value: "exited", label: "Sold out" },
  { value: "held", label: "Unchanged" },
];

const PAGE = 100;

/**
 * One manager's whole portfolio, however large: searched, filtered by what
 * changed, sorted and paged on the server, with the sector mix on top.
 */
function FundDetail({ cik, onBack, onSymbol }: { cik: string; onBack: () => void; onSymbol: (s: string) => void }) {
  const [q, setQ] = useUrlState("q", "");
  const [draft, setDraft] = useState(q);
  const [kind, setKind] = useUrlState<NonNullable<FundQuery["kind"]>>("kind", "all");
  const [sort, setSort] = useUrlState<NonNullable<FundQuery["sort"]>>("sort", "value");
  const [offset, setOffset] = useState(0);
  const funds = useQuery({ queryKey: ["funds"], queryFn: api.funds, staleTime: 5 * 60_000 });
  const fund = funds.data?.funds.find((f) => f.cik === cik);
  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ["fund", cik, q, kind, sort, offset],
    queryFn: () => api.fund(cik, { q, kind, sort, offset, limit: PAGE }),
    placeholderData: keepPreviousData,
    staleTime: 10 * 60_000,
  });
  const reset = <T,>(set: (v: T) => void) => (v: T) => {
    setOffset(0);
    set(v);
  };
  const counts = data?.counts ?? {};
  const all = Object.values(counts).reduce((a, n) => a + (n ?? 0), 0);

  return (
    <div className="flex flex-col gap-5 px-5 pb-10 md:px-8">
      <button type="button" onClick={onBack} className="inline-flex w-fit items-center gap-1.5 text-meta text-text-secondary hover:text-text-primary">
        <ArrowLeft size={14} /> All funds
      </button>
      <header>
        <h2 className="text-[24px] font-semibold tracking-[-0.01em] text-text-primary">{fund?.manager ?? "Fund"}</h2>
        {data && (
          <p className="mt-1 text-ui text-text-secondary">
            {fund?.name ? `${fund.name} · ` : ""}
            {money(data.filing.total_value)} across {data.filing.positions.toLocaleString()} positions, as of {date(data.filing.period)} (filed{" "}
            {date(data.filing.filed)}).{" "}
            <a
              href={`https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=${cik}&type=13F-HR&dateb=&owner=include&count=40`}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-brand hover:underline"
            >
              13F filings <ExternalLink size={12} />
            </a>
          </p>
        )}
      </header>

      {isError ? (
        <p className="text-ui text-text-secondary">
          {error instanceof ApiError && error.status === 404
            ? "This fund's first 13F is still being read. Check back in a minute."
            : (error as Error).message}
        </p>
      ) : isLoading || !data ? (
        <SkeletonRows count={10} height={40} />
      ) : (
        <>
          {data.sectors.length > 0 && (
            <section>
              <h3 className="text-meta font-medium text-text-secondary">Where the money is</h3>
              <div className="mt-2 flex h-3 overflow-hidden rounded-full bg-bg-chip">
                {data.sectors.slice(0, 8).map((s, i) => (
                  <span key={s.sector} title={`${s.sector} ${s.pct.toFixed(1)}%`} className="h-full" style={{ width: `${s.pct}%`, background: "var(--brass)", opacity: 1 - i * 0.1 }} />
                ))}
              </div>
              <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1">
                {data.sectors.slice(0, 8).map((s, i) => (
                  <li key={s.sector} className="flex items-center gap-1.5 text-meta text-text-secondary">
                    <span className="h-2 w-2 rounded-full" style={{ background: "var(--brass)", opacity: 1 - i * 0.1 }} />
                    {s.sector} <span className="font-num text-text-muted">{s.pct.toFixed(1)}%</span>
                  </li>
                ))}
              </ul>
            </section>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <form
              onSubmit={(e) => {
                e.preventDefault();
                reset(setQ)(draft.trim());
              }}
              className="relative w-64"
            >
              <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
              <input
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                placeholder="Search holdings by name or ticker…"
                aria-label="Search holdings"
                className="h-[34px] w-full rounded-md border border-border-subtle bg-bg-field pl-8 pr-8 text-meta outline-none focus:border-brand"
              />
              {draft && (
                <button type="button" aria-label="Clear" onClick={() => { setDraft(""); reset(setQ)(""); }} className="absolute right-2 top-1/2 -translate-y-1/2 text-text-muted hover:text-text-primary">
                  <X size={13} />
                </button>
              )}
            </form>
            <Segmented
              label="Moves"
              value={kind}
              onChange={reset(setKind)}
              options={MOVE_FILTERS.map((f) => ({ value: f.value, label: `${f.label} ${f.value === "all" ? all : counts[f.value as FundMove["kind"]] ?? 0}` }))}
            />
            <Select
              label="Sort"
              value={sort}
              onChange={(v) => reset(setSort)(v as NonNullable<FundQuery["sort"]>)}
              options={[
                { value: "value", label: "Largest position" },
                { value: "change", label: "Biggest dollar change" },
                { value: "change_pct", label: "Biggest % change" },
                { value: "shares", label: "Most shares" },
              ]}
            />
            {isFetching && <span className="text-meta text-text-muted">updating…</span>}
          </div>

          <div className="rounded-xl border border-border-subtle bg-bg-card">
            <MoveTable moves={data.moves} onSymbol={onSymbol} />
            <div className="flex items-center justify-between border-t border-border-subtle px-5 py-3 text-meta text-text-secondary">
              <span>
                {data.matched === 0
                  ? "Nothing matches."
                  : `${(data.offset + 1).toLocaleString()}–${Math.min(data.offset + PAGE, data.matched).toLocaleString()} of ${data.matched.toLocaleString()}`}
              </span>
              <span className="flex gap-2">
                <button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))} className="action-secondary h-8 px-3 disabled:opacity-40">
                  Previous
                </button>
                <button type="button" disabled={offset + PAGE >= data.matched} onClick={() => setOffset(offset + PAGE)} className="action-secondary h-8 px-3 disabled:opacity-40">
                  Next
                </button>
              </span>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function InsidersTab({ days, onSymbol }: { days: number; onSymbol: (s: string) => void }) {
  const [side, setSide] = useUrlState<"buy" | "sell" | "">("side", "buy");
  const [sym, setSym] = useUrlState("symbol", "");
  const [draft, setDraft] = useState(sym);
  const { data, isLoading } = useQuery({
    queryKey: ["insiders", days, side, sym],
    queryFn: () => api.insiderTrades({ days, side, symbol: sym || undefined }),
    staleTime: 5 * 60_000,
    placeholderData: (p) => p,
  });
  const trades = data?.trades ?? [];
  const total = trades.reduce((a, t) => a + (t.value ?? 0), 0);
  return (
    <div className="flex flex-col gap-4 px-5 pb-10 md:px-8">
      <div className="flex flex-wrap items-center gap-2">
        <Segmented
          label="Side"
          value={side}
          onChange={setSide}
          options={[
            { value: "buy", label: "Purchases" },
            { value: "sell", label: "Sales" },
            { value: "", label: "Both" },
          ]}
        />
        <form
          onSubmit={(e) => {
            e.preventDefault();
            setSym(draft.trim().toUpperCase());
          }}
          className="relative w-56"
        >
          <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
          <input
            value={draft}
            onChange={(e) => setDraft(e.target.value.toUpperCase())}
            placeholder="Filter by ticker…"
            aria-label="Filter by ticker"
            className="h-[34px] w-full rounded-md border border-border-subtle bg-bg-field pl-8 pr-8 text-meta outline-none focus:border-brand"
          />
          {draft && (
            <button type="button" aria-label="Clear" onClick={() => { setDraft(""); setSym(""); }} className="absolute right-2 top-1/2 -translate-y-1/2 text-text-muted hover:text-text-primary">
              <X size={13} />
            </button>
          )}
        </form>
        <span className="text-meta text-text-muted">
          {trades.length} trades, {money(total)} in total
        </span>
      </div>
      <section className="overflow-hidden rounded-xl border border-border-subtle bg-bg-card">
        {isLoading ? <SkeletonRows count={10} height={44} /> : <TradeTable trades={trades} onSymbol={onSymbol} />}
      </section>
    </div>
  );
}
