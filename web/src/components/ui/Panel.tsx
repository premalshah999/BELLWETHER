import { ChevronDown, ChevronRight } from "lucide-react";
import type { CSSProperties, ReactNode } from "react";
import { usePersisted } from "../../lib/layout";

/**
 * The only container.
 *
 * Panels never nest and never carry a shadow or a radius. They are separated
 * from their neighbours by a single 1px line, which is why the border lives on
 * the parent's `divide-*` rather than on each panel: two adjacent panels each
 * drawing their own border produce a 2px seam, and a screen made of those
 * reads as a stack of boxes rather than one instrument.
 *
 * Given a `collapseKey`, the header becomes a toggle and the choice is
 * remembered. Collapsing leaves the header in place rather than hiding the
 * panel outright — a pane you cannot see is a pane you cannot get back.
 */
export function Panel({
  title,
  action,
  children,
  className = "",
  style,
  scroll = false,
  collapseKey,
}: {
  title?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  /** Explicit geometry, for panels a divider resizes. */
  style?: CSSProperties;
  /** Makes the body the scroll region rather than the page. */
  scroll?: boolean;
  /** Enables collapsing, and names where the choice is stored. */
  collapseKey?: string;
}) {
  const [collapsed, setCollapsed] = usePersisted(`panel.${collapseKey ?? "none"}`, false);
  const collapsible = collapseKey != null;
  const shut = collapsible && collapsed;

  return (
    <section
      style={style}
      className={
        "flex min-h-0 flex-col bg-bg-panel " + (shut ? "shrink-0 grow-0 " : "") + className
      }
    >
      {title !== undefined && (
        <header
          className={
            "flex h-11 shrink-0 items-center justify-between gap-3 border-b border-border-subtle px-4 " +
            (collapsible ? "cursor-pointer select-none hover:bg-bg-panel-hover" : "")
          }
          onClick={collapsible ? () => setCollapsed((v) => !v) : undefined}
        >
          <h2 className="flex min-w-0 items-center gap-1.5 text-ui font-semibold text-text-primary">
            {collapsible &&
              (shut ? (
                <ChevronRight size={11} className="shrink-0" />
              ) : (
                <ChevronDown size={11} className="shrink-0" />
              ))}
            <span className="truncate">{title}</span>
          </h2>
          {/* Actions must not toggle the panel when clicked. */}
          <div onClick={(e) => e.stopPropagation()} className="shrink-0">
            {action}
          </div>
        </header>
      )}
      {!shut && (
        <div className={scroll ? "min-h-0 flex-1 overflow-y-auto" : "min-h-0 flex-1"}>
          {children}
        </div>
      )}
    </section>
  );
}
