import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ListChecks, Plus, Sliders, Trash2, X } from "lucide-react";
import { useEffect, useState } from "react";
import {
  api,
  type Algorithm,
  type Interval,
  type Node,
  type Operator,
  ValidationError,
} from "../../lib/api";
import { useResize } from "../../lib/layout";
import { Divider } from "../ui/Divider";
import { Empty } from "../ui/Empty";
import { Panel } from "../ui/Panel";
import { PageHeader, Segmented, Select as UiSelect } from "../ui/controls";
import { useNavigate, useParams } from "react-router-dom";
import { BacktestPanel } from "./BacktestPanel";
import { Pill } from "../ui/Pill";

const INTERVALS: Interval[] = ["5m", "15m", "1h", "1d", "1wk"];
const OPERATORS: Operator[] = ["<", "<=", ">", ">=", "crosses_above", "crosses_below"];

const WORKING_DRAFT = "algorithm.working";

const BLANK: Algorithm = {
  name: "",
  symbols: [],
  interval: "1d",
  all: [{ indicator: "rsi", period: 14, op: "<", value: 35 }],
  cooldown_hours: 12,
  notify: { telegram: true, ai_context: true },
  enabled: false,
};

export function AlgorithmsPage() {
  const qc = useQueryClient();
  // The rule being worked on survives a move between building and testing,
  // and a reload, for the rest of the browser session.
  const [draft, setDraftState] = useState<Algorithm>(() => {
    try {
      const saved = sessionStorage.getItem(WORKING_DRAFT);
      return saved ? (JSON.parse(saved) as Algorithm) : BLANK;
    } catch {
      return BLANK;
    }
  });
  const setDraft = (a: Algorithm) => {
    setDraftState(a);
    try {
      sessionStorage.setItem(WORKING_DRAFT, JSON.stringify(a));
    } catch {
      // Storage can be unavailable; the draft still lives for this view.
    }
  };
  const navigate = useNavigate();
  const [raw, setRaw] = useState(false);
  // The mode is the route, so a backtest can be linked and reloaded. Both
  // modes are rendered by this one component, which is what lets the rule
  // being edited survive the move between them.
  const { mode: routeMode } = useParams();
  const mode = routeMode === "backtest" ? "backtest" : "build";
  const [rawText, setRawText] = useState("");
  const [rawError, setRawError] = useState("");
  const side = useResize({
    key: "algorithms.side",
    initial: 288,
    min: 220,
    max: 520,
    direction: "w",
  });

  const { data: vocab } = useQuery({ queryKey: ["vocab"], queryFn: api.vocabulary });
  const { data: templates } = useQuery({ queryKey: ["templates"], queryFn: api.templates });
  const { data: existing } = useQuery({
    queryKey: ["algorithms"],
    queryFn: api.algorithms,
    refetchInterval: 60_000,
  });

  /*
   * A draft handed over from a chart.
   *
   * "Make a strategy from what I am looking at" arrives as a one-shot
   * handover in session storage. Consumed and cleared on arrival, so a later
   * visit to this page does not silently reopen a rule the operator has
   * already dealt with.
   */
  useEffect(() => {
    const raw = sessionStorage.getItem("algorithm.draft");
    if (!raw) return;
    sessionStorage.removeItem("algorithm.draft");
    try {
      setDraft(JSON.parse(raw) as Algorithm);
    } catch {
      // A malformed handover is not worth an error in front of the operator;
      // they simply get the blank builder they would have got anyway.
    }
  }, []);

  // The JSON view is generated from the draft whenever it is opened, so the
  // two tabs can never disagree about what is being edited.
  useEffect(() => {
    if (raw) setRawText(JSON.stringify(draft, null, 2));
  }, [raw, draft]);

  const validate = useMutation({ mutationFn: (a: Algorithm) => api.previewAlgorithm(a) });
  const toggle = useMutation({
    mutationFn: (a: Algorithm) => api.updateAlgorithm(a.id!, { ...a, enabled: !a.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["algorithms"] }),
  });
  const drop = useMutation({
    mutationFn: (id: number) => api.deleteAlgorithm(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["algorithms"] }),
  });

  // Saving a rule that already exists updates it rather than adding a copy.
  const save = useMutation({
    mutationFn: (a: Algorithm) => (a.id ? api.updateAlgorithm(a.id, a) : api.createAlgorithm(a)),
    onSuccess: (saved) => {
      qc.invalidateQueries({ queryKey: ["algorithms"] });
      setDraft(saved);
    },
  });

  const conditions = draft.all ?? [];
  const setConditions = (next: Node[]) => setDraft({ ...draft, all: next });

  // Backtesting is the same draft asked a different question, so it is a mode
  // of this page rather than a page of its own: a separate route would mean
  // carrying the rule between them, and a rule that has to be re-entered to be
  // tested will not get tested.
  if (mode === "backtest") {
    return (
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <PageHeader
          title="Backtest"
          subtitle={
            <>
              Testing <span className="font-medium text-text-primary">{draft.name || "an unsaved rule"}</span> on stored daily
              history. Signals fill at the next bar’s open, so a rule never trades on a close it just read.
            </>
          }
        />
        <BacktestPanel
          draft={draft}
          saved={existing?.algorithms ?? []}
          templates={(templates?.templates ?? []).map((t) => ({ ...BLANK, ...t.algorithm, name: t.title }))}
          onPick={setDraft}
          onEdit={() => navigate("/algorithms/build")}
        />
      </div>
    );
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader
        title="Algorithms"
        subtitle="Rules that watch your symbols and alert you when every condition holds on a closed bar."
        actions={
          <Segmented
            label="Editor"
            value={raw ? "json" : "visual"}
            onChange={(v) => setRaw(v === "json")}
            options={[
              { value: "visual", label: "Visual" },
              { value: "json", label: "JSON" },
            ]}
          />
        }
      />
      <div className="flex min-h-0 flex-1">
      <Panel
        scroll
        className="min-w-0 flex-1"
      >
        {raw ? (
          <div className="p-3">
            <textarea
              value={rawText}
              onChange={(e) => {
                setRawText(e.target.value);
                try {
                  setDraft(JSON.parse(e.target.value));
                  setRawError("");
                } catch (err) {
                  // Kept in the box rather than reverting: losing what someone
                  // typed because a brace is not closed yet is the fastest way
                  // to make an editor unusable.
                  setRawError((err as Error).message);
                }
              }}
              spellCheck={false}
              rows={22}
              className="w-full resize-none border border-border-subtle bg-bg-base px-2 py-2 font-mono text-meta leading-relaxed text-text-primary outline-none focus:border-brand"
            />
            {rawError && (
              <p className="mt-1 font-mono text-meta text-semantic-down">{rawError}</p>
            )}
          </div>
        ) : (
          <div className="divide-y divide-border-subtle">
            <Field label="Name">
              <input
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                placeholder="Momentum turn on the daily"
                className="w-full border border-border-subtle bg-bg-base px-2 py-1 text-ui outline-none focus:border-brand"
              />
            </Field>

            <Field label="Interval">
              <div className="flex border border-border-subtle">
                {INTERVALS.map((iv) => (
                  <button
                    key={iv}
                    type="button"
                    onClick={() => setDraft({ ...draft, interval: iv })}
                    className={
                      "border-r border-border-subtle px-2 py-1 font-mono text-meta last:border-r-0 " +
                      (draft.interval === iv
                        ? "bg-brand-muted text-brand"
                        : "text-text-muted hover:text-text-primary")
                    }
                  >
                    {iv}
                  </button>
                ))}
              </div>
            </Field>

            <Field
              label="Symbols"
              hint="Named instruments, watched lists, or both — the rule evaluates the union."
            >
              <SymbolTags
                symbols={draft.symbols}
                onChange={(symbols) => setDraft({ ...draft, symbols })}
              />
              <WatchlistAttach
                selected={draft.watchlist_ids ?? []}
                onChange={(watchlist_ids) => setDraft({ ...draft, watchlist_ids })}
              />
            </Field>

            <Field label="Conditions" hint="All of these must hold on a closed bar.">
              <div className="space-y-1">
                {conditions.map((c, i) => (
                  <ConditionRow
                    key={i}
                    node={c}
                    indicators={(vocab?.indicators ?? []).map((x) => x.name)}
                    onChange={(next) =>
                      setConditions(conditions.map((old, j) => (j === i ? next : old)))
                    }
                    onRemove={() => setConditions(conditions.filter((_, j) => j !== i))}
                  />
                ))}
                <button
                  type="button"
                  onClick={() =>
                    setConditions([...conditions, { indicator: "close", op: ">", value: 0 }])
                  }
                  className="inline-flex h-8 w-fit items-center gap-1.5 rounded-md border border-dashed border-border-focus px-3 text-meta font-medium text-text-secondary transition-colors hover:border-brand hover:text-text-primary"
                >
                  <Plus size={14} /> Add a condition
                </button>
              </div>
            </Field>

            <Field label="Actions">
              <div className="flex flex-wrap gap-4">
                <Toggle
                  checked={draft.notify.telegram}
                  onChange={(v) => setDraft({ ...draft, notify: { ...draft.notify, telegram: v } })}
                  label="Telegram alert"
                />
                <Toggle
                  checked={draft.notify.ai_context}
                  onChange={(v) =>
                    setDraft({ ...draft, notify: { ...draft.notify, ai_context: v } })
                  }
                  label="Attach AI context"
                />
                <Toggle
                  checked={draft.enabled}
                  onChange={(v) => setDraft({ ...draft, enabled: v })}
                  label="Enabled"
                />
              </div>
            </Field>

            <div className="flex flex-wrap items-center gap-2 px-4 py-4">
              <button type="button" onClick={() => save.mutate(draft)} disabled={!draft.name || save.isPending} className="action-primary">
                {save.isPending ? "Saving…" : draft.id ? "Save changes" : "Save algorithm"}
              </button>
              <button type="button" onClick={() => navigate("/algorithms/backtest")} className="action-secondary">
                Backtest this rule
              </button>
              {draft.id && (
                <button type="button" onClick={() => setDraft(BLANK)} className="action-secondary">
                  New rule
                </button>
              )}
              <button type="button" onClick={() => validate.mutate(draft)} disabled={validate.isPending} className="action-secondary">
                {validate.isPending ? "Checking…" : "Check it against today’s data"}
              </button>

              {validate.data && (
                <span className="flex items-center gap-1 font-mono text-meta text-semantic-up">
                  <Check size={11} /> valid · needs {validate.data.required_bars} bars · {validate.data.results.length} symbols
                </span>
              )}
              {(validate.isError || save.isError) && (
                <Problems error={(validate.error ?? save.error) as Error} />
              )}
            </div>
          </div>
        )}
      </Panel>

      <Divider resize={side} orientation="vertical" />

      <Panel
        title="Templates"
        collapseKey="algorithms.templates"
        scroll
        className="shrink-0"
        style={{ width: side.size }}
      >
        <div className="divide-y divide-border-subtle">
          {(templates?.templates ?? []).map((t) => (
            <button
              key={t.key}
              type="button"
              onClick={() => setDraft({ ...BLANK, ...t.algorithm, name: t.title })}
              className="block w-full px-3 py-2 text-left transition-colors hover:bg-bg-panel-hover"
            >
              <span className="block text-ui text-text-primary">{t.title}</span>
              <span className="mt-0.5 block text-meta leading-relaxed text-text-muted">
                {t.description}
              </span>
            </button>
          ))}
        </div>

        <div className="border-t border-border-subtle">
          <h3 className="px-3 py-2 text-meta font-medium text-text-secondary first-letter:uppercase">
            Saved · {existing?.algorithms?.length ?? 0}
          </h3>
          {(existing?.algorithms ?? []).length === 0 ? (
            <Empty icon={Sliders} title="Nothing saved yet." />
          ) : (
            <div className="divide-y divide-border-subtle">
              {(existing?.algorithms ?? []).map((a) => (
                <div
                  key={a.id}
                  className="group flex w-full items-center gap-2 px-3 py-2 transition-colors hover:bg-bg-panel-hover"
                >
                  <button
                    type="button"
                    onClick={() => setDraft(a)}
                    className="min-w-0 flex-1 truncate text-left text-ui text-text-primary"
                  >
                    {a.name}
                  </button>
                  <button type="button" onClick={() => toggle.mutate(a)} title="Enable or disable">
                    <Pill tone={a.enabled ? "up" : "muted"}>{a.enabled ? "on" : "off"}</Pill>
                  </button>
                  <button
                    type="button"
                    onClick={() => drop.mutate(a.id!)}
                    title="Delete"
                    className="opacity-0 transition-opacity group-hover:opacity-100"
                  >
                    <Trash2 size={11} className="text-text-muted hover:text-semantic-down" />
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      </Panel>
      </div>
    </div>
  );
}

/**
 * What the server said was wrong, field by field.
 *
 * The validation endpoints answer an invalid draft with a message per field —
 * "an algorithm needs a name", "list at least one symbol to watch" — and the
 * builder previously showed only the summary line, which tells an operator to
 * go and find a problem the server had already located for them.
 */
function Problems({ error }: { error: Error }) {
  const fields = error instanceof ValidationError ? error.fields : [];
  if (fields.length === 0) {
    return <span className="font-mono text-meta text-semantic-down">{error.message}</span>;
  }
  return (
    <ul className="space-y-0.5">
      {fields.map((f, i) => (
        <li key={i} className="font-mono text-meta text-semantic-down">
          <span className="text-text-muted">{f.field}</span> {f.message}
        </li>
      ))}
    </ul>
  );
}


function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="p-3">
      <div className="mb-1.5 flex items-baseline gap-2">
        <label className="text-meta font-medium text-text-secondary first-letter:uppercase">
          {label}
        </label>
        {hint && <span className="text-meta text-text-muted">{hint}</span>}
      </div>
      {children}
    </div>
  );
}

function ConditionRow({
  node,
  indicators,
  onChange,
  onRemove,
}: {
  node: Node;
  indicators: string[];
  onChange: (n: Node) => void;
  onRemove: () => void;
}) {
  // A condition compares an indicator either with a number or with another
  // indicator. Which one is being edited is explicit rather than inferred, so
  // switching cannot silently leave both set and produce an ambiguous rule.
  const comparing = node.compare != null;

  return (
    <div className="flex items-center gap-1">
      <Select
        value={node.indicator ?? "close"}
        onChange={(v) => onChange({ ...node, indicator: v })}
        options={indicators}
      />
      {node.period != null && (
        <input
          type="number"
          value={node.period}
          onChange={(e) => onChange({ ...node, period: Number(e.target.value) })}
          className="w-14 border border-border-subtle bg-bg-base px-1 py-1 text-center font-mono text-meta outline-none focus:border-brand"
        />
      )}
      <Select
        value={node.op ?? ">"}
        onChange={(v) => onChange({ ...node, op: v as Operator })}
        options={OPERATORS}
      />
      {comparing ? (
        <Select
          value={node.compare?.indicator ?? "sma"}
          onChange={(v) =>
            onChange({ ...node, compare: { ...node.compare, indicator: v } })
          }
          options={indicators}
        />
      ) : (
        <input
          type="number"
          value={node.value ?? 0}
          onChange={(e) => onChange({ ...node, value: Number(e.target.value) })}
          className="w-20 border border-border-subtle bg-bg-base px-1 py-1 text-right font-mono text-meta outline-none focus:border-brand"
        />
      )}
      <button
        type="button"
        title={comparing ? "Compare with a number" : "Compare with an indicator"}
        onClick={() =>
          comparing
            ? onChange({ ...node, compare: undefined, value: 0 })
            : onChange({ ...node, value: undefined, compare: { indicator: "sma", period: 200 } })
        }
        className="border border-border-subtle px-1.5 py-1 font-mono text-meta text-text-muted transition-colors hover:border-brand hover:text-brand"
      >
        {comparing ? "123" : "ƒ"}
      </button>
      <button
        type="button"
        onClick={onRemove}
        className="ml-auto text-text-muted transition-colors hover:text-semantic-down"
        title="Remove"
      >
        <Trash2 size={12} />
      </button>
    </div>
  );
}

function Select({
  value,
  onChange,
  options,
}: {
  value: string;
  onChange: (v: string) => void;
  options: string[];
}) {
  const OPS: Record<string, string> = {
    "<": "below",
    "<=": "at or below",
    ">": "above",
    ">=": "at or above",
    "==": "equal to",
    "!=": "not equal to",
    crosses_above: "crosses above",
    crosses_below: "crosses below",
  };
  return (
    <UiSelect
      label="Choose"
      showLabel={false}
      value={value}
      onChange={onChange}
      options={options.map((o) => ({ value: o, label: OPS[o] ?? o.replace(/_/g, " ") }))}
    />
  );
}

/**
 * Attaching a rule to a list.
 *
 * Resolved when the rule runs rather than copied in now, so an instrument
 * added to the list tomorrow is covered by every rule watching it without
 * anyone editing the rule. That is the whole reason to attach a list instead
 * of pasting its members as symbols.
 */
function WatchlistAttach({
  selected,
  onChange,
}: {
  selected: number[];
  onChange: (ids: number[]) => void;
}) {
  const { data } = useQuery({ queryKey: ["watchlists"], queryFn: api.watchlists });
  const lists = data?.watchlists ?? [];
  if (lists.length === 0) return null;

  const attached = lists.filter((l) => selected.includes(l.id));
  const total = attached.reduce((n, l) => n + l.count, 0);

  return (
    <div className="mt-1.5">
      <div className="flex flex-wrap items-center gap-1">
        {lists.map((l) => {
          const on = selected.includes(l.id);
          return (
            <button
              key={l.id}
              type="button"
              onClick={() =>
                onChange(on ? selected.filter((x) => x !== l.id) : [...selected, l.id])
              }
              className={
                "flex items-center gap-1 border px-1.5 py-0.5 font-mono text-meta transition-colors " +
                (on
                  ? "border-brand/50 bg-brand-muted text-text-primary"
                  : "border-border-subtle text-text-muted hover:text-text-primary")
              }
            >
              <ListChecks size={9} />
              {l.name}
              <span className="opacity-60">{l.count}</span>
            </button>
          );
        })}
      </div>
      {attached.length > 0 && (
        <p className="mt-1 text-meta text-text-muted">
          {total} instrument{total === 1 ? "" : "s"} from{" "}
          {attached.map((l) => l.name).join(", ")}, as of each run.
        </p>
      )}
    </div>
  );
}

function SymbolTags({
  symbols,
  onChange,
}: {
  symbols: string[];
  onChange: (s: string[]) => void;
}) {
  const [text, setText] = useState("");
  return (
    <div className="flex flex-wrap items-center gap-1">
      {symbols.map((s) => (
        <span
          key={s}
          className="flex items-center gap-1 border border-border-focus px-1.5 py-0.5 font-mono text-meta text-text-secondary"
        >
          {s}
          <button
            type="button"
            onClick={() => onChange(symbols.filter((x) => x !== s))}
            className="text-text-muted hover:text-semantic-down"
          >
            <X size={9} />
          </button>
        </span>
      ))}
      <input
        value={text}
        onChange={(e) => setText(e.target.value.toUpperCase())}
        onKeyDown={(e) => {
          if (e.key === "Enter" && text.trim()) {
            e.preventDefault();
            onChange([...new Set([...symbols, text.trim()])]);
            setText("");
          }
        }}
        placeholder="Add a ticker and press Enter…"
        className="h-8 min-w-24 flex-1 rounded-md border border-border-subtle bg-bg-base px-2.5 text-ui outline-none focus:border-brand"
      />
    </div>
  );
}

function Toggle({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
}) {
  return (
    <label className="flex cursor-pointer select-none items-center gap-1.5 text-meta text-text-secondary">
      <span
        onClick={() => onChange(!checked)}
        className={
          "flex h-3.5 w-3.5 items-center justify-center border " +
          (checked ? "border-brand/50 bg-brand-muted text-text-primary" : "border-border-focus")
        }
      >
        {checked && <Check size={9} />}
      </span>
      {label}
    </label>
  );
}
