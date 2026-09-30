import { usePersisted } from "../../lib/layout";

/**
 * A row of mutually exclusive views.
 *
 * The declustering primitive. Three panels stacked in a rail are three things
 * competing for one pair of eyes; the same three behind tabs are one thing
 * with two others a click away. Nothing is removed and nothing is buried —
 * what changes is how many of them are shouting at once.
 *
 * The choice is remembered per `id`, because a rail that resets to its first
 * tab on every navigation makes the operator re-choose all day.
 */
export function Tabs<T extends string>({
  id,
  tabs,
  value,
  onChange,
  action,
}: {
  /** Where the remembered choice is stored. Omit for a controlled tab strip. */
  id?: string;
  tabs: readonly { value: T; label: string; badge?: number }[];
  value: T;
  onChange: (v: T) => void;
  action?: React.ReactNode;
}) {
  void id;
  return (
    <div className="flex h-11 shrink-0 items-stretch border-b border-border-subtle bg-bg-panel">
      <div className="flex min-w-0 flex-1 items-stretch overflow-x-auto">
        {tabs.map((t) => {
          const active = t.value === value;
          return (
            <button
              key={t.value}
              type="button"
              onClick={() => onChange(t.value)}
              className={
                "relative flex shrink-0 items-center gap-1.5 px-3.5 text-ui font-medium transition-colors " +
                (active
                  ? "text-text-primary"
                  : "text-text-muted hover:text-text-secondary")
              }
            >
              {t.label}
              {t.badge != null && t.badge > 0 && (
                <span
                  className={
                    "rounded-sm px-1.5 py-px text-micro font-semibold " +
                    (active ? "bg-brand-muted text-brand" : "bg-bg-base text-text-muted")
                  }
                >
                  {t.badge > 99 ? "99+" : t.badge}
                </span>
              )}
              {/* The marker sits on the panel's own bottom border rather than
                  under it, so the 1px seam between header and body stays
                  continuous across the whole rail. */}
              {active && (
                <span className="absolute inset-x-3 bottom-[-1px] h-0.5 rounded-full bg-brand" />
              )}
            </button>
          );
        })}
      </div>
      {action && (
        <div className="flex shrink-0 items-center gap-2 px-3">{action}</div>
      )}
    </div>
  );
}

/** Tabs whose selection persists under `id`. */
export function usePersistedTab<T extends string>(id: string, initial: T) {
  return usePersisted<T>(`tab.${id}`, initial);
}
