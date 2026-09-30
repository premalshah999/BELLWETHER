import { chromium } from "playwright";
import fs from "node:fs";
const key = fs.readFileSync(process.env.KEYFILE, "utf8").trim();
const OUT = process.env.OUT;
const BASE = "http://127.0.0.1:8080";
const routes = ["/dashboard","/news","/news?q=nvidia","/charts","/scanner/signals","/scanner/screens","/research","/calendar","/geopolitics","/smartmoney/overview","/smartmoney/insiders","/smartmoney/congress","/positions","/journal","/alerts","/algorithms/build","/algorithms/backtest","/eventstudy","/sources","/ai"];
const only = process.env.ONLY ? process.env.ONLY.split(",") : routes;
let failures = 0;
// Sign in once and reuse the session: the login route is throttled.
const state = `${OUT}/state.json`;
{
  const b = await chromium.launch({ args: ["--disable-dev-shm-usage", "--no-sandbox"] });
  const c = await b.newContext();
  for (let i = 0; i < 12; i++) {
    const res = await c.request.post(BASE + "/api/auth/login", { data: { key } });
    if (res.status() === 200) break;
    await new Promise((ok) => setTimeout(ok, 20000));
  }
  await c.storageState({ path: state });
  await b.close();
}
for (const r of only) {
  const browser = await chromium.launch({ args: ["--disable-dev-shm-usage", "--no-sandbox"] });
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: "dark", storageState: state });
  const page = await ctx.newPage();
  const errs = [], bad = [];
  page.on("console", (m) => { if (m.type() === "error") errs.push(m.text().slice(0, 200)); });
  page.on("pageerror", (e) => errs.push("PAGEERR " + e.message.slice(0, 200)));
  page.on("response", (res) => { const u = res.url(); if (u.includes("/api/") && res.status() >= 400) bad.push(res.status() + " " + u.replace(BASE, "").slice(0, 80)); });
  const t0 = Date.now();
  await page.goto(BASE + r, { waitUntil: "domcontentloaded", timeout: 30000 }).catch((e) => errs.push("NAV " + e.message.slice(0, 80)));
  await page.waitForFunction(() => document.body.innerText.length > 200, null, { timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(r.startsWith("/charts") || r.includes("q=") ? 6000 : 2500);
  const m = await page.evaluate(() => {
    const t = document.body.innerText;
    return {
      text: t.length,
      india: (t.match(/\b(NSE|BSE|NIFTY|Sensex|IST)\b|₹|\.NSE|\.BSE|Reliance/g) || []).slice(0, 5),
      zone: (t.match(/\b\d{2}:\d{2} (ET|UTC|[A-Z]{2,4}|GMT[+-][\d:]+)\b/g) || []).slice(0, 2),
      canvas: document.querySelectorAll("canvas").length,
      web: [...document.querySelectorAll("section[aria-label]")].map((s) => s.getAttribute("aria-label") + ":" + s.querySelectorAll("li").length),
      hscroll: document.documentElement.scrollWidth > innerWidth + 1,
    };
  });
  await page.screenshot({ path: `${OUT}/${r.replace(/[/?=]/g, "_")}.png` });
  const problem = errs.length || bad.length || m.india.length || m.text < 200 || m.hscroll;
  if (problem) failures++;
  console.log(`${problem ? "FAIL" : "ok  "} ${r.padEnd(24)} ${String(Date.now() - t0).padStart(5)}ms text=${m.text} canvas=${m.canvas} zone=${JSON.stringify(m.zone)} web=${JSON.stringify(m.web)} ${m.india.length ? "INDIA:" + m.india.join(",") : ""} ${bad.length ? "BAD:" + bad.join(",") : ""} ${errs.length ? "ERR:" + errs.slice(0, 3).join(" | ") : ""}`);
  await browser.close();
}
console.log(failures ? `${failures} page(s) with problems` : "all pages clean");
