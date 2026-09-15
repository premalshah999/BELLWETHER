import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "../lib/api";

/**
 * Symbol search, opened with ⌘K.
 *
 * A dialog rather than a permanent field: the search is used a few times an
 * hour, and a permanent input would take space from the watchlist on every
 * screen for the sake of those few uses.
 */
export function CommandPalette({
  open,
  onClose,
  onSelect,
}: {
  open: boolean;
  onClose: () => void;
  onSelect: (symbol: string) => void;
}) {
  const [q, setQ] = useState("");
  const [cursor, setCursor] = useState(0);

  const { data } = useQuery({
    queryKey: ["symbol-search", q],
    queryFn: () => api.searchSymbols(q),
    enabled: open && q.trim().length >= 1,
  });

  const results = data?.results ?? [];

  useEffect(() => {
    if (!open) {
      setQ("");
      setCursor(0);
    }
  }, [open]);

  useEffect(() => setCursor(0), [q]);

  if (!open) return null;

  const choose = (symbol: string) => {
    onSelect(symbol);
    onClose();
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-bg-base/80 pt-[12vh]"
      onClick={onClose}
    >
      <div
        className="w-full max-w-lg border border-border-focus bg-bg-panel"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-border-subtle px-3">
          <Search size={13} className="shrink-0 text-text-muted" />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") onClose();
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setCursor((c) => Math.min(c + 1, results.length - 1));
              }
              if (e.key === "ArrowUp") {
                e.preventDefault();
                setCursor((c) => Math.max(c - 1, 0));
              }
              if (e.key === "Enter" && results[cursor]) choose(results[cursor]!.symbol);
            }}
            placeholder="Search companies or tickers…"
            className="w-full bg-transparent py-2.5 text-ui outline-none placeholder:text-text-muted"
          />
          <kbd className="shrink-0 border border-border-subtle px-1 font-mono text-micro text-text-muted">
            esc
          </kbd>
        </div>

        <ul className="max-h-80 overflow-y-auto">
          {results.map((r, i) => (
            <li key={r.symbol}>
              <button
                type="button"
                onMouseEnter={() => setCursor(i)}
                onClick={() => choose(r.symbol)}
                className={
                  "flex w-full items-center gap-2 px-3 py-1.5 text-left " +
                  (i === cursor ? "bg-brand-muted" : "hover:bg-bg-panel-hover")
                }
              >
                <span className="w-24 shrink-0 font-mono text-ui text-text-primary">
                  {r.ticker}
                </span>
                <span className="min-w-0 flex-1 truncate text-meta text-text-secondary">
                  {r.name}
                </span>
                <span className="shrink-0 font-mono text-meta text-text-muted">
                  {r.exchange}
                </span>
              </button>
            </li>
          ))}
          {q.trim() && results.length === 0 && (
            <li className="px-3 py-3 text-meta text-text-muted">No match.</li>
          )}
        </ul>
      </div>
    </div>
  );
}
