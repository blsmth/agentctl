// A session idle at the prompt while its sub-agents work shows as "waiting".
import { expect, openFleet, row, test, type FixtureGroup } from "./fixtures";

const SHOP = "agentctl-shop";

const fleet = (): FixtureGroup[] => [
  {
    session: SHOP, project: "shop", dir: "/fixture/shop",
    windows: [
      { id: "@1", index: 1, agent: "orchestrator", state: "idle", activity: "dispatched two workers" },
      { id: "@2", index: 2, agent: "go-engineer", state: "working", mode: "dispatch", parent: "@1", activity: "go test ./...", recap: "Add retry", recap_source: "title", short_title: "Add retry" },
      { id: "@3", index: 3, agent: "reviewer", state: "blocked", mode: "dispatch", parent: "@1", pane: "dialog", activity: "needs approval" },
      { id: "@4", index: 4, agent: "docs", state: "idle", done: true, mode: "dispatch", parent: "@1", activity: "finished" },
      { id: "@5", index: 5, agent: "solo", state: "idle", activity: "idle" },
    ],
  },
  {
    // A child in another session than its parent still counts.
    session: "agentctl-blog", project: "blog", dir: "/fixture/blog",
    windows: [{ id: "@6", index: 1, agent: "writer", state: "working", mode: "dispatch", parent: "@5", activity: "drafting" }],
  },
];

test("a parent with working or blocked children shows waiting", async ({ page, control }) => {
  await control.setFleet(fleet());
  await openFleet(page);
  const parent = row(page, "@1");
  await expect(parent).toHaveClass(/\bwaiting\b/);
  await expect(parent.locator(".glyph")).toHaveText("◷");
  await expect(parent.locator(".win-side .waiting-on")).toHaveText("waiting on 2 agents");
  // The children keep their own states; the done child does not count.
  await expect(row(page, "@3")).toHaveClass(/\bblocked\b/);
  await expect(row(page, "@2")).toHaveClass(/\bworking\b/);
  await expect(row(page, "@4")).toHaveClass(/\bdone\b/);
  // Closing it would close its children, so the row has no swipe action.
  await expect(parent.locator("xpath=../..")).not.toHaveClass(/\bswipe\b/);
  await expect(parent.locator("xpath=../..").getByRole("button", { name: /^Close / })).toHaveCount(0);
  // The finished child still swipes, so the missing action is specific to the waiting row.
  await expect(row(page, "@4").locator("xpath=../..")).toHaveClass(/\bswipe\b/);
  // Cross-session child.
  await expect(row(page, "@5").locator(".waiting-on")).toHaveText("waiting on 1 agent");
  await page.setViewportSize({ width: 390, height: 800 });
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.screenshot({ path: `/tmp/phone-waiting/fleet-${scheme}.png` });
  }
});

test("the detail screen lists what it waits on and flips back to idle", async ({ page, control }) => {
  await control.setFleet(fleet());
  await page.goto(`/#/w/${SHOP}/${encodeURIComponent("@1")}`);
  await expect(page.locator(".pane")).toBeVisible();
  const pill = page.locator(".state-pill");
  await expect(pill).toHaveClass(/\bwaiting\b/);
  await expect(pill.locator(".state-text")).toContainText("waiting on 2 agents");
  const links = page.locator(".win-waiting a.waiting-child");
  await expect(links).toHaveCount(2);
  await expect(links.nth(0)).toContainText("Add retry");
  await expect(links.nth(1).locator(".glyph")).toHaveText("⏸");
  // Still idle at the prompt: the reply composer stays.
  await expect(page.locator(".actions textarea")).toBeVisible();
  await page.setViewportSize({ width: 390, height: 800 });
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.screenshot({ path: `/tmp/phone-waiting/detail-${scheme}.png` });
  }
  // The last live child finishes: back to plain idle.
  await control.patchWindow(SHOP, "@2", { state: "idle", done: true });
  await control.patchWindow(SHOP, "@3", { state: "idle", done: true });
  await expect(pill).toHaveClass(/\bidle\b/);
  await expect(page.locator(".win-waiting")).toBeHidden();
});

test("a child link opens the child's window", async ({ page, control }) => {
  await control.setFleet(fleet());
  await page.goto(`/#/w/${SHOP}/${encodeURIComponent("@1")}`);
  await page.locator(".win-waiting a.waiting-child").first().tap();
  await expect(page).toHaveURL(/%402$/);
  // The generated title heads the screen; the agent name moves to the where line.
  await expect(page.locator("header.bar h1")).toHaveText("Add retry");
  await expect(page.locator(".win-where")).toContainText("go-engineer");
});
