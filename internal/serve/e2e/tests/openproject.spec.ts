// The fleet's "Open project" sheet (openproject.js): recent project dirs from
// GET /v1/projects/recent plus a path field; opening starts the project's
// session (POST /v1/projects/open) and focuses its group.
import type { Page } from "@playwright/test";
import { expect, openFleet, test } from "./fixtures";

const sheet = (page: Page) => page.locator(".overlay .sheet.open-sheet");
const openButton = (page: Page) => page.locator("header.bar").getByRole("button", { name: "Open project" });
const group = (page: Page, session: string) => page.locator(`.fleet section.group[data-session="${session}"]`);

test("recent projects open and the new group is focused", async ({ page, control }) => {
  await control.seedClosed([
    { session_id: "aaaa1111-0000-0000-0000-000000000001", agent: "go-engineer", project: "notes", title: "t", ago_minutes: 60 },
    { session_id: "bbbb2222-0000-0000-0000-000000000002", agent: "go-engineer", project: "gone", title: "t", ago_minutes: 90, gone: true },
  ]);
  await openFleet(page);
  await openButton(page).tap();
  await expect(sheet(page)).toBeVisible();
  const recent = sheet(page).locator(".open-dir");
  // The gone project is not offered.
  await expect(recent).toHaveCount(1);
  await expect(recent.first()).toContainText("closed-projects/notes");
  await recent.first().tap();
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "open"));
  const open = actions.filter((a) => a.kind === "open");
  expect(open).toHaveLength(1);
  expect(open[0].session).toBe("agentctl-notes");
  expect(open[0].dir).toMatch(/closed-projects\/notes$/);
  await expect(sheet(page)).toHaveCount(0);
  await expect(page.locator(".toast")).toContainText("Started agentctl-notes");
  await expect(group(page, "agentctl-notes")).toBeVisible();
  await expect(group(page, "agentctl-notes")).toHaveClass(/just-opened/);
});

test("a running project is only focused", async ({ page, control }) => {
  // The fixture already runs agentctl-blog; its closed dir is not a live group's dir.
  await control.seedClosed([{ session_id: "aaaa1111-0000-0000-0000-000000000001", agent: "writer", project: "blog", title: "t", ago_minutes: 60 }]);
  await openFleet(page);
  await openButton(page).tap();
  await sheet(page).locator(".open-dir").first().tap();
  await expect(page.locator(".toast")).toContainText("agentctl-blog is already running");
  await expect(group(page, "agentctl-blog")).toHaveClass(/just-opened/);
  const groups = (await control.fleet()).map((g) => g.session);
  expect(groups.filter((s) => s === "agentctl-blog")).toHaveLength(1);
});

test("no recent dirs shows just the path field; bad paths are refused inline", async ({ page, control, guard }) => {
  guard.allow(/HTTP 400|status of 400/);
  await openFleet(page);
  await openButton(page).tap();
  const input = sheet(page).getByRole("textbox", { name: "Project directory" });
  await expect(input).toBeVisible();
  await expect(sheet(page).locator(".open-recent")).toBeHidden();
  // Client-side: relative paths never reach the server.
  await input.fill("code/app");
  await sheet(page).getByRole("button", { name: "Open", exact: true }).tap();
  await expect(sheet(page).locator(".sheet-error")).toContainText("absolute path");
  // Server-side: a missing directory is refused with the server's message.
  await input.fill("/definitely/not/here");
  await input.press("Enter");
  await expect(sheet(page).locator(".sheet-error")).toContainText("/definitely/not/here does not exist");
  await expect(sheet(page)).toBeVisible();
  expect((await control.actions()).filter((a) => a.kind === "open")).toEqual([]);
  await sheet(page).getByRole("button", { name: "Cancel" }).tap();
  await expect(sheet(page)).toHaveCount(0);
});

test("typing filters the recent list", async ({ page, control }) => {
  await control.seedClosed([
    { session_id: "aaaa1111-0000-0000-0000-000000000001", agent: "a", project: "alpha", title: "t", ago_minutes: 60 },
    { session_id: "bbbb2222-0000-0000-0000-000000000002", agent: "a", project: "beta", title: "t", ago_minutes: 70 },
  ]);
  await openFleet(page);
  await openButton(page).tap();
  await expect(sheet(page).locator(".open-dir")).toHaveCount(2);
  await sheet(page).getByRole("textbox", { name: "Project directory" }).fill("BET");
  await expect(sheet(page).locator(".open-dir")).toHaveCount(1);
  await expect(sheet(page).locator(".open-dir")).toContainText("beta");
});

test("read-only devices get no open action", async ({ page }) => {
  await page.route("**/healthz", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.features = body.features.filter((f: string) => f !== "write");
    await route.fulfill({ response: res, json: body });
  });
  await openFleet(page);
  await expect(openButton(page)).toHaveCount(0);
});

test("the header fits a 360px screen", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await openFleet(page);
  const bar = await page.locator("header.bar").boundingBox();
  for (const name of ["Open project", "Settings"]) {
    const el = page.locator("header.bar").getByRole(name === "Settings" ? "link" : "button", { name });
    const box = await el.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x + box!.width).toBeLessThanOrEqual(bar!.x + bar!.width);
  }
  await openButton(page).tap();
  const sheetBox = await sheet(page).boundingBox();
  expect(sheetBox!.width).toBeLessThanOrEqual(360);
});
