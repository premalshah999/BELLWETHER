import { useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useRef, useState } from "react";
import { MarketStream, type StreamState, type StreamTopic } from "./stream";

/**
 * Opens one stream for the whole application.
 *
 * One connection, not one per component: browsers cap concurrent connections
 * per origin, and a component-scoped stream would spend that budget on
 * duplicates of the same data.
 */
export function useMarketStream(topics: StreamTopic[] = ["quotes", "events", "scanner"]) {
  const qc = useQueryClient();
  const [state, setState] = useState<StreamState>({
    connected: false,
    attempts: 0,
    missed: 0,
  });
  const ref = useRef<MarketStream>();
  const key = topics.join(",");

  useEffect(() => {
    const s = new MarketStream(qc, key.split(",") as StreamTopic[]);
    ref.current = s;
    const off = s.subscribe(setState);
    s.start();
    return () => {
      off();
      s.close();
      ref.current = undefined;
    };
  }, [qc, key]);

  return state;
}

/**
 * The application's stream state, so any page can decide how hard to poll
 * without opening a connection of its own.
 */
export const StreamContext = createContext<StreamState>({
  connected: false,
  attempts: 0,
  missed: 0,
});

/** Polling interval for a query, given whether the stream is carrying it. */
export function useStreamPoll(whenPolling: number): number {
  const { connected } = useContext(StreamContext);
  return pollInterval(connected, whenPolling);
}

/**
 * How often a query should poll, given the stream's state.
 *
 * The fallback is the point. When the stream is connected the data arrives by
 * push and polling is redundant, so it slows to a long reconciliation
 * interval rather than stopping — a stream can be connected and silently
 * behind, and an occasional fetch is what catches that. When the stream is
 * down, the original interval returns and the application behaves exactly as
 * it did before any of this existed.
 */
export function pollInterval(connected: boolean, whenPolling: number): number {
  return connected ? Math.max(whenPolling * 5, 120_000) : whenPolling;
}
