import { useCallback, useEffect, useState } from "react";

export type ThemeChoice = "system" | "light" | "dark";

const KEY = "bellwether.theme";

function read(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

function apply(choice: ThemeChoice) {
  const root = document.documentElement;
  if (choice === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", choice);
}

/**
 * The theme, as the viewer chose it.
 *
 * "System" is a real third state rather than a default that gets overwritten:
 * it stamps nothing on <html>, so the stylesheet follows the operating system
 * and keeps following it when that changes at sunset.
 */
export function useTheme() {
  const [choice, setChoice] = useState<ThemeChoice>(read);

  useEffect(() => apply(choice), [choice]);

  const set = useCallback((next: ThemeChoice) => {
    try {
      if (next === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, next);
    } catch {
      /* private mode: the choice lasts for this tab only */
    }
    setChoice(next);
  }, []);

  return [choice, set] as const;
}

/** Whether the page is currently drawn dark, whichever way that was decided. */
export function isDark(): boolean {
  const stamped = document.documentElement.getAttribute("data-theme");
  if (stamped) return stamped === "dark";
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}
