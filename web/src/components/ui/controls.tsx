import { Check, ChevronDown, Search, X } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";

/**
 * The controls every page shares.
 *
 * One height (32px), one radius, one way of showing "on". A page that builds
 * its own segmented control is a page that will draw it slightly differently,
 * and a dozen slightly different controls is what makes an interface feel
 * assembled rather than designed.
 */

export function Segmented<T extends string | number>({
  options,
  value,
  onChange,
  label,
  size = "md",
}: {
  options: readonly { value: T; label: ReactNode; title?: string }[];
  value: T;
  onChange: (v: T) => void;
  label: string;
  size?: "sm" | "md";
}) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex shrink-0 rounded-md bg-bg-base p-0.5 ring-1 ring-border-subtle">
      {options.map((o) => {
        const on = o.value === value;
        return (
          <button
            key={String(o.value)}
            type="button"
            role="radio"
            aria-checked={on}
            title={o.title}
            onClick={() => onChange(o.value)}
            className={
              "rounded-[5px] font-medium transition-colors " +
              (size === "sm" ? "h-6 px-2 text-meta " : "h-7 px-2.5 text-meta ") +
              (on ? "bg-bg-panel text-text-primary shadow-sm ring-1 ring-border-subtle" : "text-text-muted hover:text-text-primary")
            }
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

export function Select<T extends string>({
  value,
  onChange,
  options,
  label,
  className = "",
}: {
  value: T;
  onChange: (v: T) => void;
  options: readonly { value: T; label: string }[];
  label: string;
  className?: string;
}) {
  return (
    <label className={"relative inline-flex shrink-0 items-center " + className}>
      <span className="sr-only">{label}</span>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value as T)}
        className="h-8 w-full cursor-pointer appearance-none rounded-md border border-border-subtle bg-bg-panel pl-2.5 pr-8 text-meta font-medium text-text-primary outline-none transition-colors hover:border-border-focus focus-visible:border-brand"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <ChevronDown size={14} className="pointer-events-none absolute right-2.5 text-text-muted" />
    </label>
  );
}

export function Switch({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className="inline-flex h-8 shrink-0 items-center gap-2 rounded-md px-1.5 text-meta font-medium text-text-secondary transition-colors hover:text-text-primary"
    >
      <span
        className={
          "relative h-[18px] w-8 rounded-full transition-colors " + (checked ? "bg-brand" : "bg-border-focus")
        }
      >
        <span
          className={
            "absolute left-0 top-[2px] h-[14px] w-[14px] rounded-full bg-white shadow-sm transition-transform duration-150 " +
            (checked ? "translate-x-[16px]" : "translate-x-[2px]")
          }
        />
      </span>
      {label}
    </button>
  );
}

/** Closes on an outside click or Escape. */
export function useDismiss(open: boolean, close: () => void) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) close();
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        close();
      }
    };
    document.addEventListener("mousedown", away);
    document.addEventListener("keydown", esc, true);
    return () => {
      document.removeEventListener("mousedown", away);
      document.removeEventListener("keydown", esc, true);
    };
  }, [open, close]);
  return ref;
}

/**
 * Choose several from many, with search.
 *
 * Replaces walls of toggle chips. Fifty options laid out as buttons are fifty
 * things competing for attention before the reader has seen a single result;
 * the same fifty behind one control cost a line.
 */
