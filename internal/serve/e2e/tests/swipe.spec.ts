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
});

test("a long left swipe on an idle row asks to close; confirm closes it", async ({ page, control }) => {
  const link = row(page, "@15");
  await expect(wrap(link)).toHaveClass(/\bswipe\b/);
  await drag(link, { dx: -260, stepMs: 25 });

  await expect(sheet(page)).toBeVisible();
  await expect(sheet(page)).toContainText("Close window?");
  await expect(sheet(page)).toContainText("docs");
  // The click after the lift was swallowed: still on the fleet, row held open.
  expect(new URL(page.url()).hash).toBe("");
  expect(await offsetX(node(link))).toBeLessThan(-50);

  await sheet(page).getByRole("button", { name: "Close" }).tap();
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "close"));
  expect(actions).toEqual([{ kind: "close", session: SHOP, window: "@15" }]);
  // The row collapses (height animates to 0, or the next render hides it as
  // closed) before the fleet reports the window gone and the row is dropped.
  await expect(link).toBeHidden();
  await expect(row(page, "@15")).toHaveCount(0);
});

test("cancel in the confirm sheet snaps the row back and sends nothing", async ({ page, control }) => {
  const link = row(page, "@15");
  await drag(link, { dx: -260, stepMs: 25 });
  await expect(sheet(page)).toBeVisible();
  await sheet(page).getByRole("button", { name: "Cancel" }).tap();
  await expect(sheet(page)).toHaveCount(0);
  await settled(link);
  await expect(wrap(link)).not.toHaveClass(/swiping/);
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
  await sheet(page).getByRole("button", { name: "Close" }).tap();
  await expect(page.locator("#toasts .toast.error")).toContainText("cannot be closed");
  await settled(link);
  await expect(wrap(link)).not.toHaveCSS("height", "0px");
  await expect(row(page, "@15")).toBeVisible();
  expect(await control.actions()).toEqual([]);
});
