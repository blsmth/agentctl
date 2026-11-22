// The per-project "+" sheet (fleet.js addButton, ui.js newSheet): Agent keeps
// the Dispatch link, Shell opens a shell window and routes to its terminal.
import type { Page } from "@playwright/test";
import { expect, openFleet, test } from "./fixtures";
import { drag } from "./touch";

const sheet = (page: Page) => page.locator(".overlay .sheet");
const plus = (page: Page) => page.getByRole("button", { name: "New in shop" });

test.beforeEach(async ({ page }) => {
  await openFleet(page);
});

test("Agent in the + sheet opens Dispatch for the project", async ({ page }) => {
  await plus(page).tap();
  await expect(sheet(page)).toContainText("New in shop");
  await sheet(page).getByRole("link", { name: "Agent" }).tap();
  await expect(page).toHaveURL(/#\/dispatch\/%2Ffixture%2Fshop$/);
});

test("Shell in the + sheet opens a shell and routes to its terminal", async ({ page, control, guard }) => {
  // The fake terminal backend cannot create a viewer, so the socket closes.
  guard.allow(/WebSocket|terminal|socket/i);
  await plus(page).tap();
  await sheet(page).getByRole("button", { name: "Shell" }).tap();
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "shell"));
  const a = actions.find((x) => x.kind === "shell")!;
  expect(a.session).toBe("agentctl-shop");
  expect(a.dir).toBe("/fixture/shop");
  await expect(page).toHaveURL(/#\/t\/agentctl-shop\/%40901$/);
});

// Shell rows: the group's shells array renders compact "$ shell" rows below
// the agents (fleet.js shellRow).
const shellRow = (page: Page) => page.locator(".fleet a.win.shell-row");

test("a shell shows as a muted row, is not counted, opens its terminal and closes at once with no confirm", async ({ page, control, guard }) => {
  guard.allow(/WebSocket|terminal|socket/i);
  const groups = await control.fleet();
  const shop = groups.find((g) => g.session === "agentctl-shop")!;
  const agents = shop.windows.length;
  shop.shells = [
    { id: "@801", index: 8, dir: "/fixture/shop" },
    { id: "@802", index: 9, dir: "/fixture/shop/tools" },
  ];
  await control.setFleet([...groups, { session: "agentctl-solo", project: "solo", dir: "/fixture/solo", windows: [], shells: [{ id: "@803", index: 1, dir: "/fixture/solo" }] }]);
  await expect(shellRow(page)).toHaveCount(3);
  await expect(shellRow(page).nth(0)).toHaveText("$ shell");
  await expect(shellRow(page).nth(1)).toHaveText("$ shell · tools");
  await expect(shellRow(page).locator(".glyph")).toHaveCount(0);
  // Only agents are rows with a state.
  await expect(page.locator(".fleet section.group", { hasText: "shop" }).locator("a.win:not(.shell-row)")).toHaveCount(agents);

  await shellRow(page).nth(0).tap();
  await expect(page).toHaveURL(/#\/t\/agentctl-shop\/%40801$/);
  await page.goBack();

  const link = shellRow(page).nth(1);
  const wrap = link.locator("xpath=../..");
  await expect(wrap).toHaveClass(/\bswipe\b/);
  await drag(link, { dx: -260, stepMs: 25 });
  await wrap.getByRole("button", { name: /^Close / }).tap();
  await expect(page.locator(".overlay .sheet")).toHaveCount(0);
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "close"));
  expect(actions).toEqual([{ kind: "close", session: "agentctl-shop", window: "@802" }]);
  await expect(shellRow(page)).toHaveCount(2);
});
