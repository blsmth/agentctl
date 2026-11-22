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
  await page.getByLabel("Hour", { exact: true }).selectOption("7");
  await page.getByLabel("Minute", { exact: true }).selectOption("30");
  await expect(page.locator(".sched-preview")).toContainText("next");
  const review = page.getByRole("button", { name: "Review & schedule" });
  await expect(review).toBeEnabled();
  await review.tap();

  await expect(sheet(page)).toContainText("Runs");
  await expect(sheet(page)).toContainText("When");
  await expect(sheet(page)).toContainText("Mode");
  await expect(sheet(page)).toContainText("unattended");
  await expect(sheet(page)).toContainText("permission mode auto");
  await expect(sheet(page)).toContainText("until paused");
  await expect(sheet(page)).toContainText("a worktree per fire");
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

test("Review stays enabled with an error; a tap shows every error and scrolls to the first", async ({ page, control }) => {
  await control.seedSchedules();
  await page.goto("/#/schedules/new");
  const review = page.getByRole("button", { name: "Review & schedule" });
  await expect(review).toBeEnabled();
  await page.getByLabel("Name", { exact: true }).fill("bad name!");
  await expect(page.locator(".field-error:not([hidden])").first()).toBeVisible();
  await expect(review).toBeEnabled();
  // Untouched, still-empty fields only complain after the tap.
  await page.getByLabel("Name", { exact: true }).fill("");
  await review.tap();
  await expect(sheet(page)).toHaveCount(0);
  const errs = page.locator(".field-error:not([hidden])");
  expect(await errs.count()).toBeGreaterThanOrEqual(2);
  await expect(errs.first()).toBeInViewport();
});

test("the form's new controls: weekdays default, one-row kinds, 24h time, segments, presets and usage", async ({ page, control }) => {
  await control.seedSchedules();
  await page.goto("/#/schedules/new");
  await expect(page.getByRole("button", { name: "Weekdays" })).toHaveAttribute("aria-pressed", "true");
  expect(await page.locator("form").innerText()).not.toMatch(/\bnull\b/);
  const tops = await page.locator(".sched-seg .btn").evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)));
  expect(new Set(tops).size).toBe(1);
  // 24h: an hour select running 00-23.
  const hours = await page.getByLabel("Hour", { exact: true }).locator("option").allTextContents();
  expect(hours[0]).toBe("00");
  expect(hours.at(-1)).toBe("23");
  // Native selects are as tall as the inputs.
  const sel = (await page.locator("select[aria-label=Project]").boundingBox())!;
  const inp = (await page.getByLabel("Name", { exact: true }).boundingBox())!;
  expect(Math.abs(sel.height - inp.height)).toBeLessThan(2);

  await page.locator("details.sched-advanced summary").tap();
  await expect(page.locator(".sched-adv-summary")).toHaveText("");
  // Account select names the resolved default; model names the agent default.
  await expect(page.locator("select[aria-label=Account] option").first()).toHaveText("default (personal)");
  await expect(page.locator("select[aria-label=Model] option").first()).toContainText("agent default");
  await page.locator("select[aria-label=Account]").selectOption("work");
  await page.getByRole("button", { name: "Skip missed" }).tap();
  await page.getByLabel("Timeout", { exact: true }).selectOption("45m");
  await page.getByLabel("Skip above, 5h usage").selectOption("70");
  await page.getByLabel("Skip above, 7d usage").selectOption("100");
  await expect(page.locator(".sched-adv-summary")).toContainText("account work");
  await expect(page.locator(".sched-adv-summary")).toContainText("timeout 45m");
  await page.getByLabel("Timeout", { exact: true }).selectOption("__custom");
  await page.getByLabel("Custom timeout").fill("90m");
  await expect(page.locator(".sched-adv-summary")).toContainText("timeout 90m");

  await page.getByLabel("Name", { exact: true }).fill("nightly-x");
  await page.getByLabel("Prompt", { exact: true }).fill("Do it.");
  // Let the preview settle: clearing an error moves the button.
  await expect(page.locator(".field-error:not([hidden])")).toHaveCount(0);
  await page.getByRole("button", { name: "Review & schedule" }).tap();
  await sheet(page).getByRole("button", { name: "Schedule it" }).tap();
  await expect(page).toHaveURL(/#\/schedules\/nightly-x$/);
  const saved = (await jobFromApi(page, "nightly-x")).job;
  expect(saved.account).toBe("work");
  expect(saved.catchup).toBe("none");
  expect(saved.timeout).toBe("90m");
  expect(saved.skip_if_usage_above).toEqual({ five_hour: 70, seven_day: 100 });
});

