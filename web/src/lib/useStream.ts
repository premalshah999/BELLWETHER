import { useQueryClient } from "@tanstack/react-query";
import { createContext, useEffect, useRef, useState } from "react";
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
