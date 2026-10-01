// Route-level browser smoke test. Every page renders against deterministic
// API fixtures, so this catches broken component/API assumptions without
// touching a running deployment or consuming provider budgets.
import assert from "node:assert/strict";
import { chromium } from "playwright";

const now = new Date().toISOString();
const event = {
  id: 1,
  event_type: "MONETARY_POLICY",
  headline: "Federal Reserve publishes its latest policy decision",
  summary: "Officials held rates steady and described incoming data as mixed.",
  discovered_at: now,
  published_at: now,
  updated_at: now,
  best_trust: 100,
  source_count: 3,
  official: true,
  importance: 88,
  age_label: "now",
  primary_url: "https://example.com/release",
};
const finding = {
  id: 1,
  scan_id: 1,
  as_of: now,
  scanned_at: now,
  symbol: "NVDA",
  signals: ["volume_spike", "price_move"],
  score: 91,
  close: 140,
  return_1d: 4.8,
  return_5d: 7.1,
  return_z: 2.8,
  volume: 1000000,
  volume_ratio: 2.7,
  volume_z: 3.1,
  gap_percent: 1.2,
  pct_from_52w_high: -2,
  pct_from_52w_low: 80,
  explained: false,
};
const alert = {
  id: 1,
  algorithm_id: 1,
  algorithm_name: "Volume breakout",
  symbol: "AAPL",
  interval: "1d",
  fired_at: now,
  bar_time: now,
  price: 220,
  summary: "AAPL volume breakout confirmed",
  conditions: [],
  delivery: [],
};
const catalyst = {
  symbol: "MSFT",
  industry: "Software",
  venue: "US",
  earnings_date: now,
  earnings_in_days: 2,
  next_kind: "earnings",
  next_date: now,
  next_in_days: 2,
};

