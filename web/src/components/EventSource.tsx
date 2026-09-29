import type { MarketEvent } from "../lib/api";

/**
 * Who reported an event, and how many others did.
 *
 * Every feed row showed a headline, a time and a ticker, and never who said
 * it -- so an SEC filing and a content-farm rewrite looked the same. The
 * source is the provenance the rest of this app is built around, and a reader
 * deciding whether to believe a headline needs it before anything else. The
 * count matters for the same reason: five independent outlets reporting it is
 * a different claim from one.
 */
export function EventSource({ event }: { event: Pick<MarketEvent, "source" | "source_count"> }) {
  const others = Math.max(0, (event.source_count ?? 1) - 1);
  if (!event.source && others === 0) return null;
  return (
    <span className="font-mono text-meta text-text-secondary">
      {event.source || "unknown source"}
      {others > 0 && (
        <span className="text-text-muted" title={`${others + 1} independent sources reported this`}>
          {" "}+{others}
        </span>
      )}
    </span>
  );
}
