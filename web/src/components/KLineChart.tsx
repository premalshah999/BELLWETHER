import { useEffect, useRef } from "react";
import {
  CandleType,
  FormatDateType,
  LineType,
  PolygonType,
  TooltipShowRule,
  TooltipShowType,
  YAxisPosition,
  YAxisType,
  dispose,
  init,
  type Chart,
} from "klinecharts";
import type { Candle, Interval } from "../lib/api";
import { usePalette, type Palette } from "../lib/palette";
import { timeZoneOf } from "../lib/symbol";

/**
 * The price chart.
 *
 * klinecharts renders to a canvas, which matters at this density: an SVG chart
 * with four hundred candles plus a volume pane plus two moving averages is
 * several thousand DOM nodes that the browser lays out on every resize.
 *
 * Everything visual is set explicitly. The library's defaults are built for a
 * light theme, and inheriting even a few of them — a grid line, an axis
 * label — produces the one washed-out element that makes a dark interface look
 * unfinished.
 */
/**
 * One study on the chart.
 *
 * `pane` decides whether it draws over the candles or in a strip of its own:
 * an average shares the price axis and belongs on top, an oscillator does not
 * and would flatten the candles to a line if forced onto the same scale.
 */
export interface Study {
  name: string;
  params: number[];
  pane: "main" | "sub";
}

/** The default study set: the levels people actually watch, plus volume. */
export const DEFAULT_STUDIES: Study[] = [
  { name: "MA", params: [20, 50, 200], pane: "main" },
  { name: "VOL", params: [], pane: "sub" },
];

/**
 * How the bars are drawn.
 *
 * The values are klinecharts' own, so the prop can be handed straight to the
 * style object without a translation table that would need keeping in step.
 */
export type ChartKind = CandleType;

/** The bar styles worth offering, in the order they belong in a picker. */
export const CHART_KINDS: { id: CandleType; label: string }[] = [
  { id: CandleType.CandleSolid, label: "Candle" },
  { id: CandleType.CandleStroke, label: "Hollow" },
  { id: CandleType.Ohlc, label: "Bar" },
  { id: CandleType.Area, label: "Area" },
];

/** Drawing tools, by the name klinecharts knows them under. */
export const TOOLS = [
  { id: "horizontalStraightLine", label: "level" },
  { id: "straightLine", label: "line" },
  { id: "rayLine", label: "ray" },
  { id: "segment", label: "segment" },
  { id: "priceChannelLine", label: "channel" },
  { id: "fibonacciLine", label: "fib" },
] as const;

