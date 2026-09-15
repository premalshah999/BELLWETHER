import type { useResize } from "../../lib/layout";

type Resize = ReturnType<typeof useResize>;

/**
 * The draggable seam between two panes.
 *
 * It *is* the 1px border, not something beside it: the element is one pixel
 * wide and grows its hit area with a transparent overlay, so the layout keeps
 * its continuous hairline and still offers a comfortable target. Widening the
 * visible line on hover would make every divider announce itself and turn a
 * quiet interface into a set of controls.
 */
export function Divider({
  resize,
  orientation,
  title,
}: {
  resize: Resize;
  orientation: "vertical" | "horizontal";
  title?: string;
}) {
  const vertical = orientation === "vertical";
  return (
    <div
      role="separator"
      aria-orientation={orientation}
      title={title ?? "Drag to resize · double-click to reset"}
      onDoubleClick={resize.reset}
      {...resize.handlers}
      className={
        "group relative z-10 shrink-0 touch-none " +
        (vertical ? "w-px cursor-col-resize" : "h-px cursor-row-resize") +
        (resize.dragging ? " bg-brand" : " bg-border-subtle hover:bg-border-focus")
      }
    >
      {/* The hit area. Transparent and larger than the line, so the seam stays
          one pixel while the target is eight. */}
      <span
        className={
          "absolute " +
          (vertical ? "-left-0.5 -right-0.5 top-0 bottom-0" : "-top-0.5 -bottom-0.5 left-0 right-0")
        }
      />
    </div>
  );
}
