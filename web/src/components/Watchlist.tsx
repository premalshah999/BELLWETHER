import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, Copy, Pencil, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, type BulkAddResult, type Watchlist as List } from "../lib/api";
import { usePersisted } from "../lib/layout";


/**
 * The watchlists.
 *
 * On the right of the screen now: the left is the navigation, and a watchlist
 * is a working set rather than a way of moving around the app.
 *
 * Up to five named lists, one shown at a time. A tab strip was the obvious
 * layout and the wrong one: five names plus their counts do not fit in a
 * 288px rail, and truncating them to "Posi…" defeats the point of naming a
 * list. So the current list is a heading that opens a menu, which costs one
 * click and keeps every name readable.
 */
export function Watchlist({
  selected,
  onSelect,
}: {
  selected: string;
  onSelect: (symbol: string) => void;
}) {
  const qc = useQueryClient();
  const [activeId, setActiveId] = usePersisted<number | null>("watchlist.active", null);
  const [menu, setMenu] = useState(false);
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState(false);

  const lists = useQuery({ queryKey: ["watchlists"], queryFn: api.watchlists });
  const all = lists.data?.watchlists ?? [];

  // A remembered id can outlive the list it names — the other operator may
  // have deleted it. Falling back to the first list keeps the rail populated
  // instead of showing an empty pane for a list that no longer exists.
  const active = all.find((l) => l.id === activeId) ?? all[0];

  const items = useQuery({
    queryKey: ["watchlist-items", active?.id],
    queryFn: () => api.watchlistItems(active!.id),
    enabled: active != null,
    staleTime: 30_000,
  });

  // Paint the persisted snapshot first, then refresh upstream prices without
  // making the entire rail look empty for several seconds.
  const freshItems = useQuery({
    queryKey: ["watchlist-items-fresh", active?.id],
    queryFn: () => api.watchlistItems(active!.id, true),
    enabled: active != null && items.isSuccess,
    refetchInterval: 120_000,
    staleTime: 60_000,
  });
  useEffect(() => {
    if (active && freshItems.data) {
      qc.setQueryData(["watchlist-items", active.id], freshItems.data);
    }
  }, [active, freshItems.data, qc]);

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["watchlists"] });
    qc.invalidateQueries({ queryKey: ["watchlist-items"] });
    qc.invalidateQueries({ queryKey: ["watchlist-items-fresh"] });
  };

  const remove = useMutation({
    mutationFn: (symbol: string) => api.removeFromWatchlist(active!.id, symbol),
    onSuccess: refresh,
  });

  const rows = items.data?.items ?? [];

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      {/* The header is hand-built rather than Panel's, because the title is a
          control here and Panel's title is text. */}
      <div className="flex h-9 shrink-0 items-center justify-between gap-2 border-b border-border-subtle bg-bg-panel px-3.5">
        <button
          type="button"
          onClick={() => setMenu((v) => !v)}
          className="flex min-w-0 items-center gap-1.5 font-mono text-micro font-medium uppercase tracking-[0.14em] text-text-muted transition-colors hover:text-text-primary"
        >
          <span className="truncate">{active?.name ?? "Watchlist"}</span>
          {rows.length > 0 && <span className="shrink-0 text-text-muted">· {rows.length}</span>}
          <ChevronDown size={11} className="shrink-0" />
        </button>
        <div className="flex shrink-0 items-center gap-2">
          <button
            type="button"
            title={freshItems.isError ? "Price refresh failed — try again" : "Refresh prices"}
            onClick={() => freshItems.refetch()}
            className={freshItems.isError ? "text-semantic-down" : "text-text-muted transition-colors hover:text-brand"}
          >
            <RefreshCw size={12} className={freshItems.isFetching ? "animate-spin" : ""} />
          </button>
          <button
            type="button"
            title="Add instruments"
            onClick={() => setAdding((v) => !v)}
            className="text-text-muted transition-colors hover:text-brand"
          >
            <Plus size={13} />
          </button>
        </div>
      </div>

      {menu && active && (
        <ListMenu
          lists={all}
          max={lists.data?.max ?? 5}
          active={active}
          onPick={(id) => {
            setActiveId(id);
            setMenu(false);
          }}
          onClose={() => setMenu(false)}
          onRename={() => {
            setMenu(false);
            setRenaming(true);
          }}
          onChanged={(id) => {
            refresh();
            if (id != null) setActiveId(id);
          }}
        />
      )}

      {renaming && active && (
        <RenameBox
          list={active}
          onDone={() => {
            setRenaming(false);
            refresh();
          }}
          onCancel={() => setRenaming(false)}
        />
      )}

      {adding && active && (
        <BulkAdd
          list={active}
          onDone={() => {
            refresh();
          }}
          onClose={() => setAdding(false)}
        />
      )}

      <div className="min-h-0 flex-1 divide-y divide-border-subtle overflow-y-auto">
        {rows.map((row) => {
          const pct = row.change_percent;
          const isActive = row.symbol === selected;
          return (
            <div
              key={row.symbol}
              onClick={() => onSelect(row.symbol)}
              className={
                "group flex w-full cursor-pointer items-center gap-2 px-3.5 py-2.5 text-left transition-colors " +
                (isActive ? "bg-brand-muted" : "hover:bg-bg-panel-hover")
              }
            >
              {/* The active marker is a 2px edge rather than a filled row, so
                  the selection never competes with the price for attention. */}
              <span
                className={"h-7 w-px shrink-0 " + (isActive ? "bg-brand" : "bg-transparent")}
              />
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono text-ui text-text-primary">
                  {row.ticker}
                </span>
                <span className="mt-0.5 block truncate text-meta text-text-muted">
                  {row.note || row.exchange}
                </span>
              </span>
              {row.spark && row.spark.length > 3 && (
                <Spark points={row.spark} up={(pct ?? 0) >= 0} />
              )}
              <span className="shrink-0 text-right">
                <span className="block font-mono text-ui text-text-primary">
                  {row.price == null ? "—" : formatPrice(row.price, row.currency)}
                </span>
                <span
                  className={
                    "mt-0.5 block font-mono text-meta " +
                    (pct == null
                      ? "text-text-muted"
                      : pct >= 0
                        ? "text-semantic-up"
                        : "text-semantic-down")
                  }
                >
                  {pct == null ? "—" : `${pct >= 0 ? "+" : ""}${pct.toFixed(2)}%`}
                </span>
              </span>
              <button
                type="button"
                title="Remove"
                onClick={(e) => {
                  e.stopPropagation();
                  remove.mutate(row.symbol);
                }}
                className="shrink-0 opacity-0 transition-opacity group-hover:opacity-100"
              >
                <X size={10} className="text-text-muted hover:text-semantic-down" />
              </button>
            </div>
          );
        })}
        {rows.length === 0 && !items.isLoading && (
          <p className="px-3.5 py-6 text-meta leading-relaxed text-text-muted">
            {active ? `${active.name} is empty.` : "No lists yet."} Use{" "}
            <Plus size={10} className="inline" /> to paste in a batch of tickers.
          </p>
        )}
      </div>
    </div>
  );
}