export function KLineChart({
  candles,
  symbol,
  interval,
  timeZone = timeZoneOf(symbol),
  studies = DEFAULT_STUDIES,
  kind = CandleType.CandleSolid,
  tool = null,
  onToolUsed,
  clearToken = 0,
}: {
  candles: Candle[];
  symbol: string;
  /** Decides whether a bar is labelled with a date or a time. */
  interval: Interval;
  /** The venue's zone, not the browser's. See the note in the effect. */
  timeZone?: string;
  /** Studies to draw. Diffed against what is on the chart. */
  studies?: Study[];
  /** Candles, hollow candles, bars, or an area under the close. */
  kind?: ChartKind;
  /** A drawing tool to arm, or null. Disarmed once the shape is placed. */
  tool?: string | null;
  onToolUsed?: () => void;
  /** Incremented by the parent to erase every drawing on this chart. */
  clearToken?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const chartRef = useRef<Chart | null>(null);
  // What is currently drawn, as key -> pane id, so a change adds and removes
  // only what actually differs rather than tearing the chart down.
  const drawn = useRef(new Map<string, string>());
  const fitted = useRef("");
  const palette = usePalette();
  const paletteRef = useRef(palette);
  paletteRef.current = palette;

  // A theme change repaints in place: rebuilding would lose the zoom, the
  // pan and every line the reader drew.
  useEffect(() => {
    chartRef.current?.setStyles(chartStyles(palette));
  }, [palette]);

  // The chart instance is created once and fed data on updates. Re-creating it
  // per render would discard the viewport — the zoom and pan an operator set —
  // on every price tick.
  useEffect(() => {
    if (!ref.current) return;
    const chart = init(ref.current, {
      /*
       * The exchange's timezone, never the browser's.
       *
       * A daily bar is stamped at the start of the exchange's day. Rendered in
       * another zone, every daily candle can be labelled a day early; the data
       * was never wrong, the axis was reading it in the wrong zone.
       */
      timezone: timeZone,
      customApi: {
        /*
         * A daily bar has no time of day.
         *
         * The library's default format carries hours and minutes at every
         * resolution, so a daily candle announced itself as "08-25 18:30" —
         * a timestamp that is both the wrong day and a precision the bar does
         * not have. Intraday bars do want the clock, so the format follows
         * the interval rather than being fixed.
         */
        formatDate: (_fmt, timestamp, _format, type) => {
          const d = new Date(timestamp);
          const daily = interval === "1d" || interval === "1wk";
          const date = d.toLocaleDateString("en-US", {
            timeZone,
            day: "2-digit",
            month: "short",
            ...(type === FormatDateType.XAxis ? {} : { year: "numeric" }),
          });
          if (daily) return date;
          const time = d.toLocaleTimeString("en-US", {
            timeZone,
            hour: "2-digit",
            minute: "2-digit",
            hour12: false,
          });
          // The axis has no room for both; the crosshair and tooltip do, and
          // that is where a reader checks which bar they are actually on.
          return type === FormatDateType.XAxis ? time : `${date} ${time}`;
        },
      },
      styles: chartStyles(paletteRef.current),
    });
    if (!chart) return;
    chartRef.current = chart;

    // Studies are applied by their own effect below, so that adding one does
    // not rebuild the chart and lose the operator's zoom.

    return () => {
      dispose(ref.current!);
      chartRef.current = null;
      drawn.current.clear();
    };
    // Rebuilt when the interval or zone changes, because both are baked into
    // the formatter above. Data changes do not rebuild — see the next effect.
  }, [interval, timeZone]);

  /*
   * Studies, diffed.
   *
   * Keyed on name plus periods plus pane, so changing MA(20,50,200) to
   * MA(10,20) removes one and adds the other, while an unrelated study on the
   * chart is left exactly where it is. Recreating all of them on every change
   * would reset each sub-pane's height, which the operator may have dragged.
   */
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;

    const want = new Map<string, Study>();
    for (const st of studies) {
      want.set(`${st.name}:${st.params.join(",")}:${st.pane}`, st);
    }

    for (const [key, paneId] of [...drawn.current]) {
      if (!want.has(key)) {
        chart.removeIndicator(paneId, key.split(":")[0]);
        drawn.current.delete(key);
      }
    }

    for (const [key, st] of want) {
      if (drawn.current.has(key)) continue;
      const onMain = st.pane === "main";
      const paneId = chart.createIndicator(
        {
          name: st.name,
          calcParams: st.params,
          // Line colours come from the chart's themed styles (five of
          // them, so a study with many periods never falls through to the
          // library's own palette). Volume draws bars only.
          ...(st.name === "VOL" ? { styles: { lines: [] } } : {}),
        },
        false,
        onMain ? { id: "candle_pane" } : { height: st.name === "VOL" ? 72 : 88 },
      );
      if (paneId) drawn.current.set(key, paneId);
    }
  }, [studies]);

  // Arming a drawing tool. klinecharts places one overlay per call, so the
  // tool disarms itself as soon as the shape is drawn — a tool that stayed
  // armed would scatter shapes on every subsequent click.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart || !tool) return;
    chart.createOverlay({
      name: tool,
      onDrawEnd: () => {
        onToolUsed?.();
        return true;
      },
    });
  }, [tool, onToolUsed]);

  useEffect(() => {
    if (clearToken > 0) chartRef.current?.removeOverlay();
  }, [clearToken]);

  /*
   * Tell the chart when its box changes.
   *
   * klinecharts sizes its canvases once, at init, and never re-measures on its
   * own. In a grid that re-lays out — one chart becoming four, a rail being
   * dragged or collapsed — every canvas kept the width it was born with and
   * painted straight over its neighbours: a volume pane 842px wide sat on top
   * of the pane below it at z-index 2, swallowing that pane's interval buttons
   * and drawing tools. The charts looked fine and the clicks went nowhere.
   *
   * A ResizeObserver on the container is the fix, and it has to be on the
   * container rather than the window: a rail drag changes the pane's width
   * without the window resizing at all.
   */
  useEffect(() => {
    const host = ref.current;
    if (!host || typeof ResizeObserver === "undefined") return;
    let frame = 0;
    const ro = new ResizeObserver(() => {
      // Coalesced into a frame: a drag fires this continuously, and resizing
      // the canvases on every pixel is what makes a resize feel like tar.
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => chartRef.current?.resize());
    });
    ro.observe(host);
    return () => {
      cancelAnimationFrame(frame);
      ro.disconnect();
    };
  }, []);

  // The bar style is a style update rather than a rebuild, so switching from
  // candles to an area keeps the zoom and every drawing already placed.
  useEffect(() => {
    chartRef.current?.setStyles({ candle: { type: kind } });
  }, [kind]);

  // Data is applied separately from construction so a price update repaints
  // without rebuilding the chart.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    chart.applyNewData(
      candles.map((c) => ({
        timestamp: new Date(c.t).getTime(),
        open: c.o,
        high: c.h,
        low: c.l,
        close: c.c,
        volume: c.v,
      })),
    );

    // Fit the range to the pane. klinecharts spaces bars at a fixed width, so
    // a month of daily bars filled the right third and left the rest of the
    // chart empty. Refit only when what is being shown changes, never on the
    // minute's refresh, so a zoom the reader chose is left alone.
    const key = `${symbol}|${interval}|${candles.length}`;
    if (fitted.current !== key && ref.current && candles.length > 0) {
      fitted.current = key;
      const usable = ref.current.clientWidth - 72;
      const space = Math.max(1.5, Math.min(36, usable / (candles.length + 3)));
      chart.setBarSpace(space);
      chart.setOffsetRightDistance(space * 3);
      chart.scrollToRealTime();
    }
  }, [candles, symbol, interval]);

  return <div ref={ref} className="h-full w-full" />;
}

