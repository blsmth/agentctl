// Claude Code's folder-trust dialog, answered from the phone: the card on a
// trust-blocked fleet row and on its window screen. The pane is a real
// capture (cursor on "No, exit"); the fake redraws it per key as Claude Code
// does (arrows move the cursor, Enter answers the row under it).
import type { Page } from "@playwright/test";
import { expect, openFleet, test, type Control } from "./fixtures";

const SESSION = "agentctl-weather";
const FOLDER = "/private/tmp/trusttest.R15FAO";
const sheet = (page: Page) => page.locator(".overlay .sheet");

// The server remembers an answered dialog (by window and hash) for a few
// seconds across fixture resets, so each test answers its own window.
async function seed(control: Control, page: Page, id: string) {
  await page.setViewportSize({ width: 360, height: 740 });
  await control.setFleet([
    {
      session: SESSION,
      project: "weather",
      dir: "/tmp/weather",
      windows: [
        { id, index: 1, agent: "typescript-engineer", state: "blocked", mode: "adhoc", pane: "trust", pending_tool: "trust", pending_detail: FOLDER } as never,
        { id: "@31", index: 2, agent: "go-engineer", state: "idle" },
      ],
    },
  ]);
}

async function expectCard(page: Page, scope = page.locator("body")) {
  const card = scope.locator(".trust-card");
  await expect(card).toBeVisible();
  await expect(card.locator(".question")).toHaveText("Trust this folder?");
  await expect(card.locator(".trust-folder")).toHaveText(FOLDER);
  await expect(card).toContainText("Claude Code can read, edit and run files here, and loads its .claude settings");
  await expect(card.locator("button")).toHaveText(["Trust folder", "Exit session"]);
  // The path wraps and is never cut off at 360px.
  const fits = await card.locator(".trust-folder").evaluate((el) => el.scrollWidth <= el.clientWidth);
  expect(fits).toBe(true);
  const box = await card.boundingBox();
  expect(box!.x + box!.width).toBeLessThanOrEqual(360);
  return card;
}

test("window screen: Trust folder moves the cursor, then Enter", async ({ page, control }) => {
  await seed(control, page, "@30");
  await page.goto(`/#/w/${SESSION}/${encodeURIComponent("@30")}`);
  const card = await expectCard(page);
  await expect(page.getByRole("button", { name: "Dismiss (Esc)" })).toHaveCount(0);
  await card.getByRole("button", { name: "Trust folder" }).tap();
  await expect(sheet(page)).toHaveCount(0);
  const actions = await control.waitForActions((a) => a.some((x) => x.key === "Enter"));
  expect(actions.map((a) => a.key)).toEqual(["Down", "Enter"]);
  await expect(page.locator(".trust-card")).toHaveCount(0);
});

test("window screen: Exit session asks first, then sends Enter on No", async ({ page, control }) => {
  await seed(control, page, "@32");
  await page.goto(`/#/w/${SESSION}/${encodeURIComponent("@32")}`);
  const card = await expectCard(page);
  await card.getByRole("button", { name: "Exit session" }).tap();
  await expect(sheet(page)).toContainText("Exit this session?");
  await sheet(page).getByRole("button", { name: "Cancel" }).tap();
  await expect(sheet(page)).toHaveCount(0);
  expect(await control.actions()).toEqual([]);

  await card.getByRole("button", { name: "Exit session" }).tap();
  await sheet(page).getByRole("button", { name: "Exit session" }).tap();
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions.map((a) => a.key)).toEqual(["Enter"]);
  // Claude Code exited: the dialog left in the pane is not answerable.
  await expect(page.locator(".trust-card")).toHaveCount(0);
});

test("fleet row: the card answers without opening the window", async ({ page, control }) => {
  await seed(control, page, "@33");
  await openFleet(page);
  const row = page.locator(".trust-row");
  await expect(row).toHaveCount(1);
  const card = await expectCard(page, row);
  await card.getByRole("button", { name: "Trust folder" }).tap();
  const actions = await control.waitForActions((a) => a.some((x) => x.key === "Enter"));
  expect(actions).toEqual([
    { kind: "key", session: SESSION, window: "@33", pane: "%33", key: "Down" },
    { kind: "key", session: SESSION, window: "@33", pane: "%33", key: "Enter" },
  ]);
  // Still on the fleet list.
  expect(page.url()).not.toContain("/w/");
});

test("a long worktree path wraps inside the card at 360px", async ({ page, control }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  const long = "/Users/someone/code/personal/a-rather-long-project-name/.worktrees/agent-typescript-engineer-adhoc-03de1d67-and-more";
  await control.setFleet([
    { session: SESSION, project: "weather", dir: "/tmp/weather", windows: [{ id: "@34", index: 1, agent: "typescript-engineer", state: "blocked", pane: "trust", pending_tool: "trust", pending_detail: long }] },
  ]);
  await openFleet(page);
  const folder = page.locator(".trust-row .trust-folder");
  await expect(folder).toHaveText(long);
  expect(await folder.evaluate((el) => el.scrollWidth <= el.clientWidth && el.getBoundingClientRect().right <= 360)).toBe(true);
  expect((await folder.boundingBox())!.height).toBeGreaterThan(30); // wrapped onto more than one line
});
