// Deterministic UI checks against a local Vite server; never calls production.
import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch({
  headless: true,
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});
try {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 960 },
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  let requests = 0,
    fail = false,
    done = false;
  const turn = {
    id: 1,
    conversation_id: 1,
    seq: 1,
    question: "NVIDIA export restrictions",
    status: "running",
    stage: "searching",
    created_at: new Date().toISOString(),
    elapsed_ms: 0,
    degraded: false,
    progress: [
      {
        at: new Date().toISOString(),
        stage: "searching",
        detail: "Checking official releases",
      },
    ],
  };
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let body = {};
    if (path === "/api/auth/status")
      body = { required: false, authenticated: true };
    else if (path === "/api/meta") body = { display_tz: "UTC", features: {} };
    else if (path === "/api/health") body = { dependencies: [], budgets: {} };
    else if (path === "/api/alerts") body = { alerts: [], unread: 0 };
    else if (path === "/api/research/scrapers")
      body = {
        available: true,
        scrapers: ["collected", "sec_fulltext", "ecb-press"],
      };
    else if (path === "/api/research/conversations")
      body = {
        conversations: requests
          ? [
              {
                id: 1,
                title: turn.question,
                updated_at: turn.created_at,
                turn_count: 1,
              },
            ]
          : [],
      };
    else if (path === "/api/research/ask") {
      requests++;
      const payload = route.request().postDataJSON();
      assert.equal(payload.mode, "evidence");
      if (fail)
        return route.fulfill({
          status: 429,
          json: {
            error: {
              code: "research_busy",
              message: "Research capacity is busy. Try again shortly.",
            },
          },
        });
      body = { conversation_id: 1, turn };
      return route.fulfill({ status: 202, json: body });
    } else if (path === "/api/research/conversations/1") {
      body = {
        id: 1,
        title: turn.question,
        turns: [
          {
            ...turn,
            ...(done
              ? {
                  status: "done",
                  model: "fixture-model",
                  elapsed_ms: 2100,
                  sections: [
                    {
                      heading: "Test brief",
                      body: "Fixture assertion for citation navigation.",
                      sources: [1],
                    },
                  ],
                  sources: [
                    {
                      title: "Fixture official release",
                      url: "https://example.com/release",
                      publisher: "Fixture publisher",
                      trust: 100,
                      words: 180,
                      read_status: "read",
                    },
                  ],
                  providers: [
                    { name: "collected", count: 1 },
                    { name: "offline", count: 0, error: "Unavailable in test" },
                  ],
                }
              : {}),
          },
        ],
      };
    } else if (path === "/api/stream")
      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": test\n\n",
      });
    await route.fulfill({ json: body });
  });
  await page.goto(
    process.env.BELLWETHER_UI_URL || "http://127.0.0.1:5174/research",
  );
  await page.getByRole("heading", { name: "What do you want to find out?" }).waitFor();
  assert.equal(
    await page
      .getByRole("radio", { name: "Evidence only", exact: true })
      .getAttribute("aria-checked"),
    "true",
  );
  await page.screenshot({
    path: "/tmp/bellwether-research-desktop.png",
    fullPage: true,
  });
  await page
    .getByRole("textbox", { name: "Research question" })
    .fill(turn.question);
  await page.getByRole("button", { name: "Start research" }).click();
  await page.getByText("Checking official releases").waitFor();
  assert.equal(
    await page.getByRole("button", { name: "Start research" }).isDisabled(),
    true,
  );
  assert.equal(requests, 1);
  done = true;
  await page.getByRole("heading", { name: "Test brief" }).waitFor();
  assert.equal(
    await page
      .locator("details")
      .filter({ hasText: "Evidence library" })
      .getAttribute("open"),
    null,
  );
  await page.getByTitle("Source 1").click();
  assert.equal(await page.locator("#src-1-1").isVisible(), true);
  await page.getByRole("button", { name: "New question", exact: true }).click();
  fail = true;
  await page
    .getByRole("textbox", { name: "Research question" })
    .fill("Retry question");
  await page.getByRole("button", { name: "Start research" }).click();
  await page
    .getByRole("alert")
    .filter({ hasText: "Research capacity is busy" })
    .waitFor();
  assert.equal(
    await page.getByRole("textbox", { name: "Research question" }).inputValue(),
    "Retry question",
  );
  assert.equal(requests, 2);
  assert.deepEqual(errors, []);

  // A fresh narrow viewport uses the compact navigation and retains the composer.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await page.getByRole("heading", { name: "What do you want to find out?" }).waitFor();
  assert.equal(
    await page.getByRole("textbox", { name: "Research question" }).isVisible(),
    true,
  );
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
    true,
  );
  await page.screenshot({
    path: "/tmp/bellwether-research-mobile.png",
    fullPage: true,
  });
  console.log(
    "Research browser checks passed: evidence default, single submission, progress, citations, errors, draft recovery, mobile layout.",
  );
} finally {
  await browser.close();
}
