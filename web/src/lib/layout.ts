import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Layout the operator controls, and the app remembers.
 *
 * Density is a matter of taste and of screen, and no single set of widths is
 * right for a 13-inch laptop and a 32-inch monitor. Rather than guessing,
 * every divider is draggable and every panel collapses, and the result is
 * persisted — a layout you have to rebuild on every reload is not a layout.
 */

const PREFIX = "tradesys.layout.";

function read<T>(key: string, fallback: T): T {
  try {
    const raw = window.localStorage.getItem(PREFIX + key);
    return raw == null ? fallback : (JSON.parse(raw) as T);
  } catch {
    // Private windows and blocked site data throw on access rather than
    // returning null. A layout preference is never worth failing a render.
    return fallback;
  }
}

function write<T>(key: string, value: T) {
  try {
    window.localStorage.setItem(PREFIX + key, JSON.stringify(value));
  } catch {
    /* ignore: see read */
  }
}

/** State that survives a reload. */
export function usePersisted<T>(key: string, initial: T) {
  const [value, setValue] = useState<T>(() => read(key, initial));
  useEffect(() => write(key, value), [key, value]);
  return [value, setValue] as const;
}

export interface ResizeOptions {
  key: string;
  initial: number;
  min: number;
  max: number;
  /** Which way the pointer must travel to make the pane larger. */
  direction: "e" | "w" | "n";
}

/**
 * A draggable divider.
 *
 * Pointer events rather than mouse events, so a trackpad, a touchscreen and a
 * pen all work from one code path. The pointer is captured on the handle, so
 * a fast drag that leaves the element still tracks — without capture, dragging
 * quickly across the chart drops the gesture halfway.
 */
export function useResize({ key, initial, min, max, direction }: ResizeOptions) {
  const [size, setSize] = usePersisted(key, initial);
  const [dragging, setDragging] = useState(false);
  const start = useRef({ pos: 0, size: 0 });

  const clamp = useCallback((v: number) => Math.min(max, Math.max(min, v)), [min, max]);

  const onPointerDown = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault();
      (e.target as Element).setPointerCapture(e.pointerId);
      start.current = { pos: direction === "n" ? e.clientY : e.clientX, size };
      setDragging(true);
    },
    [direction, size],
  );

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      if (!dragging) return;
      const now = direction === "n" ? e.clientY : e.clientX;
      const delta = now - start.current.pos;
      // "e" grows rightward, "w" and "n" grow the other way.
      const signed = direction === "e" ? delta : -delta;
      setSize(clamp(start.current.size + signed));
    },
    [clamp, direction, dragging, setSize],
  );

  const stop = useCallback((e: React.PointerEvent) => {
    try {
      (e.target as Element).releasePointerCapture(e.pointerId);
    } catch {
      /* the pointer may already be gone */
    }
    setDragging(false);
  }, []);

  /** Double-click restores the default, so a bad drag is one gesture to undo. */
  const reset = useCallback(() => setSize(initial), [initial, setSize]);

  return { size, dragging, handlers: { onPointerDown, onPointerMove, onPointerUp: stop, onPointerCancel: stop }, reset };
}

/** Whether a media query matches, kept current as the window changes. */
export function useMedia(query: string): boolean {
  const [hit, setHit] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const m = window.matchMedia(query);
    const on = () => setHit(m.matches);
    on();
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, [query]);
  return hit;
}
