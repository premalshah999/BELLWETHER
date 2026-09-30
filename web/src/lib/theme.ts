import { useCallback, useEffect, useState } from "react";

export type ThemeChoice = "dark" | "light" | "system";

const KEY = "bellwether.theme";

function read(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "system" ? v : "dark";
  } catch {
    return "dark";
  }
}

function apply(choice: ThemeChoice) {
  const root = document.documentElement;
  // Dark is the stylesheet's own default, so it stamps nothing.
  if (choice === "dark") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", choice);
}

/** The theme the viewer chose: dark (the default), light, or the OS's. */
export function useTheme() {
  const [choice, setChoice] = useState<ThemeChoice>(read);
  useEffect(() => apply(choice), [choice]);
  const set = useCallback((next: ThemeChoice) => {
    try {
      if (next === "dark") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, next);
    } catch {
      /* private mode: lasts for this tab */
    }
    setChoice(next);
  }, []);
  return [choice, set] as const;
}
