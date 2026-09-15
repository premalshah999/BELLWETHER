import { useQuery } from "@tanstack/react-query";
import { Landmark } from "lucide-react";
import { useMemo, useState } from "react";
import { api } from "../../lib/api";
import { Empty } from "../ui/Empty";
import { Panel } from "../ui/Panel";
import { Pill } from "../ui/Pill";

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

// A DATE column ("2026-03-31") carries no timezone -- reformatting it
// through the display zone the way a real timestamp is would risk shifting
// it a day in either direction depending on the operator's offset. Parsed
// and rendered as plain calendar text instead.
function formatDateOnly(iso: string | undefined): string {
  if (!iso) return "—";
  const [y, m, d] = iso.split("-").map(Number);
  if (!y || !m || !d) return "—";
  return new Date(Date.UTC(y, m - 1, d)).toLocaleDateString("en-GB", {
    timeZone: "UTC",
    day: "2-digit",
    month: "short",
    year: "numeric",
  });
}

export function CongressPage({ onSelect }: { onSelect: (symbol: string) => void }) {
  const [symbol, setSymbol] = useState("");

  const { data, isLoading } = useQuery({
    queryKey: ["congress-filings", symbol],
    queryFn: () => api.congressFilings({ symbol: symbol || undefined, limit: 150 }),
    refetchInterval: 5 * 60_000,
  });
  const filings = useMemo(() => data?.filings ?? [], [data]);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-panel">
      <div className="flex h-10 shrink-0 items-center gap-3 border-b border-border-subtle px-4">
        <span className="font-mono text-micro uppercase tracking-[0.14em] text-text-muted">
          Congressional Trading
        </span>
        <input
          value={symbol}
          onChange={(e) => setSymbol(e.target.value.toUpperCase())}
          placeholder="filter by symbol…"
          spellCheck={false}
          className="w-40 border border-border-subtle bg-bg-base px-2 py-1 font-mono text-meta text-text-primary outline-none placeholder:text-text-muted focus:border-border-focus"
        />
        <span className="ml-auto font-mono text-meta text-text-muted">{filings.length} filings</span>
      </div>

      {isLoading ? (
        <div className="flex flex-1 items-center justify-center text-meta text-text-muted">loading…</div>
      ) : filings.length === 0 ? (
        <Empty
          icon={Landmark}
          title={symbol ? `No disclosed trades in ${symbol}.` : "No filings yet."}
          hint={
            symbol
              ? "Try a different symbol, or clear the filter."
              : "House Clerk disclosures are synced once a day; check back after the next sync."
          }
        />
      ) : (
        <Panel scroll className="min-h-0 flex-1 border-0">
          <ul className="divide-y divide-border-subtle">
            {filings.map((f) => {
              const late = (f.disclosure_delay_days ?? 0) > STOCK_ACT_DEADLINE_DAYS;
              return (
                <li key={f.doc_id} className="px-4 py-3">
                  <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                    <a
                      href={f.doc_url}
                      target="_blank"
                      rel="noreferrer noopener"
                      className="text-ui text-text-primary hover:text-brand"
                    >
                      {f.member}
                    </a>
                    <span className="font-mono text-meta text-text-muted">{f.state_district}</span>
                    <span className="font-mono text-meta text-text-muted">
                      filed {formatDateOnly(f.filing_date)}
                    </span>
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-1.5">
                    {f.symbols.length === 0 ? (
                      <span className="font-mono text-meta text-text-muted">no ticker resolved</span>
                    ) : (
                      f.symbols.map((s) => (
                        <button
                          key={s}
                          type="button"
                          onClick={() => onSelect(s)}
                          className="border border-border-subtle px-1.5 py-px font-mono text-micro uppercase tracking-wider text-text-secondary hover:border-brand/40 hover:text-brand"
                        >
                          {s}
                        </button>
                      ))
                    )}
                    {f.earliest_transaction_date && (
                      <Pill tone="muted">traded from {formatDateOnly(f.earliest_transaction_date)}</Pill>
                    )}
                    {f.disclosure_delay_days != null && (
                      <Pill tone={late ? "down" : "neutral"} title="Days between the earliest transaction found and this filing">
                        {f.disclosure_delay_days}d to disclose
                      </Pill>
                    )}
                    {late && <Pill tone="down">past the 45-day STOCK Act deadline</Pill>}
                  </div>
                </li>
              );
            })}
          </ul>
        </Panel>
      )}
    </div>
  );
}
