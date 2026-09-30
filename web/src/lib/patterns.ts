import type { Candle } from "./api";

/**
 * Candlestick pattern detection.
 *
 * Every rule here is expressed in proportions of the bar's own range rather
 * than in absolute prices, for the same reason the scanner works in z-scores:
 * a $4 body means one thing on a $40 stock and nothing at all on a $4,000
 * one.
 *
 * The patterns that need context — a hammer is only a hammer at the bottom of
 * a decline; the same bar in an uptrend is a hanging man and means the
 * opposite — take it from the preceding bars rather than being reported
 * bare. A pattern library that ignores trend will happily label every third
 * candle a reversal signal, which is worse than labelling none of them.
 *
 * Nothing here predicts anything. A detection says "this shape occurred",
 * and the direction attached to it is the shape's conventional reading, not a
 * forecast.
 */

export type Direction = "bullish" | "bearish" | "neutral";

export interface Pattern {
  /** Index of the bar that completes the pattern. */
  index: number;
  /** ISO timestamp of that bar, for display and for locating it on the chart. */
  time: string;
  name: string;
  direction: Direction;
  /** How many bars the pattern spans, ending at `index`. */
  span: number;
  /** What the shape is, in one line. */
  note: string;
}

interface Shape {
  o: number;
  h: number;
  l: number;
  c: number;
  body: number;
  range: number;
  upper: number;
  lower: number;
  bull: boolean;
  bear: boolean;
  /** Body as a fraction of the whole range. 0 is a perfect doji. */
  bodyPct: number;
  mid: number;
}

function shape(c: Candle): Shape {
  const body = Math.abs(c.c - c.o);
  const range = c.h - c.l;
  return {
    o: c.o,
    h: c.h,
    l: c.l,
    c: c.c,
    body,
    range,
    upper: c.h - Math.max(c.o, c.c),
    lower: Math.min(c.o, c.c) - c.l,
    bull: c.c > c.o,
    bear: c.c < c.o,
    bodyPct: range > 0 ? body / range : 0,
    mid: (c.o + c.c) / 2,
  };
}

/**
 * The prevailing direction going into a bar.
 *
 * Five bars back, which is short enough to still be the local move and long
 * enough not to flip on a single quiet day. Bars before the window simply
 * return "neutral", which suppresses the context-dependent patterns rather
 * than guessing at them.
 */
function trend(cs: Candle[], i: number): Direction {
  const look = 5;
  const back = cs[i - look];
  const prior = cs[i - 1];
  if (!back || !prior) return "neutral";
  const then = back.c;
  const now = prior.c;
  if (then <= 0) return "neutral";
  const move = (now / then - 1) * 100;
  if (move > 1.5) return "bullish";
  if (move < -1.5) return "bearish";
  return "neutral";
}

/** A body large enough for its shape to mean anything. */
function substantial(s: Shape, avg: number): boolean {
  return s.body > 0 && s.body >= avg * 0.6;
}

/**
 * Detects every supported pattern across a series.
 *
 * Returns newest first, because a list of patterns is read from the present
 * backwards — what happened this week matters more than what happened in
 * March, and a reader should not have to scroll to reach it.
 */
