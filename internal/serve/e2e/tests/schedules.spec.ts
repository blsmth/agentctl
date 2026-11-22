// The Schedules screen: list, create via the picker, edit, pause/resume, run now.
import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";
import { drag, offsetX } from "./touch";

const sheet = (page: Page) => page.locator(".overlay .sheet");
const toast = (page: Page) => page.locator("#toasts .toast");
const job = (name: string, extra: Record<string, unknown> = {}) => ({
  name, schedule: "30 7 * * 1-5", dir: "{dir}", agent: "go-engineer", prompt: `Do ${name}.`, harness: "claude", mode: "window", overlap: "skip", catchup: "latest", ...extra,
});
const ago = (mins: number) => new Date(Date.now() - mins * 60_000).toISOString();

async function jobFromApi(page: Page, name: string) {
  const res = await page.request.get(`/v1/schedules/${name}`);
  expect(res.ok()).toBeTruthy();
  return res.json();
}

test("an empty list says so and offers New", async ({ page, control }) => {
  await control.seedSchedules();
  await page.goto("/#/schedules");
  await expect(page.locator("header.bar h1")).toHaveText("Schedules");
  await expect(page.locator(".sched-empty")).toContainText("No scheduled jobs yet");
  await expect(page.locator(".banner")).toHaveCount(0);
  await expect(page.getByRole("link", { name: "+ New" })).toBeVisible();
});

test("the list sorts failed first, paused last, and shows skips and banners", async ({ page, control }) => {
  await control.seedSchedules({
    jobs: [job("alpha"), job("paused-one", { enabled: false }), job("broken"), job("skipper")],
    runs: [
      { ts: ago(30), job: "broken", result: "failed:launch", note: "no such agent" },
      { ts: ago(20), job: "skipper", result: "skipped:usage", note: "usage 5h 82%" },
      { ts: ago(10), job: "alpha", result: "fired", window: "@1", session: "agentctl-sched" },
    ],
    not_installed: true,
    cron_disabled: true,
  });
  await page.goto("/#/schedules");
  const order = await page.locator(".sched-list .sched-row .agent").allTextContents();
  expect(order[0]).toBe("broken");
  expect(order[3]).toBe("paused-one");
  expect(order.slice(1, 3).sort()).toEqual(["alpha", "skipper"]);
  await expect(page.locator(".banner")).toHaveCount(2);
  await expect(page.locator(".banner").first()).toContainText("Scheduler not installed");
  await expect(page.locator(".banner").nth(1)).toContainText("cron.enabled");
  const broken = page.locator(".sched-row", { hasText: "broken" });
  await expect(broken).toContainText("failed: no such agent");
  await expect(broken.locator(".sched-glyph")).toHaveText("!");
  await expect(broken).toContainText("go-engineer · ");
  await expect(page.locator(".sched-row", { hasText: "skipper" }).locator(".sched-exc")).toHaveText("skipped: usage 5h 82%");
  const paused = page.locator(".sched-row", { hasText: "paused-one" });
  await expect(paused).toContainText("paused");
  await expect(paused.locator(".sched-glyph")).toHaveText("⏸");
});

test("create a schedule with the picker, review it, and save", async ({ page, control }) => {
  const dir = await control.seedSchedules();
  await page.goto("/#/schedules/new");
  await expect(page.locator("select[aria-label=Project] option")).not.toHaveCount(0);
  await page.getByLabel("Name", { exact: true }).fill("weekly-review");
  await page.getByLabel("Prompt", { exact: true }).fill("Summarise open PRs.");
  await page.getByRole("button", { name: "Days…" }).tap();

  // Day chips are 44px touch targets.
  const chips = page.locator(".day-chip");
  await expect(chips).toHaveCount(7);
  for (let i = 0; i < 7; i++) {
    const box = (await chips.nth(i).boundingBox())!;
    expect(box.width).toBeGreaterThanOrEqual(43.5);
    expect(box.height).toBeGreaterThanOrEqual(43.5);
  }
  await page.getByRole("button", { name: "Monday" }).tap();
  await page.getByRole("button", { name: "Wednesday" }).tap();
  await page.getByLabel("Time (local)").fill("07:30");
  await expect(page.locator(".sched-preview")).toContainText("next");
  const review = page.getByRole("button", { name: "Review & schedule" });
  await expect(review).toBeEnabled();
  await review.tap();

  await expect(sheet(page)).toContainText("unattended");
  await expect(sheet(page)).toContainText("permission mode auto");
  await expect(sheet(page)).toContainText("new worktree each run");
  await expect(sheet(page)).toContainText("Next:");
  await expect(sheet(page)).toContainText("Summarise open PRs.");
  await sheet(page).getByRole("button", { name: "Schedule it" }).tap();

  await expect(page).toHaveURL(/#\/schedules\/weekly-review$/);
  const saved = await jobFromApi(page, "weekly-review");
  expect(saved.job.schedule).toBe("30 7 * * 1,3");
  expect(saved.job.dir).toBe(dir);
  expect(saved.job.worktree).not.toBe(false);
  expect(saved.enabled).toBe(true);
});

test("Review stays disabled while the form has an error", async ({ page, control }) => {
  await control.seedSchedules();
  await page.goto("/#/schedules/new");
  await page.getByLabel("Name", { exact: true }).fill("bad name!");
  await page.getByLabel("Prompt", { exact: true }).fill("x");
  await expect(page.locator(".field-error:not([hidden])").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "Review & schedule" })).toBeDisabled();
});

