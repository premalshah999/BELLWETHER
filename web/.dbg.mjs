import { chromium } from "playwright";
const browser = await chromium.launch({ args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const page = await browser.newPage({ viewport: { width: 1440, height: 960 } });
await page.route("**/api/**", (route) => {
  const p = new URL(route.request().url()).pathname;
  if (p === "/api/stream") return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": x\n\n" });
  const b = p === "/api/auth/status" ? { required: false, authenticated: true } : p === "/api/health" ? { providers: [], degraded_providers: [], budgets: {} } : p === "/api/events" ? { events: [] } : {};
  return route.fulfill({ status: 200, json: b });
});
await page.goto("http://127.0.0.1:5174/news");
await page.waitForTimeout(2500);
const btn = page.getByRole("button", { name: /All systems normal/ });
console.log("status buttons:", await btn.count());
await btn.first().click();
await page.waitForTimeout(500);
console.log("radios:", await page.getByRole("radio").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label") || e.textContent)));
await browser.close();
