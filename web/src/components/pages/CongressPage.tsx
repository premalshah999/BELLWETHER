import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Landmark, Search, X } from "lucide-react";
import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../../lib/api";
import { safeHref, useUrlState } from "../../lib/url";
import { PageHeader, SkeletonRows } from "../ui/controls";
import { formatDate } from "../../lib/format";

/**
 * Who in Congress has traded what, from the House Clerk's own STOCK Act
 * disclosures -- and how long they waited to say so.
 *
 * Read internal/congress's package doc before changing how a filing is
 * shown: what this page has is document-level, not row-level. A filing
 * names every ticker its PDF mentioned and the earliest transaction date
 * found anywhere in it, not a per-trade type/amount/date triple, because
 * the source PDFs' layout does not support that split reliably. The "days
 * to disclose" figure is the STOCK Act's own yardstick -- filing is due
 * within 45 days of the trade -- and a filing past it is flagged rather
 * than presented the same as one filed on time.
 */
const STOCK_ACT_DEADLINE_DAYS = 45;

export function CongressPage({ onSelect, embedded }: { onSelect: (symbol: string) => void; embedded?: boolean }) {
  const navigate = useNavigate();
  const [symbol, setSymbol] = useUrlState("symbol", "");
  const [draft, setDraft] = useState(symbol);
  const [lateOnly, setLateOnly] = useState(false);

  const { data, isLoading } = useQuery({
    queryKey: ["congress-filings", symbol],
    queryFn: () => api.congressFilings({ symbol: symbol || undefined, limit: 150 }),
    refetchInterval: 5 * 60_000,
    placeholderData: (p) => p,
  });
  const all = useMemo(() => data?.filings ?? [], [data]);
  const lateCount = all.filter((f) => (f.disclosure_delay_days ?? 0) > STOCK_ACT_DEADLINE_DAYS).length;
  const filings = lateOnly ? all.filter((f) => (f.disclosure_delay_days ?? 0) > STOCK_ACT_DEADLINE_DAYS) : all;
  const open = (s: string) => {
    onSelect(s);
    navigate("/charts");
  };

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title={embedded ? "" : "Congress trades"}
        subtitle={
          isLoading
            ? "Loading House disclosures…"
            : `${all.length} recent House STOCK Act filings${symbol ? ` mentioning ${symbol}` : ""}. ${lateCount} were filed after the 45-day deadline.`
        }
        actions={
          <form
            role="search"
            onSubmit={(e) => {
              e.preventDefault();
              setSymbol(draft.trim().toUpperCase());
            }}
            className="relative w-full sm:w-56"
          >
            <Search size={15} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
            <input
              value={draft}
              onChange={(e) => setDraft(e.target.value.toUpperCase())}
              placeholder="Filter by ticker…"
              aria-label="Filter by ticker"
              spellCheck={false}
              className="h-9 w-full rounded-md border border-border-subtle bg-bg-base pl-9 pr-9 text-ui outline-none placeholder:text-text-muted hover:border-border-focus focus:border-brand"
            />
            {draft && (
              <button
                type="button"
                aria-label="Clear filter"
                onClick={() => {
                  setDraft("");
                  setSymbol("");
                }}
                className="absolute right-2 top-1/2 flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-text-muted hover:text-text-primary"
              >
                <X size={14} />
              </button>
            )}
          </form>
        }
      >
        <button
          type="button"
          aria-pressed={lateOnly}
          onClick={() => setLateOnly((v) => !v)}
          className={
            "h-8 rounded-md border px-3 text-meta font-medium transition-colors " +
            (lateOnly ? "border-brand/50 bg-brand-muted text-text-primary" : "border-border-subtle text-text-secondary hover:border-border-focus")
          }
        >
          Late filings only{lateCount ? ` (${lateCount})` : ""}
        </button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-auto">
        {isLoading ? (
          <SkeletonRows count={9} height={52} />
        ) : filings.length === 0 ? (
          <div className="flex max-w-md flex-col items-start gap-3 px-6 py-12">
            <Landmark size={22} className="text-text-muted" />
            <p className="font-reading text-display text-text-primary">
              {symbol ? `No disclosed trades in ${symbol}.` : "No filings yet."}
            </p>
            <p className="text-ui text-text-secondary">
              {symbol ? "Try another ticker, or clear the filter." : "House disclosures sync once a day."}
            </p>
          </div>
        ) : (
          <div className="mx-5 mb-8 overflow-x-auto rounded-xl border border-border-subtle bg-bg-card md:mx-8 md:overflow-x-clip">
          <table className="w-full min-w-[820px] text-ui">
            <thead className="sticky top-0 z-10 bg-bg-card shadow-[0_1px_0_var(--line)]">
              <tr className="text-left text-meta text-text-muted">
                <th className="px-6 py-2.5 font-medium">Member</th>
                <th className="px-3 py-2.5 font-medium">Filed</th>
                <th className="px-3 py-2.5 font-medium">First trade</th>
                <th className="px-3 py-2.5 font-medium" title="Days from the earliest trade in the filing to the filing itself. The STOCK Act allows 45.">
                  Time to disclose
                </th>
                <th className="px-6 py-2.5 font-medium">Tickers</th>
              </tr>
            </thead>
            <tbody>
              {filings.map((f) => {
                const delay = f.disclosure_delay_days;
                const late = (delay ?? 0) > STOCK_ACT_DEADLINE_DAYS;
                return (
                  <tr key={f.doc_id} className="border-t border-border-subtle align-top transition-colors hover:bg-bg-panel-hover">
                    <td className="px-6 py-3">
                      <a
                        href={safeHref(f.doc_url)}
                        target="_blank"
                        rel="noreferrer noopener"
                        className="group inline-flex items-center gap-1.5 font-medium text-text-primary hover:text-brand"
                        title="Open the filing"
                      >
                        {f.member}
                        <ExternalLink size={12} className="opacity-0 transition-opacity group-hover:opacity-100" />
                      </a>
                      <span className="block text-meta text-text-muted">{f.state_district}</span>
                    </td>
                    <td className="whitespace-nowrap px-3 py-3 text-text-secondary">{formatDate(f.filing_date)}</td>
                    <td className="whitespace-nowrap px-3 py-3 text-text-secondary">{formatDate(f.earliest_transaction_date)}</td>
                    <td className="px-3 py-3">
                      {delay == null ? (
                        <span className="text-text-muted">—</span>
                      ) : (
                        <span className="flex items-center gap-2.5">
                          <span className={"w-14 whitespace-nowrap " + (late ? "font-semibold text-brand" : "text-text-secondary")}>
                            {delay} {delay === 1 ? "day" : "days"}
                          </span>
                          <span className="relative h-1.5 w-20 rounded-full bg-bg-base" aria-hidden="true">
                            <span
                              className={"absolute inset-y-0 left-0 rounded-full " + (late ? "bg-brand" : "bg-border-focus")}
                              style={{ width: `${Math.min(100, (delay / 90) * 100)}%` }}
                            />
                            <span className="absolute -top-0.5 h-2.5 w-px bg-text-muted" style={{ left: "50%" }} title="45-day deadline" />
                          </span>
                          {late && <span className="text-meta font-medium text-brand">Late</span>}
                        </span>
                      )}
                    </td>
                    <td className="px-6 py-3">
                      <Tickers symbols={f.symbols} onOpen={open} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          </div>
        )}
      </div>
    </div>
  );
}

function Tickers({ symbols, onOpen }: { symbols: string[]; onOpen: (s: string) => void }) {
  const [all, setAll] = useState(false);
  if (symbols.length === 0) return <span className="text-meta text-text-muted">No ticker identified</span>;
  const shown = all ? symbols : symbols.slice(0, 6);
  return (
    <span className="flex flex-wrap gap-x-1 gap-y-0.5">
      {shown.map((s) => (
        <button
          key={s}
          type="button"
          onClick={() => onOpen(s)}
          className="inline-flex h-6 items-center rounded-md bg-bg-chip px-2 font-num text-[12px] font-medium text-text-primary transition-colors hover:bg-brand-muted hover:text-accent-text"
        >
          {s}
        </button>
      ))}
      {symbols.length > 6 && (
        <button type="button" onClick={() => setAll((v) => !v)} className="inline-flex h-6 items-center px-1.5 text-meta text-text-muted hover:text-text-primary">
          {all ? "Show fewer" : `+${symbols.length - 6} more`}
        </button>
      )}
    </span>
  );
}