/**
 * The list switcher.
 *
 * Also where a list is created, copied and deleted, because those are all
 * "which list" questions and splitting them across a menu and a settings pane
 * would mean two places to look for one idea.
 */
function ListMenu({
  lists,
  max,
  active,
  onPick,
  onClose,
  onRename,
  onChanged,
}: {
  lists: List[];
  max: number;
  active: List;
  onPick: (id: number) => void;
  onClose: () => void;
  onRename: () => void;
  onChanged: (id?: number | null) => void;
}) {
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [copying, setCopying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const box = useRef<HTMLDivElement>(null);

  // Click-away rather than a backdrop: a full-screen overlay would swallow the
  // first click on whatever the operator actually wanted next.
  useEffect(() => {
    const away = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) onClose();
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("mousedown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", away);
      document.removeEventListener("keydown", esc);
    };
  }, [onClose]);

  const create = useMutation({
    mutationFn: () => api.createWatchlist(name.trim()),
    onSuccess: (l) => {
      setName("");
      setCreating(false);
      setError(null);
      onChanged(l.id);
    },
    onError: (e: Error) => setError(e.message),
  });

  const copy = useMutation({
    mutationFn: (to: number) => api.copyWatchlist(active.id, to),
    onSuccess: () => {
      setCopying(false);
      setError(null);
      onChanged();
    },
    onError: (e: Error) => setError(e.message),
  });

  const del = useMutation({
    mutationFn: () => api.deleteWatchlist(active.id),
    onSuccess: () => onChanged(null),
    onError: (e: Error) => setError(e.message),
  });

  return (
    <div
      ref={box}
      className="border-b border-border-subtle bg-bg-base"
    >
      {lists.map((l) => (
        <button
          key={l.id}
          type="button"
          onClick={() => onPick(l.id)}
          className="flex w-full items-center gap-2 px-3.5 py-2 text-left transition-colors hover:bg-bg-panel-hover"
        >
          <Check
            size={11}
            className={l.id === active.id ? "shrink-0 text-brand" : "shrink-0 text-transparent"}
          />
          <span className="min-w-0 flex-1 truncate text-ui text-text-primary">{l.name}</span>
          <span className="shrink-0 font-mono text-meta text-text-muted">{l.count}</span>
        </button>
      ))}

      {copying && (
        <div className="border-t border-border-subtle px-3.5 py-2">
          <p className="mb-1.5 text-meta text-text-muted">
            Copy {active.count} from {active.name} into
          </p>
          <div className="flex flex-wrap gap-1.5">
            {lists
              .filter((l) => l.id !== active.id)
              .map((l) => (
                <button
                  key={l.id}
                  type="button"
                  onClick={() => copy.mutate(l.id)}
                  className="border border-border-subtle px-2 py-1 font-mono text-meta text-text-secondary transition-colors hover:border-brand hover:text-brand"
                >
                  {l.name}
                </button>
              ))}
            {lists.length < 2 && (
              <span className="text-meta text-text-muted">
                There is nowhere to copy to yet — make a second list first.
              </span>
            )}
          </div>
        </div>
      )}

      {creating && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate();
          }}
          className="border-t border-border-subtle px-3.5 py-2"
        >
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => e.key === "Escape" && setCreating(false)}
            placeholder="List name"
            className="w-full border border-border-subtle bg-bg-panel px-2 py-1 text-meta outline-none focus:border-brand"
          />
        </form>
      )}

      {error && <p className="px-3.5 pb-1.5 text-meta text-semantic-down">{error}</p>}

      <div className="flex items-center gap-3 border-t border-border-subtle px-3.5 py-2">
        <MenuAction
          icon={Plus}
          label={lists.length >= max ? `Limit ${max}` : "New"}
          disabled={lists.length >= max}
          title={
            lists.length >= max
              ? `Five lists is the limit. Rename or delete one to make room.`
              : "Create a list"
          }
          onClick={() => setCreating((v) => !v)}
        />
        <MenuAction icon={Pencil} label="Rename" onClick={onRename} />
        <MenuAction
          icon={Copy}
          label="Copy to"
          disabled={active.count === 0}
          title={active.count === 0 ? "This list is empty." : "Copy every instrument to another list"}
          onClick={() => setCopying((v) => !v)}
        />
        <MenuAction
          icon={Trash2}
          label="Delete"
          tone="down"
          disabled={lists.length <= 1}
          title={lists.length <= 1 ? "The last list cannot be deleted." : `Delete ${active.name}`}
          onClick={() => del.mutate()}
        />
      </div>
    </div>
  );
}

