import { PanelLeftClose, PanelLeftOpen, PanelRightClose, PanelRightOpen } from "lucide-react";

/**
 * The collapsed state of a rail.
 *
 * A hidden rail leaves a 24px strip carrying the reopen control and the rail's
 * name written vertically. Hiding it completely would save twenty more pixels
 * and cost the ability to get it back without hunting through a menu.
 */
export function CollapsedRail({
  side,
  label,
  onOpen,
}: {
  side: "left" | "right";
  label: string;
  onOpen: () => void;
}) {
  const Icon = side === "left" ? PanelLeftOpen : PanelRightOpen;
  return (
    <button
      type="button"
      onClick={onOpen}
      title={`Show ${label.toLowerCase()}`}
      className={
        "flex w-6 shrink-0 flex-col items-center gap-3 bg-bg-panel py-2.5 text-text-muted transition-colors hover:bg-bg-panel-hover hover:text-brand " +
        (side === "left" ? "border-r border-border-subtle" : "border-l border-border-subtle")
      }
    >
      <Icon size={13} />
      <span
        className="font-mono text-micro"
        style={{ writingMode: "vertical-rl" }}
      >
        {label}
      </span>
    </button>
  );
}

/** The control that collapses an open rail, for its header. */
export function RailCloseButton({ side, onClose }: { side: "left" | "right"; onClose: () => void }) {
  const Icon = side === "left" ? PanelLeftClose : PanelRightClose;
  return (
    <button
      type="button"
      onClick={onClose}
      title="Hide this panel"
      className="text-text-muted transition-colors hover:text-brand"
    >
      <Icon size={13} />
    </button>
  );
}
