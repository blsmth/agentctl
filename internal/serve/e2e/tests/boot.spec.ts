// The app shell loads cleanly and the service worker precaches all of it.
// The boot test is the one that catches a module missing from the server's
// asset table (PR #107: /swipe.js 404ed, fleet.js failed to import, blank app).
import { expect, openFleet, test } from "./fixtures";

/** shellList parses the SHELL precache list and cache name out of sw.js. */
async function shellList(request: import("@playwright/test").APIRequestContext) {
  const res = await request.get("/sw.js");
  expect(res.status()).toBe(200);
  const src = await res.text();
  const list = /const\s+SHELL\s*=\s*\[([\s\S]*?)\]/.exec(src);
  const cache = /const\s+CACHE\s*=\s*["']([^"']+)["']/.exec(src);
  expect(list, "sw.js SHELL list").not.toBeNull();
  expect(cache, "sw.js CACHE name").not.toBeNull();
  const urls = [...list![1].matchAll(/["']([^"']+)["']/g)].map((m) => m[1]);
  expect(urls.length).toBeGreaterThan(5);
  return { urls, cache: cache![1] };
}

test("app boots with no console errors or failed requests", async ({ page, guard }) => {
  const loaded: string[] = [];
  page.on("response", (r) => loaded.push(new URL(r.url()).pathname));
  await page.goto("/");
  const header = page.locator("header.bar h1");
  await header.waitFor({ timeout: 5_000 }).catch(() => {});
  // Report what broke (a 404, an import error) ahead of the symptom (no header).
  expect(guard.unexpected()).toEqual([]);
  await expect(header).toHaveText("Fleet");
  // Every module in the import graph was fetched and evaluated.
  for (const m of ["/app.js", "/fleet.js", "/swipe.js", "/logic.js", "/ui.js", "/api.js", "/live.js"]) {
    expect(loaded, `${m} loaded`).toContain(m);
  }
  await expect(page.locator(".fleet h2", { hasText: "shop" })).toBeVisible();
  await expect(page.locator("#nav-dispatch")).toBeVisible();
  expect(guard.unexpected()).toEqual([]);
});

test("unpaired browser gets the not-paired page, not the app", async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL });
  const page = await ctx.newPage();
  const res = await page.goto("/");
  expect(res?.status()).toBe(401);
  await expect(page.locator("body")).toContainText(/not paired/i);
  await ctx.close();
});

test("every sw.js precache URL is served", async ({ page }) => {
  const { urls } = await shellList(page.request);
  for (const u of urls) {
    const res = await page.request.get(u);
    expect(res.status(), `GET ${u}`).toBe(200);
  }
});

test("service worker installs, activates and precaches the whole shell", async ({ page }) => {
  const { urls, cache } = await shellList(page.request);
  await openFleet(page);
  const state = await page.evaluate(async () => {
    const reg = await navigator.serviceWorker.ready;
    return reg.active?.state;
  });
  expect(state).toBe("activated");
  // install uses Promise.allSettled, so a 404 would not fail it: check the
  // cache itself holds every entry.
  await expect
    .poll(
      () =>
        page.evaluate(async (name) => {
          const c = await caches.open(name);
          return (await c.keys()).map((r) => new URL(r.url).pathname).sort();
        }, cache),
      { timeout: 10_000 },
    )
    .toEqual([...urls].sort());
});