function MenuAction({
  icon: Icon,
  label,
  onClick,
  disabled,
  title,
  tone,
}: {
  icon: typeof Plus;
  label: string;
  onClick: () => void;
  disabled?: boolean;
  title?: string;
  tone?: "down";
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      className={
        "flex items-center gap-1 text-meta transition-colors disabled:cursor-not-allowed disabled:opacity-40 " +
        (tone === "down"
          ? "text-text-muted hover:text-semantic-down"
          : "text-text-muted hover:text-brand")
      }
    >
      <Icon size={11} />
      {label}
    </button>
  );
}

function RenameBox({
  list,
  onDone,
  onCancel,
}: {
  list: List;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(list.name);
  const [error, setError] = useState<string | null>(null);
  const rename = useMutation({
    mutationFn: () => api.renameWatchlist(list.id, name.trim()),
    onSuccess: onDone,
    onError: (e: Error) => setError(e.message),
  });
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) rename.mutate();
      }}
      className="border-b border-border-subtle px-3.5 py-2"
    >
      <input
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && onCancel()}
        className="w-full border border-border-subtle bg-bg-base px-2 py-1 text-meta outline-none focus:border-brand"
      />
      {error && <p className="mt-1 text-meta text-semantic-down">{error}</p>}
    </form>
  );
}

