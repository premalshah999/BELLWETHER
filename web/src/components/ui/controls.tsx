import { Check, ChevronDown, Search, X } from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";

/**
 * The controls every page shares, in one place so they look and behave the
 * same everywhere: one 34px height, one radius, one way of showing "on".
 */

const FIELD =
  "rounded-md border border-border-subtle bg-bg-field text-text-primary transition-[border-color,box-shadow] hover:border-border-focus";
const OPEN_RING = "border-brand shadow-[0_0_0_3px_var(--brass-soft)]";

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
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex shrink-0 rounded-[9px] border border-border-subtle bg-bg-field p-[3px]"
    >
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
              "rounded-md transition-colors " +
              (size === "sm" ? "h-6 px-2.5 text-meta " : "h-7 px-3 text-meta ") +
              (on
                ? "bg-bg-chip font-semibold text-text-primary shadow-[0_1px_2px_rgba(0,0,0,0.35)]"
                : "font-medium text-text-muted hover:text-text-primary")
            }
          >
            {o.label}
          </button>
        );
      })}
    </div>
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

function Menu({ id, children, width, align = "left" }: { id: string; children: ReactNode; width?: number; align?: "left" | "right" }) {
  return (
    <div
      id={id}
      style={{ minWidth: width ?? 220 }}
      className={
        "absolute top-full z-40 mt-1.5 overflow-hidden rounded-[11px] border border-border-subtle bg-bg-raised p-1.5 shadow-pop [animation:rise-in_130ms_var(--ease-out)] " +
        (align === "right" ? "right-0" : "left-0")
      }
    >
      {children}
    </div>
  );
}

/**
 * Choose one of several.
 *
 * Drawn by the app rather than the browser: the native menu cannot be themed
 * and opened as a white system list over a dark page. Keyboard works the way
 * a native select does -- arrows move, Enter picks, Escape closes, typing a
 * letter jumps to the next option starting with it.
 */
