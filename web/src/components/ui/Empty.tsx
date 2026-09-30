import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

/**
 * What an empty screen says: what would fill it, and how to get there.
 *
 * An empty page is an invitation to act, so it names the next step rather
 * than apologising for having nothing to show.
 */
export function Empty({
  icon: Icon,
  title,
  hint,
  action,
}: {
  icon: LucideIcon;
  title: string;
  hint?: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex max-w-lg flex-col items-start gap-3 px-6 py-12">
      <Icon size={22} className="text-text-muted" />
      <p className="font-reading text-display text-text-primary">{title}</p>
      {hint && <p className="text-ui leading-relaxed text-text-secondary">{hint.replace(/ -- /g, " — ")}</p>}
      {action && <div className="mt-1">{action}</div>}
    </div>
  );
}
