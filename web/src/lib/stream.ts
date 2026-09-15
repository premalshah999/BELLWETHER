import type { QueryClient } from "@tanstack/react-query";

/**
 * The live connection to the server.
 *
 * Replaces a dozen independent polling timers with one connection that pushes.
 * The important properties are not about speed:
 *
 *  - It degrades to polling rather than to nothing. If the stream cannot be
 *    established the existing queries keep their intervals and the app works
 *    exactly as it did before, which is why `connected` is exposed.
 *  - It notices gaps. Each message carries a per-topic sequence; a jump means
 *    something was missed while disconnected, and the affected queries are
 *    refetched rather than the UI carrying on with a hole in it.
 *  - It never trusts the socket as the source of truth. Updates are written
 *    into the query cache, so a reconnect or a manual refresh reconciles
 *    against the server rather than against whatever the stream happened to
 *    deliver.
 */

export type StreamTopic = "quotes" | "events" | "scanner" | "health";

export interface QuoteUpdate {
  symbol: string;
  price: number;
  change: number;
  change_percent: number;
  volume?: number;
  day_high?: number;
  day_low?: number;
  at: string;
  source: string;
  stale?: boolean;
}

export interface StreamState {
  connected: boolean;
  /** Reconnection attempts since the last successful connection. */
  attempts: number;
  lastMessageAt?: number;
  /** Messages the client can tell it missed, from sequence gaps. */
  missed: number;
}

type Listener = (s: StreamState) => void;

export class MarketStream {
  private es?: EventSource;
  private readonly topics: StreamTopic[];
  private readonly qc: QueryClient;
  private seq = new Map<StreamTopic, number>();
  private listeners = new Set<Listener>();
  private state: StreamState = { connected: false, attempts: 0, missed: 0 };
  private closed = false;
  private retryTimer?: number;

  constructor(qc: QueryClient, topics: StreamTopic[]) {
    this.qc = qc;
    this.topics = topics;
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    fn(this.state);
    return () => this.listeners.delete(fn);
  }

  start() {
    if (this.closed || this.es) return;
    // EventSource reconnects on its own, but only for transport-level drops.
    // An HTTP error — the server restarting, a 503 while a dependency is
    // down — closes it permanently, so the retry below covers that case.
    const url = `/api/stream?topics=${this.topics.join(",")}`;
    const es = new EventSource(url);
    this.es = es;

    es.onopen = () => {
      this.set({ connected: true, attempts: 0 });
    };

    es.onerror = () => {
      // Fires both for a recoverable blip and a permanent close. Only the
      // latter needs handling; EventSource is already retrying the former.
      if (es.readyState === EventSource.CLOSED) {
        this.set({ connected: false });
        this.scheduleRetry();
      } else {
        this.set({ connected: false });
      }
    };

    for (const topic of this.topics) {
      es.addEventListener(topic, (ev) => this.onMessage(topic, ev as MessageEvent));
    }
  }

  close() {
    this.closed = true;
    if (this.retryTimer) window.clearTimeout(this.retryTimer);
    this.es?.close();
    this.es = undefined;
    this.set({ connected: false });
  }

  private scheduleRetry() {
    if (this.closed || this.retryTimer) return;
    const attempts = this.state.attempts + 1;
    // Capped exponential backoff with jitter, so a server restart does not
    // bring every open tab back at the same instant.
    const base = Math.min(30_000, 1000 * 2 ** Math.min(attempts, 5));
    const delay = base / 2 + Math.random() * (base / 2);
    this.set({ attempts });
    this.retryTimer = window.setTimeout(() => {
      this.retryTimer = undefined;
      this.es = undefined;
      this.start();
    }, delay);
  }

  private onMessage(topic: StreamTopic, ev: MessageEvent) {
    const id = Number(ev.lastEventId);
    if (Number.isFinite(id)) {
      const prev = this.seq.get(topic);
      if (prev !== undefined && id > prev + 1) {
        // A gap means messages were published while we were not listening.
        // The stream cannot replay them, so the queries that depend on this
        // topic are refetched: correctness comes from the server, not from
        // assuming the stream saw everything.
        this.state.missed += id - prev - 1;
        this.refetchFor(topic);
      }
      this.seq.set(topic, id);
    }
    this.set({ lastMessageAt: Date.now() });

    let data: unknown;
    try {
      data = JSON.parse(ev.data);
    } catch {
      return;
    }
    this.apply(topic, data);
  }

  private apply(topic: StreamTopic, data: unknown) {
    switch (topic) {
      case "quotes": {
        const q = data as QuoteUpdate;
        if (!q?.symbol) return;
        // Written straight into the cache the existing components already
        // read, so nothing has to know whether a price arrived by stream or
        // by poll.
        this.qc.setQueryData(["quote", q.symbol], (prev: unknown) => ({
          ...(prev as object | undefined),
          quote: {
            price: q.price,
            change: q.change,
            change_percent: q.change_percent,
            volume: q.volume,
            day_high: q.day_high,
            day_low: q.day_low,
            as_of: q.at,
          },
          source: q.source,
          fetched_at: q.at,
          stale: q.stale ?? false,
        }));
        this.qc.invalidateQueries({ queryKey: ["watchlist-quotes"], exact: false });
        break;
      }
      case "events":
        // The feed is paginated and filtered server-side, so a new event
        // invalidates rather than being spliced in: splicing would have to
        // reimplement the server's filters in the client and would drift.
        this.qc.invalidateQueries({ queryKey: ["events"], exact: false });
        break;
      case "scanner":
        this.qc.invalidateQueries({ queryKey: ["scan-latest"], exact: false });
        break;
      case "health":
        this.qc.invalidateQueries({ queryKey: ["stream-health"], exact: false });
        break;
    }
  }

  private refetchFor(topic: StreamTopic) {
    const keys: Record<StreamTopic, string> = {
      quotes: "quote",
      events: "events",
      scanner: "scan-latest",
      health: "stream-health",
    };
    this.qc.invalidateQueries({ queryKey: [keys[topic]], exact: false });
  }

  private set(patch: Partial<StreamState>) {
    this.state = { ...this.state, ...patch };
    for (const fn of this.listeners) fn(this.state);
  }
}
