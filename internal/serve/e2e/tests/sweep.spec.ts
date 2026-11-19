// The per-project sweep button on group headers (fleet.js sweepButton,
// logic.js runSweep): preview, confirm sheet, apply.
import type { Page } from "@playwright/test";
import { expect, openFleet, row, test } from "./fixtures";

const SHOP = "agentctl-shop";
const sheet = (page: Page) => page.locator(".overlay .sheet");
const sweepButton = (page: Page, project: string) => page.getByRole("button", { name: `Sweep ${project}` });

test.beforeEach(async ({ page }) => {
  await openFleet(page);
});

test("project groups get a sweep button; the hub group does not", async ({ page }) => {
  await expect(sweepButton(page, "shop")).toBeVisible();
  await expect(sweepButton(page, "blog")).toBeVisible();
  await expect(sweepButton(page, "hub")).toHaveCount(0);
});

test("sweep previews the plan, and confirm closes the finished windows", async ({ page, control }) => {
  await sweepButton(page, "shop").tap();
  await expect(sheet(page)).toBeVisible();
  await expect(sheet(page)).toContainText("Sweep shop?");
  await expect(sheet(page)).toContainText("Close 1 window:");
  await expect(sheet(page)).toContainText("go-engineer");
  await expect(sheet(page)).toContainText("2 need attention, 1 in progress"); // @14 (Bash dialog) and @17 (AskUserQuestion)
  // The preview changed nothing.
  expect((await control.actions()).filter((a) => a.kind === "sweep")).toEqual([]);

  await sheet(page).getByRole("button", { name: "Close 1" }).tap();
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "sweep"));
  expect(actions.filter((a) => a.kind === "sweep")).toEqual([{ kind: "sweep", session: SHOP, text: "@12" }]);
  await expect(page.locator(".toast")).toContainText("Swept 1");
  await expect(row(page, "@12")).toHaveCount(0);
  await expect(row(page, "@14")).toHaveCount(1);
});

test("cancel leaves everything in place", async ({ page, control }) => {
  await sweepButton(page, "shop").tap();
  await expect(sheet(page)).toBeVisible();
  await sheet(page).getByRole("button", { name: "Cancel" }).tap();
  await expect(sheet(page)).toHaveCount(0);
  await expect(row(page, "@12")).toHaveCount(1);
  expect((await control.actions()).filter((a) => a.kind === "sweep")).toEqual([]);
  // The button is usable again.
  await expect(sweepButton(page, "shop")).toBeEnabled();
});

test("nothing to close shows a toast and no confirm", async ({ page, control }) => {
  await sweepButton(page, "blog").tap();
  await expect(page.locator(".toast")).toContainText("Nothing to sweep");
  await expect(sheet(page)).toHaveCount(0);
  expect((await control.actions()).filter((a) => a.kind === "sweep")).toEqual([]);
});