export function detectPatterns(candles: Candle[]): Pattern[] {
  const cs = candles.filter(
    (c) =>
      Number.isFinite(c.o) && Number.isFinite(c.h) && Number.isFinite(c.l) && Number.isFinite(c.c),
  );
  if (cs.length < 6) return [];

  const shapes = cs.map(shape);
  // Average body over the whole window, used to tell a real body from a
  // rounding artefact. A fixed threshold would misjudge every low-priced
  // stock in the universe.
  const avgBody = shapes.reduce((n, s) => n + s.body, 0) / shapes.length || 1;

  const found: Pattern[] = [];
  const add = (index: number, name: string, direction: Direction, span: number, note: string) => {
    const bar = cs[index];
    if (bar) found.push({ index, time: bar.t, name, direction, span, note });
  };

  for (let i = 0; i < cs.length; i++) {
    const s = shapes[i];
    if (!s || s.range <= 0) continue;
    const p = i > 0 ? shapes[i - 1] ?? null : null;
    const pp = i > 1 ? shapes[i - 2] ?? null : null;
    const t = trend(cs, i);

    // ---- single bar ----

    if (s.bodyPct <= 0.08) {
      if (s.upper > s.range * 0.35 && s.lower > s.range * 0.35) {
        add(i, "Long-legged doji", "neutral", 1, "Opened and closed at the same level after ranging both ways — indecision.");
      } else if (s.lower >= s.range * 0.66) {
        add(i, "Dragonfly doji", t === "bearish" ? "bullish" : "neutral", 1, "Sold off and recovered the entire move by the close.");
      } else if (s.upper >= s.range * 0.66) {
        add(i, "Gravestone doji", t === "bullish" ? "bearish" : "neutral", 1, "Rallied and gave the entire move back by the close.");
      } else {
        add(i, "Doji", "neutral", 1, "Open and close effectively equal — buyers and sellers matched.");
      }
    } else if (s.bodyPct >= 0.9 && s.upper <= s.range * 0.03 && s.lower <= s.range * 0.03) {
      add(i, s.bull ? "Bullish marubozu" : "Bearish marubozu", s.bull ? "bullish" : "bearish", 1,
        `Opened at the ${s.bull ? "low" : "high"} and closed at the ${s.bull ? "high" : "low"} — one side held the bar throughout.`);
    } else if (substantial(s, avgBody) && s.lower >= s.body * 2 && s.upper <= s.body * 0.5) {
      if (t === "bearish") add(i, "Hammer", "bullish", 1, "A long lower wick into a decline: sellers pushed down and were rejected.");
      else if (t === "bullish") add(i, "Hanging man", "bearish", 1, "The same shape at the top of an advance, where the rejection cuts the other way.");
    } else if (substantial(s, avgBody) && s.upper >= s.body * 2 && s.lower <= s.body * 0.5) {
      if (t === "bearish") add(i, "Inverted hammer", "bullish", 1, "A failed push down met with a long upper wick.");
      else if (t === "bullish") add(i, "Shooting star", "bearish", 1, "Rallied hard intraday and closed near the low of the bar.");
    }

    if (!p) continue;

    // ---- two bars ----

    if (
      p.bear && s.bull &&
      s.o <= p.c && s.c >= p.o &&
      s.body > p.body && substantial(s, avgBody)
    ) {
      add(i, "Bullish engulfing", "bullish", 2, "This bar's body completely covers the previous down bar.");
    }
    if (
      p.bull && s.bear &&
      s.o >= p.c && s.c <= p.o &&
      s.body > p.body && substantial(s, avgBody)
    ) {
      add(i, "Bearish engulfing", "bearish", 2, "This bar's body completely covers the previous up bar.");
    }

    if (p.bear && s.bull && substantial(p, avgBody) && s.body < p.body * 0.6 &&
        Math.max(s.o, s.c) <= p.o && Math.min(s.o, s.c) >= p.c) {
      add(i, "Bullish harami", "bullish", 2, "A small up bar held entirely inside the previous large down bar — the decline stalled.");
    }
    if (p.bull && s.bear && substantial(p, avgBody) && s.body < p.body * 0.6 &&
        Math.max(s.o, s.c) <= p.c && Math.min(s.o, s.c) >= p.o) {
      add(i, "Bearish harami", "bearish", 2, "A small down bar held entirely inside the previous large up bar — the advance stalled.");
    }

    if (p.bear && s.bull && substantial(p, avgBody) &&
        s.o < p.l && s.c > p.mid && s.c < p.o) {
      add(i, "Piercing line", "bullish", 2, "Gapped below the prior bar's low and closed back above its midpoint.");
    }
    if (p.bull && s.bear && substantial(p, avgBody) &&
        s.o > p.h && s.c < p.mid && s.c > p.o) {
      add(i, "Dark cloud cover", "bearish", 2, "Gapped above the prior bar's high and closed back below its midpoint.");
    }

    // Tweezers need the two extremes to match closely, measured against the
    // bar's own range so the tolerance scales with the instrument.
    const tol = Math.max(s.range, p.range) * 0.02;
    if (t === "bearish" && Math.abs(s.l - p.l) <= tol && p.bear && s.bull) {
      add(i, "Tweezer bottom", "bullish", 2, "Two bars refused the same low.");
    }
    if (t === "bullish" && Math.abs(s.h - p.h) <= tol && p.bull && s.bear) {
      add(i, "Tweezer top", "bearish", 2, "Two bars failed at the same high.");
    }

    if (!pp) continue;

    // ---- three bars ----

    if (
      pp.bear && substantial(pp, avgBody) &&
      p.bodyPct <= 0.35 && p.body < pp.body * 0.5 &&
      s.bull && substantial(s, avgBody) && s.c > pp.mid
    ) {
      add(i, "Morning star", "bullish", 3, "A heavy down bar, a stalled bar, then a recovery closing back inside the first.");
    }
    if (
      pp.bull && substantial(pp, avgBody) &&
      p.bodyPct <= 0.35 && p.body < pp.body * 0.5 &&
      s.bear && substantial(s, avgBody) && s.c < pp.mid
    ) {
      add(i, "Evening star", "bearish", 3, "A heavy up bar, a stalled bar, then a decline closing back inside the first.");
    }

    if (
      pp.bull && p.bull && s.bull &&
      p.c > pp.c && s.c > p.c &&
      p.o > pp.o && p.o < pp.c && s.o > p.o && s.o < p.c &&
      p.upper <= p.body * 0.5 && s.upper <= s.body * 0.5 &&
      substantial(p, avgBody) && substantial(s, avgBody)
    ) {
      add(i, "Three white soldiers", "bullish", 3, "Three rising bars, each opening inside the last body and closing near its high.");
    }
    if (
      pp.bear && p.bear && s.bear &&
      p.c < pp.c && s.c < p.c &&
      p.o < pp.o && p.o > pp.c && s.o < p.o && s.o > p.c &&
      p.lower <= p.body * 0.5 && s.lower <= s.body * 0.5 &&
      substantial(p, avgBody) && substantial(s, avgBody)
    ) {
      add(i, "Three black crows", "bearish", 3, "Three falling bars, each opening inside the last body and closing near its low.");
    }
  }

  return found.reverse();
}

