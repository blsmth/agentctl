// The window view: pane, state-aware actions, the window's approvals.
import type { Page } from "@playwright/test";
import { expect, openFleet, test } from "./fixtures";

const SHOP = "agentctl-shop";
const sheet = (page: Page) => page.locator(".overlay .sheet");

async function openWindow(page: Page, id: string) {
  await page.goto(`/#/w/${SHOP}/${encodeURIComponent(id)}`);
  await expect(page.locator(".pane")).toBeVisible();
}

test("reply to an idle agent", async ({ page, control }) => {
  await openWindow(page, "@15");
  await expect(page.locator("header.bar h1")).toHaveText("docs");
  await page.locator(".actions textarea").fill("please also update CHANGELOG");
  await page.getByRole("button", { name: "Reply" }).tap();
  await expect(sheet(page)).toContainText("Send reply?");
  await sheet(page).getByRole("button", { name: "Send" }).tap();
  await expect(page.locator("#toasts .toast").first()).toHaveText("Sent");
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toEqual([{ kind: "reply", session: SHOP, window: "@15", pane: "%15", text: "please also update CHANGELOG" }]);
});

test("answer a blocked agent's dialog", async ({ page, control }) => {
  await openWindow(page, "@14");
  await expect(page.locator(".actions pre.dialog")).toContainText("Do you want to proceed?");
  const yes = page.locator(".actions .choices button", { hasText: "1. Yes" });
  await expect(page.locator(".actions .choices button")).toHaveCount(3); // 1, 2, Esc
  await yes.tap();
  await expect(sheet(page)).toContainText('Answer "1"?');
  await sheet(page).getByRole("button", { name: "Send" }).tap();
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toEqual([{ kind: "key", session: SHOP, window: "@14", pane: "%14", key: "1" }]);
  // The fake dismisses the dialog; the refreshed view stops offering answers.
  await expect(page.locator(".actions pre.dialog")).toHaveCount(0);
});

test("interrupt a working agent", async ({ page, control }) => {
  await openWindow(page, "@13");
  await page.getByRole("button", { name: "Interrupt" }).tap();
  await sheet(page).getByRole("button", { name: "Interrupt" }).tap();
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toEqual([{ kind: "key", session: SHOP, window: "@13", pane: "%13", key: "Escape" }]);
});

test("a pending approval shows on the fleet and in its window, and approving answers it", async ({ page, control }) => {
  await openFleet(page);
  const id = await control.openApproval({
    session: SHOP, window_id: "@14", asking_agent: "reviewer", command: "rm -rf build/",
  });
  const banner = page.locator("a.approval-banner");
  await expect(banner).toHaveText("1 approval waiting");
  await expect(banner).toHaveAttribute("href", `#/approvals/${id}`);
  await expect(page.locator("#nav-approvals-badge")).toHaveText("1");

  await openWindow(page, "@14");
  const card = page.locator(".window-approvals section.approval");
  await expect(card).toContainText("rm -rf build/");
  await expect(card).toContainText("reviewer @14");
  await card.getByRole("button", { name: "Approve…" }).tap();
  await expect(sheet(page)).toContainText("rm -rf build/");
  await sheet(page).locator(".row button").last().tap();
  await expect(page.locator("#toasts .toast").first()).toHaveText("Approved");
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "approval"));
  expect(actions).toEqual([{ kind: "approval", session: SHOP, window: "@14", id, decision: "allow", device: "e2e-phone" }]);
  await expect(card).toHaveCount(0);
});

test("deny sends a deny decision", async ({ page, control }) => {
  const id = await control.openApproval({ session: SHOP, window_id: "@14", asking_agent: "reviewer", command: "git push --force" });
  await openWindow(page, "@14");
  await page.locator(".window-approvals").getByRole("button", { name: "Deny" }).tap();
  await sheet(page).locator(".row button").last().tap();
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "approval"));
  expect(actions[0]).toMatchObject({ kind: "approval", id, decision: "deny" });
});
