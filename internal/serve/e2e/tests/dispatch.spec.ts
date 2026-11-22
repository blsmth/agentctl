// The Dispatch screen: attach a screenshot, confirm, dispatch.
import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

const sheet = (page: Page) => page.locator(".overlay .sheet");
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64");

async function openDispatch(page: Page) {
  await page.goto("/#/dispatch");
  await expect(page.locator("select[aria-label=Agent] option").first()).toBeAttached();
  await expect(page.locator("select[aria-label=Project] option").first()).toBeAttached();
}

test("attach a screenshot, confirm, dispatch", async ({ page, control, guard }) => {
  // The stub dispatcher returns a window the fixture fleet does not have.
  guard.allow(/HTTP 404|status of 404/);
  await openDispatch(page);
  await page.getByPlaceholder("What should the agent do?").fill("the header overlaps the status bar");
  await page.locator("input[type=file]").setInputFiles({ name: "IMG_0001.png", mimeType: "image/png", buffer: PNG });
  await expect(page.locator(".thumb img")).toHaveCount(1);
  await page.getByRole("button", { name: "Review & send" }).tap();
  await expect(sheet(page)).toContainText("images: 1");
  await sheet(page).getByRole("button", { name: "Dispatch" }).tap();
  await expect(page.locator("#toasts .toast").first()).toHaveText("Dispatched");
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "dispatch"));
  const d = actions.find((x) => x.kind === "dispatch")!;
  expect(d.text).toMatch(/^the header overlaps the status bar\n\n\[Screenshot: \/.*\/uploads\/dispatch\/[0-9a-f]{32}\.png\]$/);
});

test("cancelling the confirm sheet uploads nothing and dispatches nothing", async ({ page, control }) => {
  await openDispatch(page);
  let uploads = 0;
  page.on("request", (r) => {
    if (r.url().includes("/v1/dispatch")) uploads++;
  });
  await page.getByPlaceholder("What should the agent do?").fill("x");
  await page.locator("input[type=file]").setInputFiles({ name: "a.png", mimeType: "image/png", buffer: PNG });
  await page.getByRole("button", { name: "Review & send" }).tap();
  await sheet(page).getByRole("button", { name: "Cancel" }).tap();
  expect(uploads).toBe(0);
  expect((await control.actions()).filter((a) => a.kind === "dispatch")).toHaveLength(0);
});

test("a screenshot can be removed before dispatching", async ({ page }) => {
  await openDispatch(page);
  await page.locator("input[type=file]").setInputFiles({ name: "a.png", mimeType: "image/png", buffer: PNG });
  await expect(page.locator(".thumb")).toHaveCount(1);
  await page.getByRole("button", { name: "Remove image" }).tap();
  await expect(page.locator(".thumb")).toHaveCount(0);
});

test("additional instructions reach the confirm sheet and the dispatch", async ({ page, control, guard }) => {
  guard.allow(/HTTP 404|status of 404/);
  await openDispatch(page);
  const box = page.getByLabel("Additional instructions");
  await expect(box).toBeHidden(); // collapsed by default
  await page.getByText("Additional instructions", { exact: true }).tap();
  await expect(box).toBeVisible();
  await page.getByPlaceholder("What should the agent do?").fill("fix the header");
  await box.fill("  Keep commits small.  ");
  await page.getByRole("button", { name: "Review & send" }).tap();
  await expect(sheet(page)).toContainText("additional instructions (system prompt):");
  await expect(sheet(page)).toContainText("Keep commits small.");
  await sheet(page).getByRole("button", { name: "Dispatch" }).tap();
  await expect(page.locator("#toasts .toast").first()).toHaveText("Dispatched");
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "dispatch"));
  expect(actions.find((x) => x.kind === "dispatch")!.instructions).toBe("Keep commits small.");
});

test("no instructions typed: the sheet has no instructions block", async ({ page }) => {
  await openDispatch(page);
  await page.getByPlaceholder("What should the agent do?").fill("x");
  await page.getByRole("button", { name: "Review & send" }).tap();
  await expect(sheet(page)).not.toContainText("additional instructions");
});
