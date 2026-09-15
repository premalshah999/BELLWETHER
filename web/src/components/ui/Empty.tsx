import type { LucideIcon } from "lucide-react";

/** Left-aligned and quiet. A centred glyph over centred prose is the house
 *  style of generated UI; this is a terminal telling you a pane is empty. */
export function Empty({
  icon: Icon,
  title,
  hint,
}: {
  icon: LucideIcon;
  title: string;
  hint?: string;
}) {
  return (
    <div className="flex items-start gap-2 px-3 py-6 text-text-muted">
      <Icon size={13} className="mt-px shrink-0" />
      <div className="max-w-[46ch]">
        <p className="text-ui text-text-secondary">{title}</p>
        {hint && <p className="mt-0.5 text-meta leading-relaxed">{hint}</p>}
      </div>
    </div>
  );
}
