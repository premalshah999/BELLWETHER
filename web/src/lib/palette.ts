import { useEffect, useState } from "react";

/**
 * The theme's colours as values, for things drawn on a canvas.
 *
 * A canvas cannot read a CSS variable, so the chart asks for the resolved
 * colours and is handed a fresh set whenever the theme changes — by the
 * viewer's choice or by the operating system at sunset.
 */
export interface Palette {
  ground: string;
  surface: string;
  raised: string;
  line: string;
  lineStrong: string;
  ink: string;
  ink2: string;
  ink3: string;
  brass: string;
  brassInk: string;
  up: string;
  down: string;
  upSoft: string;
  downSoft: string;
  font: string;
}

export function readPalette(): Palette {
  const css = getComputedStyle(document.documentElement);
  const v = (name: string) => css.getPropertyValue(name).trim();
  return {
    ground: v("--ground"),
    surface: v("--surface"),
    raised: v("--raised"),
    line: v("--line"),
    lineStrong: v("--line-strong"),
    ink: v("--ink"),
    ink2: v("--ink-2"),
    ink3: v("--ink-3"),
    brass: v("--brass"),
    brassInk: v("--brass-ink"),
    up: v("--up"),
    down: v("--down"),
    upSoft: v("--up-soft"),
    downSoft: v("--down-soft"),
    font: "Public Sans Variable, system-ui, sans-serif",
  };
}

export function usePalette(): Palette {
  const [p, setP] = useState(readPalette);
  useEffect(() => {
    const update = () => setP(readPalette());
    const mo = new MutationObserver(update);
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    mq.addEventListener("change", update);
    return () => {
      mo.disconnect();
      mq.removeEventListener("change", update);
    };
  }, []);
  return p;
}