test("an edit's review shows old → new, and a stored unknown value is kept", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("nightly", { model: "opsu", account: "soon" })] });
  await page.goto("/#/schedules/nightly/edit");
  await page.locator("details.sched-advanced summary").tap();
  await expect(page.locator("select[aria-label=Model] option.unknown")).toHaveText("opsu (unknown)");
  await expect(page.locator("select[aria-label=Account] option.unknown")).toHaveText("soon (unknown)");
  await page.getByRole("button", { name: "Skip missed" }).tap();
  await page.getByRole("button", { name: "Review changes" }).tap();
  await expect(sheet(page)).toContainText("Changes:");
  await expect(sheet(page)).toContainText("catch-up  latest → none");
  await sheet(page).getByRole("button", { name: "Save changes" }).tap();
  await expect(page).toHaveURL(/#\/schedules\/nightly$/);
  const saved = (await jobFromApi(page, "nightly")).job;
  expect(saved.model).toBe("opsu");
  expect(saved.account).toBe("soon");
});

test("a prompt_key job shows the key read-only", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("keyed", { prompt: "", prompt_key: "my-key" })] });
  await page.goto("/#/schedules/keyed/edit");
  await expect(page.locator(".sched-keyline")).toHaveText("from context key my-key");
  await expect(page.getByLabel("Prompt", { exact: true })).toHaveCount(0);
});

test("the card leads with the status line and failure reason; Delete is in the overflow", async ({ page, control }) => {
  await control.seedSchedules({
    jobs: [job("nightly")],
    runs: [{ ts: ago(30), job: "nightly", result: "failed:launch", note: "no such agent" }],
  });
  await page.goto("/#/schedules/nightly");
  await expect(page.locator(".sched-card > *").first()).toHaveClass(/sched-status/);
  await expect(page.locator(".sched-status")).toContainText("next");
  await expect(page.locator(".sched-fail")).toContainText("Last run failed · launch: no such agent");
  await expect(page.getByRole("button", { name: "Delete" })).toBeHidden();
  await expect(page.locator("ul.sched-runs")).toContainText("failed · launch");
  expect(await page.locator("body").innerText()).not.toContain("null");
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
  await page.getByLabel("More actions").tap();
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

test("typing in the prompt does not spend the write budget on previews", async ({ page, control }) => {
  await control.seedSchedules();
  await page.goto("/#/schedules/new");
  await expect(page.locator(".sched-preview")).toContainText("next");
  let previews = 0;
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith("/v1/schedules/preview")) previews++;
  });
  await page.getByLabel("Prompt", { exact: true }).pressSequentially("Triage every new issue and label it", { delay: 25 });
  await page.getByLabel("Name", { exact: true }).pressSequentially("triage", { delay: 25 });
  expect(previews).toBeLessThanOrEqual(2);
  await expect(page.getByRole("button", { name: "Review & schedule" })).toBeEnabled();
});

test("a template as the prompt source: pick it, review it, save, and see it on the card and the list", async ({ page, control }) => {
  await control.seedSchedules({
    templates: [{ name: "triage", description: "Weekly triage", agent: "go-engineer", prompt: "Triage the backlog." }],
  });
  await page.goto("/#/schedules/new");
  await expect(page.locator("select[aria-label=Project] option")).not.toHaveCount(0);
  await page.getByLabel("Name", { exact: true }).fill("triage-job");
  await page.getByRole("group", { name: "Prompt source" }).getByRole("button", { name: "Template" }).tap();
  const pick = page.locator("select[aria-label=Template]");
  await expect(pick.locator("option", { hasText: "triage · Weekly triage" })).toHaveCount(1);
  await pick.selectOption("triage");
  await expect(page.locator("pre[aria-label='Template prompt']")).toHaveText("Triage the backlog.");
  await page.locator("select[aria-label=Agent]").selectOption("");
  await expect(page.locator(".sched-preview")).toContainText("next");
  await page.getByRole("button", { name: "Review & schedule" }).tap();
  await expect(sheet(page)).toContainText("go-engineer (from template triage)");
  await expect(sheet(page)).toContainText("Prompt: template triage");
  await expect(sheet(page)).toContainText("Triage the backlog.");
  await sheet(page).getByRole("button", { name: "Schedule it" }).tap();

  await expect(page).toHaveURL(/#\/schedules\/triage-job$/);
  const saved = await jobFromApi(page, "triage-job");
  expect(saved.job.template).toBe("triage");
  expect(saved.job.prompt ?? "").toBe("");
  expect(saved.job.agent ?? "").toBe("");
  await expect(page.locator(".sched-prompt")).toHaveText("Triage the backlog.");
  await expect(page.locator(".sched-facts")).toContainText("Template triage");

  await page.goto("/#/schedules");
  await expect(page.locator(".sched-row", { hasText: "triage-job" })).toContainText("template triage");
});

test("a job whose template was deleted shows as broken", async ({ page, control }) => {
  await control.seedSchedules({ jobs: [job("orphan", { prompt: undefined, template: "gone" })] });
  await page.goto("/#/schedules/orphan");
  await expect(page.locator(".sched-status")).toContainText("invalid");
  await expect(page.locator(".sched-fail").first()).toContainText('template "gone" not found');
});