test("editing keeps the fields the form does not show", async ({ page, control }) => {
  await control.seedSchedules({
    jobs: [job("nightly", { worktree: false, account: "work", timeout: "30m", overlap: "allow", schedule: "*/20 9-17 * * 1-5" })],
  });
  await page.goto("/#/schedules/nightly/edit");
  await expect(page.getByLabel("Name", { exact: true })).toBeDisabled();
  // A schedule the picker cannot show opens in Custom, untouched.
  await expect(page.getByRole("button", { name: "Custom" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByLabel("Cron expression")).toHaveValue("*/20 9-17 * * 1-5");
  await page.getByLabel("Prompt", { exact: true }).fill("A new prompt.");
  await page.getByRole("button", { name: "Review changes" }).tap();
  await expect(sheet(page)).toContainText("A new prompt.");
  await sheet(page).getByRole("button", { name: "Save changes" }).tap();
  await expect(page).toHaveURL(/#\/schedules\/nightly$/);
  const saved = (await jobFromApi(page, "nightly")).job;
  expect(saved.prompt).toBe("A new prompt.");
  expect(saved.worktree).toBe(false);
  expect(saved.account).toBe("work");
  expect(saved.timeout).toBe("30m");
  expect(saved.overlap).toBe("allow");
  expect(saved.schedule).toBe("*/20 9-17 * * 1-5");
});

test("swipe pauses with an Undo, and resume announces the next fire", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("nightly")] });
  await page.goto("/#/schedules");
  const link = page.locator("a.sched-row", { hasText: "nightly" });
  await drag(link, { dx: -260, stepMs: 25 });
  await expect.poll(() => offsetX(link.locator("xpath=.."))).toBe(-88);
  await page.getByRole("button", { name: "Pause nightly" }).tap();
  await expect(toast(page).first()).toContainText("nightly paused");
  await expect(page.locator("a.sched-row", { hasText: "nightly" })).toContainText("paused");
  expect((await jobFromApi(page, "nightly")).enabled).toBe(false);

  await toast(page).first().getByRole("button", { name: "Undo" }).tap();
  await expect(toast(page).last()).toContainText("nightly resumed · next");
  await expect(page.locator("a.sched-row", { hasText: "nightly" })).not.toContainText("paused");
  expect((await jobFromApi(page, "nightly")).enabled).toBe(true);
});

test("a paused job resumes from its card", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("nightly", { enabled: false })] });
  await page.goto("/#/schedules/nightly");
  await page.getByRole("button", { name: "Resume" }).tap();
  await expect(toast(page).first()).toContainText("nightly resumed · next");
  await expect(page.getByRole("button", { name: "Pause" })).toBeVisible();
});

test("run now confirms, and shows the skip reason without forcing", async ({ page, control }) => {
  await control.seedSchedules({
    jobs: [job("nightly")],
    run: { result: "skipped:usage", note: "usage 5h 82%" },
  });
  await page.goto("/#/schedules");
  await page.getByRole("button", { name: "Run nightly now" }).tap();
  await expect(sheet(page)).toContainText("Run nightly now?");
  await expect(sheet(page)).toContainText("nothing is forced");
  await sheet(page).getByRole("button", { name: "Run now" }).tap();
  await expect(toast(page).last()).toContainText("nightly did not run: skipped: usage 5h 82%");
  expect(await control.scheduleRuns()).toEqual(["nightly"]);
});

test("run now reports the window it started", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("nightly")] });
  await page.goto("/#/schedules/nightly");
  await page.getByRole("button", { name: "Run now" }).tap();
  await sheet(page).getByRole("button", { name: "Run now" }).tap();
  await expect(toast(page).last()).toContainText("nightly started");
});

test("delete asks first and keeps history", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("nightly")] });
  await page.goto("/#/schedules/nightly");
  await page.getByRole("button", { name: "Delete" }).tap();
  await expect(sheet(page)).toContainText("run history is kept");
  await sheet(page).getByRole("button", { name: "Delete" }).tap();
  await expect(page).toHaveURL(/#\/schedules$/);
  await expect(page.locator(".sched-empty")).toBeVisible();
});

test("duplicate prefills a -copy name and saves paused", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("nightly")] });
  await page.goto("/#/schedules/nightly/copy");
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("nightly-copy");
  await page.getByRole("button", { name: "Review & schedule" }).tap();
  await expect(sheet(page)).toContainText("Saved paused");
  await sheet(page).getByRole("button", { name: "Schedule it" }).tap();
  await expect(page).toHaveURL(/#\/schedules\/nightly-copy$/);
  expect((await jobFromApi(page, "nightly-copy")).enabled).toBe(false);
});

test("the fleet's scheduled bucket links to the screen", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("nightly")] });
  await control.setFleet([
    { session: "agentctl-sched", project: "sched", dir: "/fixture/sched", windows: [{ id: "@90", index: 1, agent: "go-engineer", state: "idle", scheduled_by: "nightly" }] },
  ]);
  await page.goto("/");
  await page.getByRole("link", { name: "Schedules ›" }).tap();
  await expect(page.locator("header.bar h1")).toHaveText("Schedules");
});
