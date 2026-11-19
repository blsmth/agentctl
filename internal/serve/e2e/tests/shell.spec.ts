// The per-project "+" sheet (fleet.js addButton, ui.js newSheet): Agent keeps
// the Dispatch link, Shell opens a shell window and routes to its terminal.
import type { Page } from "@playwright/test";
import { expect, openFleet, test } from "./fixtures";

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
