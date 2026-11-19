// Swipe-left-to-close on fleet rows (swipe.js), driven by touch pointers.
import { expect, openFleet, row, test } from "./fixtures";
import { drag, lift, move, offsetX } from "./touch";

const SHOP = "agentctl-shop";
const sheet = (page: import("@playwright/test").Page) => page.locator(".overlay .sheet");
/** node is the sliding part of a row (the link's parent). */
const node = (link: import("@playwright/test").Locator) => link.locator("xpath=..");
const wrap = (link: import("@playwright/test").Locator) => link.locator("xpath=../..");

/** settle waits out swipe.js's 200ms snap animation. */
async function settled(link: import("@playwright/test").Locator) {
  await expect.poll(() => offsetX(node(link))).toBe(0);
}

test.beforeEach(async ({ page }) => {
  await openFleet(page);
  // Let the initial re-render land so a drag does not hit a row about to be replaced
  // (there is no DOM signal for it).
  await page.waitForTimeout(500);
});

const closeBtn = (page: import("@playwright/test").Page, link: import("@playwright/test").Locator) =>
  wrap(link).getByRole("button", { name: /^Close / });

test("a long left swipe reveals Close; tapping it closes the window with no sheet", async ({ page, control }) => {
  const link = row(page, "@15");
  await expect(wrap(link)).toHaveClass(/\bswipe\b/);
  await drag(link, { dx: -260, stepMs: 25 });

  // The click after the lift was swallowed: still on the fleet, row held open.
  await expect(sheet(page)).toHaveCount(0);
  expect(new URL(page.url()).hash).toBe("");
  await expect.poll(() => offsetX(node(link))).toBe(-88);
  expect(await control.actions()).toEqual([]);

  await closeBtn(page, link).tap();
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "close"));
  expect(actions).toEqual([{ kind: "close", session: SHOP, window: "@15" }]);
  await expect(sheet(page)).toHaveCount(0);
  await expect(link).toBeHidden();
  await expect(row(page, "@15")).toHaveCount(0);
});

test("tapping the open row snaps it back without navigating or closing", async ({ page, control }) => {
  const link = row(page, "@15");
  await drag(link, { dx: -260, stepMs: 25 });
  await expect.poll(() => offsetX(node(link))).toBe(-88);
  await link.tap();
  await settled(link);
  await expect(wrap(link)).not.toHaveClass(/swiping/);
  expect(new URL(page.url()).hash).toBe("");
  expect(await control.actions()).toEqual([]);
});

test("a short drag snaps back without asking or navigating", async ({ page, control }) => {
  const link = row(page, "@15");
  await drag(link, { dx: -40, steps: 6, stepMs: 40 });
  await settled(link);
  await expect(sheet(page)).toHaveCount(0);
  expect(new URL(page.url()).hash).toBe("");
  expect(await control.actions()).toEqual([]);
});

test("a vertical drag is left to the browser: no slide, no sheet, no navigation", async ({ page, control }) => {
  const link = row(page, "@11");
  // touch-action: pan-y is what lets the browser scroll the list natively.
  await expect(link).toHaveCSS("touch-action", "pan-y");
  // Mostly vertical, with some sideways drift; checked mid-gesture.
  await drag(link, { dx: -30, dy: 120, release: false });
  expect(await offsetX(node(link))).toBe(0);
  await expect(wrap(link)).not.toHaveClass(/swiping/);
  // Once locked to scrolling, a later sideways move does not start a swipe.
  await move(link, -230, 120);
  expect(await offsetX(node(link))).toBe(0);
  await lift(link, -230, 120);
  await settled(link);
  await expect(sheet(page)).toHaveCount(0);
  expect(new URL(page.url()).hash).toBe("");
  expect(await control.actions()).toEqual([]);
});

for (const [id, what] of [
  ["@13", "working"],
  ["@14", "blocked"],
  ["@41", "hub"],
] as const) {
  test(`a ${what} row does not move`, async ({ page, control }) => {
    const link = row(page, id);
    await expect(link).toBeVisible();
    await expect(wrap(link)).not.toHaveClass(/\bswipe\b/);
    await drag(link, { dx: -260, stepMs: 25, release: false });
    expect(await offsetX(node(link))).toBe(0);
    await lift(link, -260);
    await expect(sheet(page)).toHaveCount(0);
    expect(await control.actions()).toEqual([]);
  });
}

test("a 409 from the server toasts and snaps back", async ({ page, control, guard }) => {
  guard.allow(/HTTP 409: POST \/v1\/sessions\/agentctl-shop\/windows\/%4015\/close/);
  guard.allow(/console error: .*409/);
  // The fleet still says idle, but the pane is busy: the server refuses.
  await control.patchWindow(SHOP, "@15", { pane: "working" });
  const link = row(page, "@15");
  await drag(link, { dx: -260, stepMs: 25 });
  await closeBtn(page, link).tap();
  await expect(page.locator("#toasts .toast.error")).toContainText("cannot be closed");
  await settled(link);
  await expect(wrap(link)).not.toHaveCSS("height", "0px");
  await expect(row(page, "@15")).toBeVisible();
  expect(await control.actions()).toEqual([]);
});

test("renders resume after a reveal is closed by tapping the row", async ({ page, control }) => {
  const link = row(page, "@15");
  await drag(link, { dx: -260, stepMs: 25 });
  await expect.poll(() => offsetX(node(link))).toBe(-88);
  // Held open: a change elsewhere must wait.
  await control.patchWindow(SHOP, "@11", { activity: "while open" });
  await page.waitForTimeout(500);
  await expect(row(page, "@11").locator(".activity")).not.toHaveText("while open");
  await link.tap();
  await settled(link);
  await expect(row(page, "@11").locator(".activity")).toHaveText("while open");
  // And later renders still land: the hold was fully released.
  await control.patchWindow(SHOP, "@11", { activity: "after close" });
  await expect(row(page, "@11").locator(".activity")).toHaveText("after close", { timeout: 2000 });
});

test("renders resume after a failed close snaps the row back", async ({ page, control, guard }) => {
  guard.allow(/HTTP 409: POST \/v1\/sessions\/agentctl-shop\/windows\/%4015\/close/);
  guard.allow(/console error: .*409/);
  await control.patchWindow(SHOP, "@15", { pane: "working" });
  const link = row(page, "@15");
  await drag(link, { dx: -260, stepMs: 25 });
  await closeBtn(page, link).tap();
  await expect(page.locator("#toasts .toast.error")).toContainText("cannot be closed");
  await settled(link);
  await control.patchWindow(SHOP, "@11", { activity: "after failure" });
  await expect(row(page, "@11").locator(".activity")).toHaveText("after failure", { timeout: 2000 });
});

test("a double tap on Close sends one close", async ({ page, control }) => {
  // Hold the response so the second tap lands while the first is in flight.
  await page.route(/\/windows\/%4015\/close$/, async (route) => {
    await new Promise((r) => setTimeout(r, 600));
    await route.continue();
  });
  const link = row(page, "@15");
  await drag(link, { dx: -260, stepMs: 25 });
  const btn = closeBtn(page, link);
  await btn.evaluate((el: HTMLElement) => {
    el.click();
    el.click();
  });
  await control.waitForActions((a) => a.some((x) => x.kind === "close"));
  await page.waitForTimeout(800);
  expect((await control.actions()).filter((x) => x.kind === "close")).toHaveLength(1);
});