/**
 * A continuous structure across the whole window, rather than a shape on two
 * or three bars.
 */
export interface Structure {
  name: string;
  direction: Direction;
  detail: string;
}

/**
 * Structural reads: trend, range, and the levels the series keeps returning to.
 *
 * Separate from the candlestick patterns because they answer a different
 * question. A hammer is an event on one bar; "this has been range-bound
 * between 1,240 and 1,320 for six weeks" is a description of the whole
 * window, and it is usually the more actionable of the two.
 */
export function detectStructure(candles: Candle[]): Structure[] {
  const cs = candles.filter((c) => Number.isFinite(c.c));
  if (cs.length < 20) return [];

  const out: Structure[] = [];
  const closes = cs.map((c) => c.c);
  const n = closes.length;

  // Trend, by least-squares slope over the window, expressed as percent per
  // bar so it can be compared across instruments.
  const xs = closes.map((_, i) => i);
  const mx = xs.reduce((a, b) => a + b, 0) / n;
  const my = closes.reduce((a, b) => a + b, 0) / n;
  let num = 0;
  let den = 0;
  for (let i = 0; i < n; i++) {
    const x = xs[i] ?? 0;
    const y = closes[i] ?? 0;
    num += (x - mx) * (y - my);
    den += (x - mx) ** 2;
  }
  const slope = den > 0 ? num / den : 0;
  const perBar = my > 0 ? (slope / my) * 100 : 0;

  // R² decides whether the slope is a trend or a line drawn through noise.
  let ssTot = 0;
  let ssRes = 0;
  for (let i = 0; i < n; i++) {
    const y = closes[i] ?? 0;
    const fit = my + slope * ((xs[i] ?? 0) - mx);
    ssTot += (y - my) ** 2;
    ssRes += (y - fit) ** 2;
  }
  const r2 = ssTot > 0 ? 1 - ssRes / ssTot : 0;

  const hi = Math.max(...closes);
  const lo = Math.min(...closes);
  const width = lo > 0 ? ((hi - lo) / lo) * 100 : 0;

  if (r2 >= 0.6 && Math.abs(perBar) >= 0.05) {
    out.push({
      name: perBar > 0 ? "Uptrend" : "Downtrend",
      direction: perBar > 0 ? "bullish" : "bearish",
      detail: `${perBar > 0 ? "+" : ""}${perBar.toFixed(2)}% per bar, R² ${r2.toFixed(2)} over ${n} bars — a consistent drift rather than a line through noise.`,
    });
  } else if (width <= 15 && r2 < 0.3) {
    out.push({
      name: "Range-bound",
      direction: "neutral",
      detail: `Held a ${width.toFixed(1)}% band between ${lo.toFixed(2)} and ${hi.toFixed(2)} with no directional fit.`,
    });
  } else {
    out.push({
      name: "No clear structure",
      direction: "neutral",
      detail: `R² ${r2.toFixed(2)} against a straight line — the window is neither trending nor holding a range.`,
    });
  }

  // Where the series actually is inside its own window. "Near the high" is a
  // fact a reader checks constantly and should not have to derive.
  const last = closes[n - 1] ?? 0;
  const pos = hi > lo ? ((last - lo) / (hi - lo)) * 100 : 50;
  out.push({
    name: "Position in window",
    direction: pos > 80 ? "bullish" : pos < 20 ? "bearish" : "neutral",
    detail: `${pos.toFixed(0)}% of the way from the window low (${lo.toFixed(2)}) to its high (${hi.toFixed(2)}).`,
  });

  // Higher highs and higher lows, counted on swing pivots rather than raw
  // bars, which is what makes it a structure claim rather than a restatement
  // of the slope.
  const pivots: { i: number; v: number; kind: "h" | "l" }[] = [];
  for (let i = 2; i < n - 2; i++) {
    const v = closes[i];
    if (v == null) continue;
    const w = closes.slice(i - 2, i + 3);
    if (v === Math.max(...w)) pivots.push({ i, v, kind: "h" });
    if (v === Math.min(...w)) pivots.push({ i, v, kind: "l" });
  }
  const highs = pivots.filter((p) => p.kind === "h").slice(-3);
  const lows = pivots.filter((p) => p.kind === "l").slice(-3);
  const [h0, h1, h2] = highs;
  const [l0, l1, l2] = lows;
  if (h0 && h1 && h2 && l0 && l1 && l2) {
    const hh = h2.v > h1.v && h1.v > h0.v;
    const hl = l2.v > l1.v && l1.v > l0.v;
    const lh = h2.v < h1.v && h1.v < h0.v;
    const ll = l2.v < l1.v && l1.v < l0.v;
    if (hh && hl) {
      out.push({ name: "Higher highs and higher lows", direction: "bullish", detail: "The last three swing highs and three swing lows each stepped up." });
    } else if (lh && ll) {
      out.push({ name: "Lower highs and lower lows", direction: "bearish", detail: "The last three swing highs and three swing lows each stepped down." });
    } else if (lh && hl) {
      out.push({ name: "Contracting range", direction: "neutral", detail: "Highs stepping down into lows stepping up — the range is tightening." });
    }
  }

  return out;
}
