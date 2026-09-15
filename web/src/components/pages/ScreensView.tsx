import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Filter, Plus, Save, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  api,
  type Screen,
  type ScreenCondition,
  type ScreenDefinition,
} from "../../lib/api";
import { formatAgo } from "../../lib/format";
import { Empty } from "../ui/Empty";

/** A new screen starts with one condition rather than none, because an empty
 *  builder gives the operator nothing to edit and the shape of a condition is
 *  the thing that needs explaining. */
const BLANK: ScreenDefinition = {
  match: "all",
  conditions: [{ field: "volume_z", op: ">=", value: 2 }],
  sort_by: "volume_z",
  sort_desc: true,
  limit: 50,
};

/**
 * Custom scanners.
 *
 * The built-in scanner answers one question with six hand-tuned signals. This
 * answers whatever question the operator writes, over the same measurements
 * and the same 750 constituents — including, importantly, the instruments the
 * built-in scanner deliberately throws away as unremarkable.
 *
 * It runs against the last stored sweep rather than re-reading prices, which
 * is why a screen returns in milliseconds and a scan takes two minutes.
 */
export function ScreensView({ onSelect }: { onSelect: (symbol: string) => void }) {
  const qc = useQueryClient();
  const [def, setDef] = useState<ScreenDefinition>(BLANK);
  const [editing, setEditing] = useState<Screen | null>(null);
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);

  const catalogue = useQuery({ queryKey: ["screen-fields"], queryFn: api.screenCatalogue });
  const saved = useQuery({ queryKey: ["screens"], queryFn: api.screens });
  const lists = useQuery({ queryKey: ["watchlists"], queryFn: api.watchlists });

  const fields = catalogue.data?.fields ?? [];
  const ops = catalogue.data?.ops ?? [];
  const maxConditions = catalogue.data?.max_conditions ?? 12;

  // The definition is re-run on a delay rather than on every keystroke: typing
  // "25" into a threshold would otherwise run a screen for 2 on the way past.
  const [live, setLive] = useState<ScreenDefinition>(BLANK);
  useEffect(() => {
    const t = setTimeout(() => setLive(def), 400);
    return () => clearTimeout(t);
  }, [def]);

  const result = useQuery({
    queryKey: ["screen-run", live],
    queryFn: () => api.runScreen(live),
    enabled: live.conditions.length > 0,
    placeholderData: (p) => p,
  });

  const save = useMutation({
    mutationFn: () =>
      editing
        ? api.updateScreen(editing.id, editing.name, editing.description, def)
        : api.createScreen(name.trim(), "", def),
    onSuccess: (s) => {
      setEditing(s);
      setNaming(false);
      setName("");
      setError(null);
      qc.invalidateQueries({ queryKey: ["screens"] });
    },
    onError: (e: Error) => setError(e.message),
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.deleteScreen(id),
    onSuccess: () => {
      setEditing(null);
      qc.invalidateQueries({ queryKey: ["screens"] });
    },
  });

  const rows = result.data?.rows ?? [];
  const universe = result.data?.universe ?? 0;

  const patch = (i: number, c: Partial<ScreenCondition>) =>
    setDef((d) => ({
      ...d,
      conditions: d.conditions.map((x, j) => (j === i ? { ...x, ...c } : x)),
    }));

  const unit = useMemo(() => {
    const m = new Map(fields.map((f) => [f.field, f.unit]));
    return (field: string) => m.get(field) ?? "";
  }, [fields]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* Saved screens, as a single row of chips. A sidebar would cost 200px
          of the results table for a list that is at most a handful long. */}
      <div className="flex flex-wrap items-center gap-1.5 border-b border-border-subtle px-3.5 py-2">
        {(saved.data?.screens ?? []).map((s) => (
          <button
            key={s.id}
            type="button"
            onClick={() => {
              setEditing(s);
              setDef(s.definition);
              setError(null);
            }}
            title={s.last_run_at ? `Last run ${formatAgo(s.last_run_at)}` : "Never run"}
            className={
              "border px-2 py-0.5 font-mono text-meta transition-colors " +
              (editing?.id === s.id
                ? "border-brand bg-brand-muted text-brand"
                : "border-border-subtle text-text-secondary hover:border-border-focus hover:text-text-primary")
            }
          >
            {s.name}
          </button>
        ))}
        <button
          type="button"
          onClick={() => {
            setEditing(null);
            setDef(BLANK);
            setError(null);
          }}
          className="flex items-center gap-1 px-2 py-0.5 font-mono text-meta text-text-muted transition-colors hover:text-brand"
        >
          <Plus size={10} /> new
        </button>
        {editing && (
          <button
            type="button"
            onClick={() => remove.mutate(editing.id)}
            title={`Delete ${editing.name}`}
            className="ml-auto flex items-center gap-1 px-2 py-0.5 font-mono text-meta text-text-muted transition-colors hover:text-semantic-down"
          >
            <Trash2 size={10} /> delete
          </button>
        )}
      </div>

      {/* The builder. */}
      <div className="border-b border-border-subtle px-3.5 py-3">
        <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-2">
          <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
            match
          </span>
          <div className="flex border border-border-subtle">
            {(["all", "any"] as const).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => setDef((d) => ({ ...d, match: m }))}
                className={
                  "border-r border-border-subtle px-2 py-0.5 font-mono text-meta last:border-r-0 " +
                  (def.match === m
                    ? "bg-brand-muted text-brand"
                    : "text-text-muted hover:text-text-primary")
                }
              >
                {m}
              </button>
            ))}
          </div>

          <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
            within
          </span>
          <div className="flex flex-wrap gap-1">
            <ListChip
              label="all 750"
              on={!def.watchlist_ids || def.watchlist_ids.length === 0}
              onClick={() => setDef((d) => ({ ...d, watchlist_ids: [] }))}
            />
            {(lists.data?.watchlists ?? []).map((l) => {
              const on = def.watchlist_ids?.includes(l.id) ?? false;
              return (
                <ListChip
                  key={l.id}
                  label={l.name}
                  on={on}
                  onClick={() =>
                    setDef((d) => {
                      const cur = d.watchlist_ids ?? [];
                      return {
                        ...d,
                        watchlist_ids: on ? cur.filter((x) => x !== l.id) : [...cur, l.id],
                      };
                    })
                  }
                />
              );
            })}
          </div>
        </div>

        <div className="space-y-1.5">
          {def.conditions.map((c, i) => (
            <div key={i} className="flex flex-wrap items-center gap-1.5">
              <select
                value={c.field}
                onChange={(e) => patch(i, { field: e.target.value })}
                className="border border-border-subtle bg-bg-base px-1.5 py-1 font-mono text-meta text-text-primary outline-none focus:border-brand"
              >
                {fields.map((f) => (
                  <option key={f.field} value={f.field}>
                    {f.label}
                  </option>
                ))}
              </select>
              <select
                value={c.op}
                onChange={(e) => patch(i, { op: e.target.value })}
                className="border border-border-subtle bg-bg-base px-1.5 py-1 font-mono text-meta text-text-primary outline-none focus:border-brand"
              >
                {ops.map((o) => (
                  <option key={o.op} value={o.op}>
                    {o.label}
                  </option>
                ))}
              </select>
              <input
                type="number"
                step="any"
                value={c.value}
                onChange={(e) => patch(i, { value: Number(e.target.value) })}
                className="w-24 border border-border-subtle bg-bg-base px-1.5 py-1 text-right font-mono text-meta text-text-primary outline-none focus:border-brand"
              />
              <span className="w-16 font-mono text-meta text-text-muted">{unit(c.field)}</span>
              {def.conditions.length > 1 && (
                <button
                  type="button"
                  onClick={() =>
                    setDef((d) => ({
                      ...d,
                      conditions: d.conditions.filter((_, j) => j !== i),
                    }))
                  }
                  className="text-text-muted transition-colors hover:text-semantic-down"
                >
                  <X size={11} />
                </button>
              )}
            </div>
          ))}
        </div>

        <div className="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-2">
          <button
            type="button"
            disabled={def.conditions.length >= maxConditions}
            onClick={() =>
              setDef((d) => ({
                ...d,
                conditions: [...d.conditions, { field: "return_z", op: ">=", value: 2 }],
              }))
            }
            className="flex items-center gap-1 font-mono text-meta text-text-muted transition-colors hover:text-brand disabled:opacity-40"
          >
            <Plus size={10} /> condition
          </button>

          <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
            sort
          </span>
          <select
            value={def.sort_by}
            onChange={(e) => setDef((d) => ({ ...d, sort_by: e.target.value }))}
            className="border border-border-subtle bg-bg-base px-1.5 py-0.5 font-mono text-meta text-text-secondary outline-none focus:border-brand"
          >
            {fields.map((f) => (
              <option key={f.field} value={f.field}>
                {f.label}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={() => setDef((d) => ({ ...d, sort_desc: !d.sort_desc }))}
            className="font-mono text-meta text-text-muted transition-colors hover:text-brand"
          >
            {def.sort_desc ? "high → low" : "low → high"}
          </button>

          <span className="font-mono text-micro uppercase tracking-[0.12em] text-text-muted">
            top
          </span>
          <input
            type="number"
            min={1}
            max={750}
            value={def.limit}
            onChange={(e) => setDef((d) => ({ ...d, limit: Number(e.target.value) }))}
            className="w-16 border border-border-subtle bg-bg-base px-1.5 py-0.5 text-right font-mono text-meta outline-none focus:border-brand"
          />

          <div className="ml-auto flex items-center gap-2">
            {naming && (
              <input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") setNaming(false);
                  if (e.key === "Enter" && name.trim()) save.mutate();
                }}
                placeholder="Screen name"
                className="w-40 border border-border-subtle bg-bg-base px-2 py-0.5 text-meta outline-none focus:border-brand"
              />
            )}
            <button
              type="button"
              onClick={() => (editing || naming ? save.mutate() : setNaming(true))}
              disabled={save.isPending || (naming && !name.trim())}
              className="flex items-center gap-1 border border-border-subtle px-2 py-0.5 font-mono text-micro uppercase tracking-wider text-text-secondary transition-colors hover:border-brand hover:text-brand disabled:opacity-40"
            >
              <Save size={10} />
              {editing ? `save ${editing.name}` : "save"}
            </button>
          </div>
        </div>

        {error && <p className="mt-2 text-meta text-semantic-down">{error}</p>}
      </div>

      {/* Results. */}
      <div className="flex items-center gap-3 border-b border-border-subtle px-3.5 py-1.5">
        <span className="font-mono text-meta text-text-muted">
          {result.isError ? (
            <span className="text-semantic-down">
              {(result.error as Error).message}
            </span>
          ) : universe === 0 ? (
            "no scan has run yet"
          ) : (
            <>
              <span className="text-text-primary">{rows.length}</span> of {universe} measured
              {result.data?.elapsed ? ` · ${result.data.elapsed}` : ""}
              {result.data?.scan_as_of ? ` · scan ${formatAgo(result.data.scan_as_of)}` : ""}
            </>
          )}
        </span>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {rows.length === 0 ? (
          <Empty
            icon={Filter}
            title={universe === 0 ? "No scan to screen yet." : "Nothing matches."}
            hint={
              universe === 0
                ? "Run a scan from the Signals tab first — a screen queries the last sweep rather than re-reading prices."
                : "Loosen a threshold, or switch match to any."
            }
          />
        ) : (
          <table className="w-full max-w-[1200px] border-collapse">
            <thead className="sticky top-0 z-10 bg-bg-panel">
              <tr className="border-b border-border-subtle text-left">
                <Th className="w-[120px]">symbol</Th>
                <Th right>close</Th>
                <Th right>1d %</Th>
                <Th right>5d %</Th>
                <Th right>1d σ</Th>
                <Th right>vol ×</Th>
                <Th right>vol σ</Th>
                <Th right>from high</Th>
                <Th>industry</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border-subtle">
              {rows.map((r) => (
                <tr
                  key={r.symbol}
                  onClick={() => onSelect(`${r.symbol}.NSE`)}
                  className="cursor-pointer hover:bg-bg-panel-hover"
                >
                  <td className="px-2.5 py-2 font-mono text-meta text-text-primary">
                    {r.symbol}
                  </td>
                  <td className="px-2 py-1.5 text-right font-mono text-meta text-text-secondary">
                    {r.close.toLocaleString(undefined, { maximumFractionDigits: 2 })}
                  </td>
                  <Num v={r.return_1d} signed />
                  <Num v={r.return_5d} signed />
                  <td className="px-2 py-1.5 text-right font-mono text-meta text-text-secondary">
                    {r.return_z.toFixed(1)}
                  </td>
                  <td className="px-2 py-1.5 text-right font-mono text-meta text-text-secondary">
                    {r.volume_ratio >= 10 ? r.volume_ratio.toFixed(0) : r.volume_ratio.toFixed(1)}×
                  </td>
                  <td className="px-2 py-1.5 text-right font-mono text-meta text-text-primary">
                    {r.volume_z.toFixed(1)}
                  </td>
                  <td className="px-2 py-1.5 text-right font-mono text-meta text-text-muted">
                    {r.pct_from_52w_high > -0.5 ? "at high" : `${r.pct_from_52w_high.toFixed(0)}%`}
                  </td>
                  <td className="truncate px-2 py-1.5 text-meta text-text-muted">
                    {r.industry}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function ListChip({ label, on, onClick }: { label: string; on: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "border px-2 py-0.5 font-mono text-meta transition-colors " +
        (on
          ? "border-brand bg-brand-muted text-brand"
          : "border-border-subtle text-text-muted hover:text-text-primary")
      }
    >
      {label}
    </button>
  );
}

function Num({ v, signed }: { v: number; signed?: boolean }) {
  return (
    <td
      className={
        "px-2 py-1.5 text-right font-mono text-meta " +
        (v >= 0 ? "text-semantic-up" : "text-semantic-down")
      }
    >
      {signed && v >= 0 ? "+" : ""}
      {v.toFixed(2)}
    </td>
  );
}

function Th({
  children,
  right,
  className = "",
}: {
  children: string;
  right?: boolean;
  className?: string;
}) {
  return (
    <th
      className={`px-2 py-1.5 font-mono text-micro font-medium uppercase tracking-[0.12em] text-text-muted ${
        right ? "text-right" : "text-left"
      } ${className}`}
    >
      {children}
    </th>
  );
}
