// The fleet's "Recently closed" section (closed.js, GET /v1/closed): collapsed
// by default with a count, rows with reasons, and resume through a confirm
// sheet that lands on the new window.
import type { Page } from "@playwright/test";
import { expect, openFleet, test } from "./fixtures";

const section = (page: Page) => page.locator(".fleet section.closed-group");
const toggle = (page: Page) => section(page).getByRole("button", { name: /Recently closed/ });
const sheet = (page: Page) => page.locator(".overlay .sheet");

test.beforeEach(async ({ page, control }) => {
  await control.seedClosed([
    { session_id: "aaaa1111-0000-0000-0000-000000000001", agent: "go-engineer", project: "shop", title: "Fix the parser", ago_minutes: 120, pr_number: 42, pr_url: "https://github.com/o/shop/pull/42" },
    { session_id: "bbbb2222-0000-0000-0000-000000000002", agent: "reviewer", project: "blog", title: "Review the theme", ago_minutes: 300, gone: true },
    { session_id: "", agent: "lead", project: "blog", title: "Old plan", ago_minutes: 600 },
  ]);
  await openFleet(page);
});

test("the section is collapsed with a count, and expands to the rows", async ({ page }) => {
  await expect(toggle(page)).toHaveText("Recently closed (3)");
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
  await expect(section(page).locator(".closed-row")).toHaveCount(0);

  await toggle(page).tap();
  const rows = section(page).locator(".closed-row");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(0)).toContainText("Fix the parser");
  await expect(rows.nth(0)).toContainText("shop · closed 2h ago");
  await expect(rows.nth(0).getByRole("link", { name: "PR #42" })).toHaveAttribute("href", "https://github.com/o/shop/pull/42");
  await expect(rows.nth(1)).toContainText("is gone");
  await expect(rows.nth(2)).toContainText("no session id");
  // Only the resumable row is a button.
  await expect(section(page).getByRole("button", { name: /^Resume / })).toHaveCount(1);
});

test("resume confirms, resumes and opens the new window", async ({ page, control, guard }) => {
  // The fake resume reports a window the fixture fleet does not have.
  guard.allow(/HTTP 404|status of 404/);
  await toggle(page).tap();
  await section(page).getByRole("button", { name: "Resume Fix the parser" }).tap();
  await expect(sheet(page)).toContainText("Resume Fix the parser?");
  await expect(sheet(page)).toContainText("go-engineer in shop");
  await sheet(page).getByRole("button", { name: "Resume" }).tap();
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "resume"));
  const resume = actions.filter((a) => a.kind === "resume");
  expect(resume).toHaveLength(1);
  expect(resume[0]).toMatchObject({ kind: "resume", session: "agentctl-shop", agent: "go-engineer", id: "aaaa1111-0000-0000-0000-000000000001" });
  await expect(page).toHaveURL(/#\/w\/agentctl-shop\/%40902$/);
});

test("cancel resumes nothing", async ({ page, control }) => {
  await toggle(page).tap();
  await section(page).getByRole("button", { name: "Resume Fix the parser" }).tap();
  await sheet(page).getByRole("button", { name: "Cancel" }).tap();
  await expect(sheet(page)).toHaveCount(0);
  expect((await control.actions()).filter((a) => a.kind === "resume")).toEqual([]);
});

test("the open state survives a reload", async ({ page }) => {
  await toggle(page).tap();
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "true");
  await page.evaluate(() => location.reload());
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "true");
  await expect(section(page).locator(".closed-row")).toHaveCount(3);
});
