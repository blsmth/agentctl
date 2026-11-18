// The fleet screen renders the fixture: groups, the dispatch tree, glyphs.
import { expect, openFleet, row, test } from "./fixtures";

test.beforeEach(async ({ page }) => {
  await openFleet(page);
});

test("groups are ordered by attention and every window is listed", async ({ page }) => {
  // shop has a blocked window, so it comes first; the idle ones follow.
  await expect(page.locator(".fleet section.group h2")).toHaveText(["shop", "blog", "hub"]);
  await expect(page.locator(".fleet section.group").first()).toHaveClass(/\bblocked\b/);
  for (const id of ["@11", "@12", "@13", "@14", "@15", "@31", "@41"]) {
    await expect(row(page, id)).toBeVisible();
  }
});

test("rows show the state glyph and agent", async ({ page }) => {
  const glyphs: Record<string, [string, string]> = {
    "@11": ["○", "idle"],
    "@12": ["✓", "done"],
    "@13": ["●", "working"],
    "@14": ["⏸", "blocked"],
  };
  for (const [id, [glyph, state]] of Object.entries(glyphs)) {
    const r = row(page, id);
    await expect(r).toHaveClass(new RegExp(`\\b${state}\\b`));
    await expect(r.locator(".glyph")).toHaveText(glyph);
    await expect(r.locator(".glyph")).toHaveAttribute("aria-label", state);
  }
  await expect(row(page, "@13").locator(".agent")).toHaveText("test-writer");
  await expect(row(page, "@13").locator(".activity")).toHaveText("go test ./...");
  // Internal identifiers (window ID, launch mode, generated branch) stay off the row.
  // A recap replaces the agent name as the headline; the name becomes a chip.
  await expect(row(page, "@12").locator(".win-title .agent")).toHaveText("Add a checkout retry");
  await expect(row(page, "@12").locator(".win-title .tag")).toHaveText(["#7", "PR #8", "go-engineer"]);
  // The task is already the headline, so the second line is the activity.
  await expect(row(page, "@12").locator(".activity")).toHaveText("opened PR #8");
  await expect(row(page, "@12").locator(".activity.recap")).toHaveCount(0);
  await expect(row(page, "@12").locator(".win-title")).not.toContainText("@12");
  await expect(row(page, "@12").locator(".win-side")).toContainText("42% ctx");
});

test("dispatched windows nest under their parent", async ({ page }) => {
  // @11 dispatched @12 and @13; @12 dispatched @14.
  await expect(row(page, "@11")).not.toHaveClass(/\bchild\b/);
  await expect(row(page, "@12")).toHaveClass(/\bchild\b/);
  await expect(row(page, "@13")).toHaveAttribute("style", /--depth:\s*1/);
  await expect(row(page, "@14")).toHaveAttribute("style", /--depth:\s*2/);
  // The parent's disclosure collapses its subtree, grandchildren included.
  const toggle = row(page, "@11").locator("xpath=..").locator("button.disclose");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await toggle.tap();
  await expect(row(page, "@12")).toHaveCount(0);
  await expect(row(page, "@14")).toHaveCount(0);
  await expect(page.locator(".fleet button.disclose.collapsed .glyph")).toHaveText("⏸");
  await page.locator(".fleet button.disclose.collapsed").first().tap();
  await expect(row(page, "@14")).toBeVisible();
});

test("tapping a row opens its window view", async ({ page }) => {
  await row(page, "@15").tap();
  await expect(page).toHaveURL(/#\/w\/agentctl-shop\/%4015$/);
  await expect(page.locator("header.bar h1")).toHaveText("docs");
});

test("an empty fleet says so", async ({ page, control }) => {
  await control.useFixture("empty");
  await expect(page.locator(".fleet")).toContainText("No agents running.");
});

test("cron windows get a job chip and their own bucket", async ({ page, control }) => {
  const groups = await control.fleet();
  groups[0].windows.push({ id: "@16", index: 6, agent: "triage", state: "working", mode: "adhoc", scheduled_by: "nightly-triage", activity: "triaging" });
  await control.setFleet(groups);
  const r = row(page, "@16");
  await expect(r.locator(".win-title .tag.sched-chip")).toHaveText("⏲ nightly-triage");
  await expect(r.locator(".glyph")).toHaveText("●");
  // Not filed under "Tasks from other sessions": cron has no starting session.
  const shop = page.locator(".fleet section.group").first();
  await expect(shop.locator(".bucket-label")).toHaveText(["Scheduled jobs (1)"]);
  await expect(r).toHaveAttribute("style", /--depth:\s*1/);
  // Other rows carry no job chip.
  await expect(row(page, "@15").locator(".sched-chip")).toHaveCount(0);
});