export function MultiSelect({
  label,
  options,
  value,
  onChange,
  allLabel,
}: {
  label: string;
  options: readonly { value: string; label: string; hint?: string }[];
  value: string[];
  onChange: (v: string[]) => void;
  allLabel: string;
}) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const ref = useDismiss(open, () => setOpen(false));
  const id = useId();
  const shown = useMemo(() => {
    const t = q.trim().toLowerCase();
    return t ? options.filter((o) => o.label.toLowerCase().includes(t)) : options;
  }, [q, options]);
  const summary =
    value.length === 0
      ? allLabel
      : value.length === 1
        ? options.find((o) => o.value === value[0])?.label ?? value[0]
        : `${value.length} types`;

  return (
    <div ref={ref} className="relative shrink-0">
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((v) => !v)}
        className={
          "inline-flex h-8 items-center gap-1.5 rounded-md border pl-2.5 pr-2 text-meta font-medium transition-colors " +
          (value.length
            ? "border-brand/50 bg-brand-muted text-text-primary"
            : "border-border-subtle bg-bg-panel text-text-primary hover:border-border-focus")
        }
      >
        <span className="text-text-muted">{label}</span>
        <span className="max-w-[12rem] truncate">{summary}</span>
        {value.length > 0 ? (
          <span
            role="button"
            tabIndex={0}
            aria-label={`Clear ${label}`}
            onClick={(e) => {
              e.stopPropagation();
              onChange([]);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                e.stopPropagation();
                onChange([]);
              }
            }}
            className="-mr-0.5 flex h-5 w-5 items-center justify-center rounded text-text-muted hover:bg-bg-panel-hover hover:text-text-primary"
          >
            <X size={12} />
          </span>
        ) : (
          <ChevronDown size={14} className="text-text-muted" />
        )}
      </button>
      {open && (
        <div className="absolute left-0 top-full z-30 mt-1.5 w-72 overflow-hidden rounded-lg border border-border-subtle bg-bg-raised shadow-pop [animation:rise-in_140ms_var(--ease-out)]">
          <div className="relative border-b border-border-subtle">
            <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" />
            <input
              autoFocus
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder={`Find a ${label.toLowerCase()}…`}
              className="h-10 w-full bg-transparent pl-9 pr-3 text-ui outline-none"
            />
          </div>
          <ul id={id} role="listbox" aria-multiselectable className="max-h-72 overflow-y-auto overscroll-contain p-1">
            {shown.map((o) => {
              const on = value.includes(o.value);
              return (
                <li key={o.value}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={on}
                    onClick={() => onChange(on ? value.filter((v) => v !== o.value) : [...value, o.value])}
                    className="flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left text-ui text-text-secondary hover:bg-bg-panel-hover hover:text-text-primary"
                  >
                    <span
                      className={
                        "flex h-4 w-4 shrink-0 items-center justify-center rounded-[4px] border " +
                        (on ? "border-brand bg-brand text-brand-ink" : "border-border-focus")
                      }
                    >
                      {on && <Check size={11} strokeWidth={3} />}
                    </span>
                    <span className="min-w-0 flex-1 truncate">{o.label}</span>
                    {o.hint && <span className="shrink-0 text-micro text-text-muted">{o.hint}</span>}
                  </button>
                </li>
              );
            })}
            {shown.length === 0 && <li className="px-3 py-3 text-meta text-text-muted">No match for “{q}”.</li>}
          </ul>
          {value.length > 0 && (
            <div className="flex items-center justify-between border-t border-border-subtle px-3 py-2">
              <span className="text-meta text-text-muted">{value.length} selected</span>
              <button type="button" onClick={() => onChange([])} className="text-meta font-medium text-brand hover:underline">
                Clear
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * A page's title and what it is showing.
 *
 * Every page opens the same way — a name, a sentence of context, and its
 * actions on the right — so the reader always knows where they are before
 * reading anything else.
 */
export function PageHeader({
  title,
  subtitle,
  actions,
  children,
}: {
  title: string;
  subtitle?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <header className="shrink-0 border-b border-border-subtle bg-bg-panel">
      <div className="flex flex-wrap items-end gap-x-6 gap-y-3 px-5 pb-3 pt-5 md:px-6">
        <div className="min-w-0 flex-1">
          <h1 className="font-reading text-title font-semibold text-text-primary max-md:hidden">{title}</h1>
          {subtitle && <p className="mt-1 text-ui text-text-secondary">{subtitle}</p>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2 max-sm:w-full">{actions}</div>}
      </div>
      {children && (
        <div className="flex items-center gap-2 overflow-x-auto px-5 pb-3 md:flex-wrap md:overflow-visible md:px-6">{children}</div>
      )}
    </header>
  );
}

/**
 * A panel that slides in over the right of the page, holding one thing in
 * detail while the list it came from stays in place behind it.
 */
export function Drawer({
  open,
  onClose,
  label,
  children,
  width = 520,
}: {
  open: boolean;
  onClose: () => void;
  label: string;
  children: ReactNode;
  width?: number;
}) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", esc);
    panel.current?.focus();
    return () => document.removeEventListener("keydown", esc);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-40 flex justify-end" role="dialog" aria-modal="true" aria-label={label}>
      <button
        type="button"
        aria-label="Close"
        tabIndex={-1}
        onClick={onClose}
        className="absolute inset-0 bg-[var(--scrim)] [animation:fade-in_160ms_ease]"
      />
      <div
        ref={panel}
        tabIndex={-1}
        style={{ width: `min(${width}px, 100vw)` }}
        className="relative flex h-full flex-col overflow-hidden border-l border-border-subtle bg-bg-raised shadow-pop outline-none [animation:sheet-in_220ms_var(--ease-out)]"
      >
        {children}
      </div>
    </div>
  );
}

/** Rows of placeholder in the shape of the rows to come. */
export function SkeletonRows({ count = 8, height = 56 }: { count?: number; height?: number }) {
  return (
    <div className="flex flex-col gap-2 px-5 py-4 md:px-6" aria-busy="true" aria-label="Loading">
      {Array.from({ length: count }, (_, i) => (
        <span key={i} className="skeleton w-full" style={{ height, opacity: 1 - i * 0.08 }} />
      ))}
    </div>
  );
}
