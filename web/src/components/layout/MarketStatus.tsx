import { useEffect, useState } from "react";
import { humanMinutes, sessionState } from "../../lib/session";

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

  const s = sessionState();
  return (
    <div className="flex items-center gap-2.5 px-4 py-3">
      <span className={"h-2 w-2 shrink-0 rounded-full " + (s.open ? "bg-semantic-up" : "bg-border-focus")} />
      <p className="min-w-0 flex-1 text-meta text-text-secondary">
        {s.open ? (
          <>
            US market open, <span className="font-medium text-text-primary">{humanMinutes(s.closesInMinutes ?? 0)}</span> to the close
          </>
        ) : (
          <>
            US market closed, opens in <span className="font-medium text-text-primary">{humanMinutes(s.opensInMinutes ?? 0)}</span>
          </>
        )}
      </p>
      <span className="shrink-0 text-meta text-text-muted">{s.localTime} ET</span>
    </div>
  );
}