export function Select<T extends string>({
  value,
  onChange,
  options,
  label,
  showLabel = true,
  className = "",
  align,
}: {
  value: T;
  onChange: (v: T) => void;
  options: readonly { value: T; label: string; hint?: string }[];
  label: string;
  showLabel?: boolean;
  className?: string;
  align?: "left" | "right";
}) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const close = useCallback(() => setOpen(false), []);
  const ref = useDismiss(open, close);
  const btn = useRef<HTMLButtonElement>(null);
  const id = useId();
  const current = options.find((o) => o.value === value);

  useEffect(() => {
    if (open) setActive(Math.max(0, options.findIndex((o) => o.value === value)));
  }, [open, options, value]);

  const pick = (i: number) => {
    const o = options[i];
    if (o) onChange(o.value);
    setOpen(false);
    btn.current?.focus();
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (!open && (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      setOpen(true);
      return;
    }
    if (!open) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((a) => Math.min(options.length - 1, a + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((a) => Math.max(0, a - 1));
    } else if (e.key === "Home") {
      e.preventDefault();
      setActive(0);
    } else if (e.key === "End") {
      e.preventDefault();
      setActive(options.length - 1);
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      pick(active);
    } else if (e.key === "Tab") {
      setOpen(false);
    } else if (e.key.length === 1 && /\S/.test(e.key)) {
      const k = e.key.toLowerCase();
      const from = (active + 1) % options.length;
      const order = [...options.slice(from), ...options.slice(0, from)];
      const hit = order.find((o) => o.label.toLowerCase().startsWith(k));
      if (hit) setActive(options.indexOf(hit));
    }
  };

  return (
    <div ref={ref} className={"relative shrink-0 " + className}>
      <button
        ref={btn}
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={id}
        aria-label={`${label}: ${current?.label ?? ""}`}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={onKey}
        className={
          "inline-flex h-[34px] w-full items-center gap-2 pl-3 pr-2.5 text-left text-meta " +
          FIELD +
          " " +
          (open ? OPEN_RING : "")
        }
      >
        {showLabel && <span className="shrink-0 text-text-muted">{label}</span>}
        <span className="min-w-0 flex-1 truncate font-medium">{current?.label ?? "Choose…"}</span>
        <ChevronDown size={14} className={"shrink-0 text-text-muted transition-transform " + (open ? "rotate-180" : "")} />
      </button>
      {open && (
        <Menu id={id} align={align}>
          <ul role="listbox" aria-label={label} className="max-h-72 overflow-y-auto overscroll-contain" onKeyDown={onKey}>
            {options.map((o, i) => {
              const sel = o.value === value;
              return (
                <li
                  key={o.value}
                  role="option"
                  aria-selected={sel}
                  onMouseEnter={() => setActive(i)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => pick(i)}
                  className={
                    "flex cursor-pointer items-center gap-2.5 rounded-md px-2.5 py-2 text-meta " +
                    (i === active ? "bg-bg-panel-hover text-text-primary" : "text-text-secondary")
                  }
                >
                  <span className="min-w-0 flex-1 truncate">{o.label}</span>
                  {o.hint && <span className="shrink-0 font-num text-micro text-text-muted">{o.hint}</span>}
                  <Check size={14} className={"shrink-0 text-brand " + (sel ? "opacity-100" : "opacity-0")} />
                </li>
              );
            })}
          </ul>
        </Menu>
      )}
    </div>
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
      className={
        "inline-flex h-[34px] shrink-0 items-center gap-2.5 rounded-full border px-3 text-meta font-medium transition-colors " +
        (checked
          ? "border-brand/40 bg-brand-muted text-text-primary"
          : "border-border-subtle text-text-secondary hover:border-border-focus hover:text-text-primary")
      }
    >
      <span className={"relative h-[18px] w-[30px] rounded-full transition-colors " + (checked ? "bg-brand" : "bg-border-focus")}>
        <span
          className={
            "absolute left-0 top-[2px] h-[14px] w-[14px] rounded-full bg-white shadow-sm transition-transform duration-150 " +
            (checked ? "translate-x-[14px]" : "translate-x-[2px]")
          }
        />
      </span>
      {label}
    </button>
  );
}

/**
 * Choose several from many, with search. Replaces walls of toggle chips.
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
  const close = useCallback(() => setOpen(false), []);
  const ref = useDismiss(open, close);
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
        : `${value.length} selected`;

  return (
    <div ref={ref} className="relative shrink-0">
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((v) => !v)}
        className={
          "inline-flex h-[34px] items-center gap-2 pl-3 pr-2.5 text-meta " +
          FIELD +
          " " +
          (open ? OPEN_RING : value.length ? "border-brand/40" : "")
        }
      >
        <span className="text-text-muted">{label}</span>
        <span className="max-w-[12rem] truncate font-medium">{summary}</span>
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
            className="-mr-1 flex h-5 w-5 items-center justify-center rounded text-text-muted hover:bg-bg-panel-hover hover:text-text-primary"
          >
            <X size={12} />
          </span>
        ) : (
          <ChevronDown size={14} className={"text-text-muted transition-transform " + (open ? "rotate-180" : "")} />
        )}
      </button>
      {open && (
        <Menu id={id} width={264}>
          <div className="relative mb-1.5">
            <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-text-muted" />
            <input
              autoFocus
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder={`Find a ${label.toLowerCase()}…`}
              className="h-8 w-full rounded-md border border-border-subtle bg-bg-field pl-8 pr-2.5 text-meta outline-none focus:border-brand"
            />
          </div>
          <ul role="listbox" aria-multiselectable className="max-h-72 overflow-y-auto overscroll-contain">
            {shown.map((o) => {
              const on = value.includes(o.value);
              return (
                <li key={o.value}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={on}
                    onClick={() => onChange(on ? value.filter((v) => v !== o.value) : [...value, o.value])}
                    className="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-meta text-text-secondary hover:bg-bg-panel-hover hover:text-text-primary"
                  >
                    <span
                      className={
                        "flex h-4 w-4 shrink-0 items-center justify-center rounded-[5px] border " +
                        (on ? "border-brand bg-brand text-brand-ink" : "border-border-focus")
                      }
                    >
                      {on && <Check size={11} strokeWidth={3} />}
                    </span>
                    <span className="min-w-0 flex-1 truncate">{o.label}</span>
                    {o.hint && <span className="shrink-0 font-num text-micro text-text-muted">{o.hint}</span>}
                  </button>
                </li>
              );
            })}
            {shown.length === 0 && <li className="px-3 py-3 text-meta text-text-muted">No match for “{q}”.</li>}
          </ul>
          {value.length > 0 && (
            <div className="mt-1.5 flex items-center justify-between border-t border-border-subtle px-2.5 pt-2">
              <span className="text-meta text-text-muted">{value.length} selected</span>
              <button type="button" onClick={() => onChange([])} className="text-meta font-medium text-accent-text hover:underline">
                Clear
              </button>
            </div>
          )}
        </Menu>
      )}
    </div>
  );
}

/**
 * A page's title and what it is showing, then its filters. Every page opens
 * the same way, so the reader always knows where they are.
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
    <header className="shrink-0">
      <div className="flex flex-wrap items-end gap-x-6 gap-y-3 px-5 pb-4 pt-6 md:px-8 md:pt-7">
        <div className="min-w-0 flex-1">
          <h1 className="text-[26px] font-semibold leading-tight tracking-[-0.02em] text-text-primary max-md:hidden">{title}</h1>
          {subtitle && <p className="mt-1 text-ui text-text-secondary">{subtitle}</p>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2 max-sm:w-full">{actions}</div>}
      </div>
      {children && (
        <div className="flex items-center gap-2 overflow-x-auto px-5 pb-4 md:flex-wrap md:overflow-visible md:px-8">{children}</div>
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
  width = 540,
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
        className="absolute inset-0 bg-[var(--scrim)] backdrop-blur-[2px] [animation:fade-in_160ms_ease]"
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

/** The frame lists and tables sit in: a card inset from the page edge. */
export function Sheet({ children, className = "" }: { children: ReactNode; className?: string }) {
  return (
    <div className={"mx-5 mb-8 overflow-clip rounded-xl border border-border-subtle bg-bg-card md:mx-8 " + className}>{children}</div>
  );
}

/** Rows of placeholder in the shape of the rows to come. */
export function SkeletonRows({ count = 8, height = 56 }: { count?: number; height?: number }) {
  return (
    <div className="flex flex-col gap-2 px-5 py-4 md:px-8" aria-busy="true" aria-label="Loading">
      {Array.from({ length: count }, (_, i) => (
        <span key={i} className="skeleton w-full" style={{ height, opacity: 1 - i * 0.08 }} />
      ))}
    </div>
  );
}
