import { useEffect, useState } from "react";
import { humanMinutes, sessionState, type Venue } from "../../lib/session";

/**
 * Whether anything can move right now.
 *
 * This exists because the interface used to claim "LIVE" at every hour of the
 * day. Outside market hours nothing updates — correctly, since nothing is
 * trading — but with no indication of why, a closed market and a dead feed
 * look identical, and the honest reading of a frozen screen is that the
 * application is broken.
 */
export function MarketStatus() {
  const [, tick] = useState(0);

  // A minute is the right resolution: the countdown is read, not raced.
  useEffect(() => {
    const t = window.setInterval(() => tick((n) => n + 1), 30_000);
    return () => window.clearInterval(t);
  }, []);

  return (
    <div className="grid grid-cols-2 divide-x divide-border-subtle border-b border-border-subtle">
      {(["NSE", "US"] as Venue[]).map((venue) => (
        <VenueRow key={venue} venue={venue} />
      ))}
    </div>
  );
}

function VenueRow({ venue }: { venue: Venue }) {
  const s = sessionState(venue);
  return (
    <div className="px-3.5 py-2.5">
      <div className="flex items-center gap-1.5">
        <span
          className={
            "h-1.5 w-1.5 shrink-0 " +
            (s.open ? "bg-semantic-up shadow-[0_0_6px_rgba(74,222,128,0.6)]" : "bg-text-muted")
          }
        />
        <span className="font-mono text-meta text-text-primary">{venue}</span>
        <span className="ml-auto font-mono text-meta text-text-muted">{s.localTime}</span>
      </div>
      <p className="mt-1 font-mono text-meta text-text-secondary">
        {s.open ? (
          <>
            open <span className="text-text-muted">·</span>{" "}
            <span className="text-semantic-up">{humanMinutes(s.closesInMinutes ?? 0)}</span> left
          </>
        ) : (
          <>
            closed <span className="text-text-muted">·</span> opens{" "}
            <span className="text-text-primary">{humanMinutes(s.opensInMinutes ?? 0)}</span>
          </>
        )}
      </p>
    </div>
  );
}