function response(path) {
  if (path === "/api/auth/status") return { required: false, authenticated: true };
  if (path === "/api/meta") return { app: "Bellwether", display_tz: "UTC", features: {}, intervals: ["1d"], marketdata_providers: [] };
  if (path === "/api/health") return { providers: [], degraded_providers: [], budgets: {}, market_data_degraded: false };
  if (path === "/api/ai/status") return { status: "ok", budget: { used: 20, limit: 1000, exhausted: false }, model: "deepseek-flash", search: true };
  if (path === "/api/ai/brief") return { stale: false, brief: { date: now.slice(0, 10), generated_at: now, model: "deepseek-flash", status: "ok", headline: "Policy signals and unusual volume lead today", bullets: ["The Fed held rates steady.", "NVDA volume is 2.7× its recent average."], watch_today: ["NVDA", "MSFT earnings"], coverage: { symbols: 2, articles: 8, alerts: 1 } } };
  if (path === "/api/ai/calibration") return { buckets: [], resolved: 0, pending: 1, mean_brier: 0, baseline_brier: 0, generated_at: now };
  if (path === "/api/ai/outlooks" || path.endsWith("/outlooks")) return { outlooks: [] };
  if (path === "/api/alerts") return { alerts: [alert], unread: 1 };
  if (path === "/api/events") return { events: [event], total: 1, limit: 20, offset: 0 };
  if (path.endsWith("/events")) return { events: [event], total: 1, limit: 20, offset: 0 };
  if (path === "/api/events/types") return { types: [{ type: "MONETARY_POLICY", label: "Monetary policy" }] };
  if (path === "/api/scan/latest") return { findings: [finding], count: 1 };
  if (path.includes("/api/scan/symbols/")) return { findings: [finding], count: 1, symbol: "AAPL" };
  if (path === "/api/calendar") return { catalysts: [catalyst], days: 14, total: 1 };
  if (path === "/api/watchlists") return { watchlists: [{ id: 1, name: "Core", position: 0, count: 1, created_at: now }], max: 5 };
  if (path.includes("/api/watchlists/1/symbols")) return { items: [{ symbol: "AAPL", ticker: "AAPL", exchange: "US", currency: "USD", note: "", added_at: now, price: 220, change_percent: 1.2, spark: [210, 215, 220], stale: false }] };
  if (path === "/api/symbols/AAPL/candles") return { symbol: "AAPL", interval: "1d", currency: "USD", candles: [], source: "fixture", fetched_at: now, stale: false };
  if (path === "/api/symbols/AAPL/quote") return { symbol: "AAPL", currency: "USD", quote: { price: 220, change: 2, change_percent: 0.92, day_high: 222, day_low: 217, volume: 1000000, as_of: now }, source: "fixture", fetched_at: now, stale: false };
  if (path === "/api/symbols/AAPL/fundamentals") return { symbol: "AAPL", snapshot: { symbol: "AAPL", as_of: now } };
  if (path === "/api/symbols/search") return { results: [] };
  if (path === "/api/symbols/sectors") return { sectors: {} };
  if (path === "/api/positions") return { positions: [] };
  if (path === "/api/journal") return { trades: [] };
  if (path === "/api/congress/filings") return { filings: [] };
  if (path === "/api/eventstudy/types") return { types: [{ event_type: "EARNINGS", count: 20 }] };
  if (path === "/api/eventstudy") return { event_type: "EARNINGS", benchmark: "GSPC.INDEX", holding_days: 5, total_events: 20, samples: 10, mean_abnormal_return_pct: 0.5, median_abnormal_return_pct: 0.2, stddev_pct: 2, hit_rate: 55, reaction: { mean_pct: 0.5, median_pct: 0.2, stddev_pct: 2, hit_rate: 55, t: 1.1 }, drift: { mean_pct: 0.5, median_pct: 0.2, stddev_pct: 2, hit_rate: 55, t: 1.1 }, abs_reaction_pct: 3.1, groups: [{ label: "positive", samples: 6, reaction: { mean_pct: 0.5, median_pct: 0.2, stddev_pct: 2, hit_rate: 55, t: 1.1 }, drift: { mean_pct: 0.5, median_pct: 0.2, stddev_pct: 2, hit_rate: 55, t: 1.1 }, abs_reaction_pct: 3 }, { label: "negative", samples: 4, reaction: { mean_pct: 0.5, median_pct: 0.2, stddev_pct: 2, hit_rate: 55, t: 1.1 }, drift: { mean_pct: 0.5, median_pct: 0.2, stddev_pct: 2, hit_rate: 55, t: 1.1 }, abs_reaction_pct: 3 }], warnings: [] };
  if (path === "/api/smartmoney/funds") return { funds: [] };
  if (path === "/api/forecast") return {
    report: { at: now, as_of: now, horizon: 5, symbols: 2, train_rows: 1000, years: [{ year: 2025, days: 100, rank_ic: 0.03, t: 2, spread_pct: 0.4, top_hit_pct: 60 }],
      overall: { year: 0, days: 100, rank_ic: 0.03, t: 2, spread_pct: 0.4, top_hit_pct: 60 },
      factors: [{ key: "mom_12_1", label: "12-month momentum", why: "Momentum persists.", rank_ic: 0.03, t: 3, weight: 0.03 }] },
    top: [{ symbol: "AAPL", name: "Apple Inc.", sector: "Information Technology", score: 0.1, percentile: 99, drivers: [{ key: "mom_12_1", label: "12-month momentum", contribution: 0.02 }] }],
    bottom: [], total: 2, live: [], live_summary: { runs: 0 },
  };
  if (path.startsWith("/api/forecast/symbols/")) return { symbol: "AAPL", latest: { as_of: now, symbol: "AAPL", score: 0.1, percentile: 99, drivers: [] }, history: [], horizon: 5, stale: false };
  if (path === "/api/paper/wallets") return { wallets: [], defaults: {} };
  if (path === "/api/algorithms/templates") return { templates: [] };
  if (path === "/api/algorithms/vocabulary") return { indicators: [], intervals: [], operators: [] };
  if (path === "/api/algorithms") return { algorithms: [] };
  if (path === "/api/screens/fields") return { fields: [], ops: [], max_conditions: 8, max_limit: 100 };
  if (path === "/api/screens") return { screens: [] };
  if (path === "/api/research/scrapers") return { available: true, scrapers: ["sec_fulltext", "federal-register"] };
  if (path === "/api/research/conversations") return { conversations: [] };
  if (path === "/api/ingest/sources") return { sources: [], total: 0, healthy: 0, failing: 0, unpolled: 0, items_held: 0 };
  if (path === "/api/ingest/stats") return { raw_items: 0, pending_items: 0, events: 0, classified_events: 0, evidence_links: 0, entity_links: 0, size_bytes: 0 };
  if (path === "/api/ingest/pipeline") return { phase: "idle", lanes: [], due: [], sources: 0, healthy: 0, failing: 0 };
  if (path === "/api/ingest/latency") return { sources: [], days: 30 };
  if (path === "/api/stream/health") return { sources: [] };
  if (path === "/api/smartmoney/overview") return { days: 90, top_buys: [], top_sells: [], cluster_buys: [], largest_buys: [], funds: [], fund_buys: [], insider_count: 0 };
  if (path === "/api/smartmoney/insiders") return { trades: [] };
  if (path.startsWith("/api/smartmoney/symbol/")) return { symbol: "AAPL", trades: [], funds: [], congress: [] };
  if (path === "/api/web/search") return { query: "fed", kind: "news", results: [{ title: "Fed holds rates, wires report", url: "https://example.com/fed", publisher: "example.com", published_at: now, engine: "bing_news" }] };
  return {};
}

