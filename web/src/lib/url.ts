import { useCallback } from "react";
import { useSearchParams } from "react-router-dom";

/**
 * A piece of page state that lives in the address bar.
 *
 * Filters, the open item, the chosen tab: anything a person would expect to
 * survive a reload, a back button or being pasted to someone else. Defaults
 * are left out of the URL so an unfiltered page has a clean address.
 */
export function useUrlState<T extends string = string>(key: string, fallback: NoInfer<T>): [T, (v: T) => void] {
  const [params, setParams] = useSearchParams();
  const value = (params.get(key) as T | null) ?? fallback;
  const set = useCallback(
    (v: T) => {
      setParams(
        (p) => {
          const next = new URLSearchParams(p);
          if (v === fallback || v === "") next.delete(key);
          else next.set(key, v);
          return next;
        },
        { replace: true },
      );
    },
    [key, fallback, setParams],
  );
  return [value, set];
}

/** The same, for a list: stored comma-separated. */
export function useUrlList(key: string): [string[], (v: string[]) => void] {
  const [raw, setRaw] = useUrlState(key, "");
  const list = raw ? raw.split(",").filter(Boolean) : [];
  const set = useCallback((v: string[]) => setRaw(v.join(",")), [setRaw]);
  return [list, set];
}

/** A link from data, kept only if it is http(s): feeds are untrusted input. */
export function safeHref(u: string | undefined | null): string | undefined {
  return u && /^https?:\/\//i.test(u.trim()) ? u.trim() : undefined;
}
