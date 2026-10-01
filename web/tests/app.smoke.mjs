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
  if (path === "/api/forecast" || path.startsWith("/api/forecast/symbols/")) {
    const q = [-0.06, -0.045, -0.035, -0.027, -0.02, -0.014, -0.009, -0.005, -0.002, 0.001, 0.004, 0.007, 0.011, 0.016, 0.022, 0.029, 0.037, 0.048, 0.064];
    const hd = (h) => ({ h, q: q.map((v) => v * Math.sqrt(h / 5)), expected: 0.002, alpha: 0.001, p_up: 0.53, p_beat: 0.51, p_up_raw: 0.54, p_beat_raw: 0.52, es5: -0.08, sigma: 0.04 });
    const levels = [0.05, 0.1, 0.25, 0.5, 0.75, 0.9, 0.95];
    const fan = Array.from({ length: 20 }, (_, d) => levels.map((l) => (l - 0.5) * 0.12 * Math.sqrt((d + 1) / 20)));
    const dist = { horizons: [hd(5), hd(10), hd(20)], fan, fan_levels: levels, beta: 1.1, vol_pct: 28, normal_vol_pct: 25, earnings: { session: 7, typical_move_pct: 4.2, from_calendar: true }, percentile_20: 80 };
    const pick = { symbol: "AAPL", name: "Apple Inc.", sector: "Information Technology", score: 0.9, percentile: 95, drivers: [{ key: "mom_12_1", label: "12-month momentum", contribution: 0.2 }], dist };
    const rec = { n: 1000, crps: 0.03, crps_raw: 0.031, crps_normal: 0.0305, crps_history: 0.0306, skill_normal_pct: 1.7, skill_history_pct: 1.6, dm_normal_t: 4, dm_history_t: 4.3,
      coverage: [52, 82, 91], calibrated_coverage: [50, 80, 90], calibrated: true, pit: [0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1],
      brier_up: 0.251, brier_up_calibrated: 0.2505, brier_up_base_rate: 0.2497, brier_beat: 0.2501, brier_beat_calibrated: 0.2499, brier_beat_base_rate: 0.2496 };
    const alpha = { days: 100, ic: 0.024, t: 2.3, ridge_ic: 0.02, tree_ic: 0.016, momentum_ic: 0.013, spread_pct: 0.47 };
    const vol = { n: 1000, har: 0.22, garch: 0.27, blend: 0.22, naive: 0.32, weight: 1, skill_pct: 31 };
    const report = { version: 2, at: now, as_of: now, horizon: 5, horizons: [5, 10, 20], symbols: 2, train_rows: 1000, paths: 4000, eval_paths: 400, eval_stocks: 400,
      years: [{ year: 2025, train_rows: 1000, alpha: { 5: alpha, 20: alpha }, vol: { 5: vol, 20: vol }, dist: { 5: rec, 10: rec, 20: rec }, turnover_pct: 50, momentum_turnover_pct: 11 }],
      alpha: { 5: alpha, 20: alpha }, vol: { 5: vol, 20: vol }, dist: { 5: rec, 10: rec, 20: rec }, turnover_pct: 50, momentum_turnover_pct: 11,
      calibration: { slope_bps: { 5: 3, 20: 19 }, slope_t: { 5: 1, 20: 1.6 }, ensemble_weight: { 5: [0.33, 0.33, 0.34], 20: [0.33, 0.33, 0.34] }, har_weight: { 5: 1, 20: 1 }, pit_n: { 5: 1000 } },
      regime: { stress_prob: 0.01, calm_vol_pct: 11, stress_vol_pct: 34, stay_calm: 0.98, stay_stressed: 0.82, vix: 17.5, vol_forecast_pct: { 5: 14, 10: 15, 20: 15.4 }, realised_vol_pct: 10.3 },
      market: [hd(5), hd(10), hd(20)],
      factors: [{ key: "mom_12_1", label: "12-month momentum", why: "Momentum persists.", ridge_weight: 0.03, tree_share_pct: 4 }],
      reliability: { 5: { up: [{ forecast: 0.5, observed: 0.52, n: 400 }], beat: [{ forecast: 0.5, observed: 0.5, n: 400 }, { forecast: 0.55, observed: 0.53, n: 200 }] } } };
    if (path === "/api/forecast") return { report, rows: [pick], total: 1, sort: "p_beat", offset: 0, live: [], live_summary: { runs: 0 } };
    return { symbol: "AAPL", latest: { ...pick, as_of: now }, history: [], horizons: [5, 10, 20], stale: false, market: report.market, regime: report.regime, calibration: report.dist };
  }
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