const routes = [
  "/dashboard", "/charts", "/scanner/signals", "/scanner/screens", "/news",
  "/geopolitics", "/congress", "/eventstudy", "/calendar", "/positions",
  "/journal", "/research", "/algorithms/build", "/algorithms/backtest",
  "/alerts", "/sources", "/ai", "/smartmoney/overview", "/smartmoney/insiders",
  "/smartmoney/funds", "/smartmoney/congress", "/forecast", "/paper/overview",
];

const browser = await chromium.launch({ headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } });
  const errors = [];
  const unknown = new Set();
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/stream") return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": smoke\n\n" });
    const body = response(url.pathname);
    if (Object.keys(body).length === 0) unknown.add(url.pathname);
    await route.fulfill({ status: 200, json: body });
  });

  for (const route of routes) {
    await page.goto((process.env.BELLWETHER_UI_URL || "http://127.0.0.1:5174") + route);
    // Waits for content rather than a fixed delay: a cold dev server can
    // take longer than any fixed pause to serve a page's first chunk.
    await page.waitForFunction(() => document.body.innerText.trim().length > 0, null, { timeout: 15000 }).catch(() => {});
    assert.ok((await page.locator("body").innerText()).trim().length > 0, `${route} rendered no text`);
  }

  await page.goto((process.env.BELLWETHER_UI_URL || "http://127.0.0.1:5174") + "/dashboard");
  await page.getByRole("heading", { name: "Needs a look" }).waitFor();
  await page.screenshot({ path: "/tmp/bellwether-overview-desktop.png", fullPage: true });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await page.getByRole("heading", { name: "Needs a look" }).waitFor();
  await page.screenshot({ path: "/tmp/bellwether-overview-mobile.png", fullPage: true });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);

  // A news search also shows what the open web has, and every clock time
  // names its zone: New York by default, the viewer's choice after that.
  const base = process.env.BELLWETHER_UI_URL || "http://127.0.0.1:5174";
  await page.setViewportSize({ width: 1440, height: 960 });
  await page.goto(base + "/news?q=fed");
  await page.getByRole("link", { name: "Fed holds rates, wires report" }).waitFor();
  const clock = page.locator("time").first();
  assert.match(await clock.innerText(), /\d{2}:\d{2}\sET$/, "feed times should be labelled ET by default");
  await page.getByRole("button", { name: /All systems normal/ }).click();
  await page.getByRole("radio", { name: "UTC", exact: true }).click();
  assert.match(await page.locator("time").first().innerText(), /\d{2}:\d{2}\sUTC$/, "choosing UTC should relabel every time");

  assert.deepEqual(errors, []);
  console.log(`Application browser checks passed across ${routes.length} routes.`);
  if (unknown.size) console.log(`Unmodelled optional API calls: ${[...unknown].sort().join(", ")}`);
} finally {
  await browser.close();
}