/**
 * Every visual choice on the chart, from the theme's own colours.
 *
 * Nothing is left to the library's defaults: inheriting even one grid line
 * or axis label from a palette built for another background is the washed-out
 * element that makes a chart look pasted in.
 */
function chartStyles(p: Palette) {
  const text = (color: string) => ({ size: 11, family: p.font, color });
  return {
    grid: {
      show: true,
      horizontal: { show: true, size: 1, color: p.line, style: LineType.Solid, dashedValue: [2, 3] },
      vertical: { show: false, size: 1, color: p.line, style: LineType.Solid, dashedValue: [2, 3] },
    },
    candle: {
      bar: {
        upColor: p.up,
        downColor: p.down,
        noChangeColor: p.ink3,
        upBorderColor: p.up,
        downBorderColor: p.down,
        noChangeBorderColor: p.ink3,
        upWickColor: p.up,
        downWickColor: p.down,
        noChangeWickColor: p.ink3,
      },
      area: {
        lineSize: 2,
        lineColor: p.brass,
        value: "close",
        backgroundColor: [
          { offset: 0, color: p.brass + "00" },
          { offset: 1, color: p.brass + "33" },
        ],
      },
      priceMark: {
        show: true,
        high: { show: true, color: p.ink3, textSize: 11, textFamily: p.font },
        low: { show: true, color: p.ink3, textSize: 11, textFamily: p.font },
        last: {
          show: true,
          upColor: p.up,
          downColor: p.down,
          noChangeColor: p.ink3,
          line: { show: true, style: LineType.Dashed, dashedValue: [3, 3], size: 1 },
          text: { show: true, size: 11, family: p.font, color: "#ffffff", borderRadius: 3, paddingLeft: 5, paddingRight: 5, paddingTop: 2, paddingBottom: 2 },
        },
      },
      tooltip: {
        showRule: TooltipShowRule.FollowCross,
        showType: TooltipShowType.Standard,
        text: { ...text(p.ink2), marginLeft: 10, marginTop: 8 },
        rect: { paddingLeft: 8, paddingRight: 8, borderRadius: 6, borderSize: 1, borderColor: p.line, color: p.raised },
      },
    },
    indicator: {
      // The averages: brass leads, then two greys. A chart where every line is
      // its own hue reads as a legend to decode rather than a price to follow.
      lines: [
        { color: p.brass, size: 1.5, style: LineType.Solid, dashedValue: [2, 2] },
        { color: p.ink2, size: 1, style: LineType.Solid, dashedValue: [2, 2] },
        { color: p.ink3, size: 1, style: LineType.Solid, dashedValue: [2, 2] },
        { color: p.up, size: 1, style: LineType.Solid, dashedValue: [2, 2] },
        { color: p.down, size: 1, style: LineType.Solid, dashedValue: [2, 2] },
      ],
      bars: [{ style: PolygonType.Fill, borderSize: 0, upColor: p.upSoft, downColor: p.downSoft, noChangeColor: p.line }],
      tooltip: { showRule: TooltipShowRule.FollowCross, text: text(p.ink2) },
    },
    xAxis: {
      axisLine: { show: true, color: p.line, size: 1 },
      tickLine: { show: false, size: 1, length: 3, color: p.line },
      tickText: { show: true, ...text(p.ink3) },
    },
    yAxis: {
      type: YAxisType.Normal,
      position: YAxisPosition.Right,
      axisLine: { show: false, color: p.line, size: 1 },
      tickLine: { show: false, size: 1, length: 3, color: p.line },
      tickText: { show: true, ...text(p.ink3) },
    },
    separator: { size: 1, color: p.line },
    crosshair: {
      show: true,
      horizontal: {
        line: { show: true, style: LineType.Dashed, dashedValue: [3, 3], size: 1, color: p.ink3 },
        text: { show: true, size: 11, family: p.font, color: p.brassInk, backgroundColor: p.brass, borderRadius: 3, paddingLeft: 5, paddingRight: 5, paddingTop: 2, paddingBottom: 2 },
      },
      vertical: {
        line: { show: true, style: LineType.Dashed, dashedValue: [3, 3], size: 1, color: p.ink3 },
        text: { show: true, size: 11, family: p.font, color: p.brassInk, backgroundColor: p.brass, borderRadius: 3, paddingLeft: 5, paddingRight: 5, paddingTop: 2, paddingBottom: 2 },
      },
    },
  };
}
