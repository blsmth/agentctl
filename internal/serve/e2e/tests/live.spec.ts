// Live updates over SSE: fixture edits reach the open page the way windowstate
// changes do (the fleet bus re-reads the source and publishes the diff).
import { expect, openFleet, row, test } from "./fixtures";
import { drag, lift, offsetX } from "./touch";

const SHOP = "agentctl-shop";

test.beforeEach(async ({ page }) => {
  await openFleet(page);
});

test("a window update re-renders its row", async ({ page, control }) => {
  await control.patchWindow(SHOP, "@11", { activity: "reviewing PR #8" });
  await expect(row(page, "@11").locator(".activity")).toHaveText("reviewing PR #8");
});

test("a short title becomes the row headline over the full recap", async ({ page, control }) => {
  const recap = "Moved the photo library into albums by year and started on the 2019 duplicates.";
  await control.patchWindow(SHOP, "@15", { recap, recap_source: "summary" });
  await expect(row(page, "@15").locator(".win-title .agent")).toHaveText("Moved the photo library into albums by year and started on the 2019 duplicates");
  await control.patchWindow(SHOP, "@15", { short_title: "Organizing your Photos" });
  await expect(row(page, "@15").locator(".win-title .agent")).toHaveText("Organizing your Photos");
  await expect(row(page, "@15").locator(".activity.recap")).toHaveText(recap);
});

test("a sub-agent row shows its role pill before the title and a bare row never does", async ({ page, control }) => {
  await control.patchWindow(SHOP, "@15", { recap: "Did a thing.", recap_source: "summary", short_title: "Fixing Hub Sort Order", short_kind: "go" });
  await expect(row(page, "@15").locator(".win-title .agent")).toHaveText("Fixing Hub Sort Order");
  await expect(row(page, "@15").locator(".win-title .tag.kind-chip")).toHaveCount(0);
  await control.patchWindow(SHOP, "@13", { recap: "Did a thing.", recap_source: "summary", short_title: "Checking Retry Backoff", short_kind: "debug" });
  await expect(row(page, "@13").locator(".win-title .tag.kind-chip")).toHaveText("fix");
  await expect(row(page, "@13").locator(".win-title .agent")).toHaveText("Checking Retry Backoff");
});

test("a state change moves the row and makes it swipeable", async ({ page, control }) => {
  const r = row(page, "@13");
  await expect(r.locator("xpath=../..")).not.toHaveClass(/\bswipe\b/);
  await control.patchWindow(SHOP, "@13", { state: "idle", done: true, pane: "idle" });
  await expect(r).toHaveClass(/\bdone\b/);
  await expect(r.locator(".glyph")).toHaveText("✓");
  await expect(r.locator("xpath=../..")).toHaveClass(/\bswipe\b/);
});

test("a window that goes away leaves the list", async ({ page, control }) => {
  const groups = await control.fleet();
  groups[1].windows = []; // blog's only window exits
  await control.setFleet(groups);
  await expect(row(page, "@31")).toHaveCount(0);
});

test("a new window appears", async ({ page, control }) => {
  const groups = await control.fleet();
  groups[1].windows.push({ id: "@32", index: 2, agent: "editor", state: "working", activity: "proofreading" });
  await control.setFleet(groups);
  await expect(row(page, "@32").locator(".agent")).toHaveText("editor");
});

test("a render arriving mid-swipe waits until the gesture ends", async ({ page, control }) => {
  const dragged = row(page, "@15");
  await drag(dragged, { dx: -90, stepMs: 25, release: false });
  const held = await offsetX(dragged.locator("xpath=.."));
  expect(held).toBeLessThan(-60);

  await control.patchWindow(SHOP, "@11", { activity: "changed mid-swipe" });
  // Long enough for several bus refreshes and the SSE event to land.
  await page.waitForTimeout(800);
  await expect(row(page, "@11").locator(".activity")).toHaveText("waiting for review");
  // The dragged row was not rebuilt under the finger.
  expect(await offsetX(dragged.locator("xpath=.."))).toBe(held);
  expect(await dragged.evaluate((el) => el.isConnected)).toBe(true);

  await lift(dragged, -90); // under the threshold: snaps back, gesture over
  await expect(row(page, "@11").locator(".activity")).toHaveText("changed mid-swipe");
  await expect.poll(() => offsetX(row(page, "@15").locator("xpath=.."))).toBe(0);
  expect(await control.actions()).toEqual([]);
});
