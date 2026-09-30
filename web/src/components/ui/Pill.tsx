import type { ReactNode } from "react";

type Tone = "neutral" | "brand" | "up" | "down" | "muted" | "info";

const tones: Record<Tone, string> = {
  neutral: "bg-bg-panel-hover text-text-secondary",
  brand: "bg-brand-muted text-brand",
  up: "bg-semantic-up-soft text-semantic-up",
  down: "bg-semantic-down-soft text-semantic-down",
  muted: "text-text-muted",
  info: "bg-info-soft text-info",
};

/**
 * A short label with a soft tint and no border. Sentence case: a label is
 * read, and capitals make every one of them shout at the same volume.
 */
export function Pill({
  children,
  tone = "neutral",
  title,
}: {
  children: ReactNode;
  tone?: Tone;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={`inline-flex shrink-0 items-center gap-1 rounded-sm px-1.5 py-px text-micro font-medium leading-4 ${tones[tone]}`}
    >
      {children}
    </span>
  );
}