/**
 * Bulk add.
 *
 * A textarea rather than a single field, because the way instruments arrive is
 * pasted — out of a spreadsheet column, a broker export, a message. The server
 * splits on commas, newlines, tabs and semicolons but deliberately not spaces,
 * so a pasted line of prose does not become one "instrument" per word.
 */
function BulkAdd({
  list,
  onDone,
  onClose,
}: {
  list: List;
  onDone: () => void;
  onClose: () => void;
}) {
  const [text, setText] = useState("");
  const [result, setResult] = useState<BulkAddResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const add = useMutation({
    mutationFn: () => api.addToWatchlist(list.id, text),
    onSuccess: (r) => {
      // Normalised on arrival. The server sends all three, but a missing key
      // here would white-screen the rail rather than fail the add, and a
      // paste box is not worth that risk.
      const safe = {
        added: r.added ?? [],
        skipped: r.skipped ?? [],
        rejected: r.rejected ?? [],
      };
      setResult(safe);
      setError(null);
      if (safe.added.length > 0) setText("");
      onDone();
    },
    onError: (e: Error) => setError(e.message),
  });

  return (
    <div className="border-b border-border-subtle bg-bg-base px-3.5 py-2.5">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (text.trim()) add.mutate();
        }}
      >
        <textarea
          autoFocus
          rows={3}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") onClose();
            // Enter submits; Shift+Enter is a newline, which is what a paste
            // of many tickers needs.
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              if (text.trim()) add.mutate();
            }
          }}
          placeholder={"RELIANCE, TCS, INFY\nAAPL\nHDFCBANK.NSE"}
          className="w-full resize-y border border-border-subtle bg-bg-panel px-2 py-1.5 font-mono text-meta leading-relaxed outline-none focus:border-brand"
        />
        <div className="mt-1.5 flex items-center justify-between">
          <span className="text-meta text-text-muted">
            Commas, newlines or semicolons. Enter to add.
          </span>
          <button
            type="submit"
            disabled={!text.trim() || add.isPending}
            className="border border-border-subtle px-2 py-0.5 font-mono text-micro uppercase tracking-wider text-text-secondary transition-colors hover:border-brand hover:text-brand disabled:opacity-40"
          >
            {add.isPending ? "Adding" : "Add"}
          </button>
        </div>
      </form>

      {error && <p className="mt-1.5 text-meta text-semantic-down">{error}</p>}

      {/* Three outcomes reported separately. "12 added" alone hides that four
          were already there and two were not tickers at all — and those are
          the two the operator needs to act on. */}
      {result && (
        <div className="mt-2 space-y-0.5 text-meta">
          {result.added.length > 0 && (
            <p className="text-semantic-up">Added {result.added.length}.</p>
          )}
          {result.skipped.length > 0 && (
            <p className="text-text-muted">
              Already on {list.name}: {result.skipped.join(", ")}
            </p>
          )}
          {result.rejected.length > 0 && (
            <p className="text-semantic-down">
              Not a ticker: {result.rejected.join(", ")}
            </p>
          )}
          {result.added.length === 0 &&
            result.skipped.length === 0 &&
            result.rejected.length === 0 && (
              <p className="text-text-muted">Nothing to add.</p>
            )}
        </div>
      )}
    </div>
  );
}

/**
 * A 40x16 trace of the recent range.
 *
 * Drawn as a bare polyline with no axes, fill or dots: at this size any of
 * those would be noise, and the only question it answers is "which way has
 * this been going".
 */
function Spark({ points, up }: { points: number[]; up: boolean }) {
  const w = 44;
  const h = 18;
  const lo = Math.min(...points);
  const hi = Math.max(...points);
  const span = hi - lo || 1;
  const d = points
    .map((p, i) => {
      const x = (i / (points.length - 1)) * w;
      const y = h - ((p - lo) / span) * h;
      return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  return (
    <svg width={w} height={h} className="shrink-0" aria-hidden="true">
      <path
        d={d}
        fill="none"
        strokeWidth={1}
        className={up ? "stroke-semantic-up" : "stroke-semantic-down"}
      />
    </svg>
  );
}

function formatPrice(v: number, currency: string) {
  const symbol = currency === "INR" ? "₹" : currency === "USD" ? "$" : "";
  return symbol + v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
