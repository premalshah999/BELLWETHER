import type { ReactNode } from "react";

type Tone = "neutral" | "brand" | "up" | "down" | "muted";

const tones: Record<Tone, string> = {
  neutral: "border-border-focus text-text-secondary",
  brand: "border-brand/40 bg-brand-muted text-brand",
  up: "border-semantic-up/40 text-semantic-up",
  down: "border-semantic-down/40 text-semantic-down",
  muted: "border-border-subtle text-text-muted",
};

/** A label with a 1px border and no fill, except where meaning demands one. */
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
      className={`inline-flex shrink-0 items-center gap-1 border px-1.5 py-px font-mono text-micro uppercase leading-4 tracking-wider ${tones[tone]}`}
    >
      {children}
    </span>
  );
}
