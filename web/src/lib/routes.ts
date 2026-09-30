/**
 * Every page's code, loadable ahead of the click.
 *
 * Navigation calls `preload` on hover and focus, so by the time a pointer
 * lands the page's chunk is usually already parsed, and a route change is
 * a render rather than a download.
 */
export const loaders = {
  "/alerts": () => import("../components/pages/AlertsPage"),
  "/algorithms": () => import("../components/pages/AlgorithmsPage"),
  "/calendar": () => import("../components/pages/CalendarPage"),
  "/ai": () => import("../components/pages/CalibrationPage"),
  "/charts": () => import("../components/pages/ChartsPage"),
  "/congress": () => import("../components/pages/CongressPage"),
  "/eventstudy": () => import("../components/pages/EventStudyPage"),
  "/geopolitics": () => import("../components/pages/GeopoliticsPage"),
  "/journal": () => import("../components/pages/JournalPage"),
  "/positions": () => import("../components/pages/PositionsPage"),
  "/paper": () => import("../components/pages/PaperPage"),
  "/forecast": () => import("../components/pages/ForecastPage"),
  "/news": () => import("../components/pages/NewsPage"),
  "/research": () => import("../components/pages/ResearchPage"),
  "/scanner": () => import("../components/pages/ScannerPage"),
  "/sources": () => import("../components/pages/SourcesPage"),
  "/smartmoney": () => import("../components/pages/SmartMoneyPage"),
} as const;

const started = new Set<string>();

export function preload(path: string) {
  const key = (Object.keys(loaders) as (keyof typeof loaders)[]).find((k) => path.startsWith(k));
  if (!key || started.has(key)) return;
  started.add(key);
  void loaders[key]().catch(() => started.delete(key));
}
