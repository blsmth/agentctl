// The window view: header, pane, state-aware actions, the window's approvals.
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
  await expect(page.locator(".answer .question")).toHaveText("Do you want to proceed?");
  const yes = page.locator(".answer .choices button", { hasText: "1. Yes" });
  await expect(page.locator(".answer .choices button")).toHaveCount(3); // 1, 2, Dismiss
  await yes.tap();
  await expect(sheet(page)).toContainText('Answer "1"?');
  await sheet(page).getByRole("button", { name: "Send" }).tap();
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toEqual([{ kind: "key", session: SHOP, window: "@14", pane: "%14", key: "1" }]);
  // The fake dismisses the dialog; the refreshed view stops offering answers.
  await expect(page.locator(".answer")).toHaveCount(0);
});

// @17 is a two-question AskUserQuestion from real Claude Code captures: a
// single-select tab, a multi-select tab, then the review page. The fake
// redraws each screen per key as Claude Code does, so the server's re-parse
// between the keys of a submit sees every toggle and cursor move land.
test("answer an AskUserQuestion: single-select, multi-select, review page", async ({ page, control }) => {
  await openWindow(page, "@17");
  const answer = page.locator(".answer");
  const send = () => sheet(page).getByRole("button", { name: "Send" }).tap();

  // Tab 1: a digit picks and moves on.
  await expect(answer.locator(".question")).toHaveText("Which color should the button be?");
  await answer.locator(".choices button", { hasText: "2. Green" }).tap();
  await expect(sheet(page)).toContainText('Answer "2"?');
  await send();

  // Tab 2: local checkboxes, one Submit that sends the set.
  await expect(answer.locator(".question")).toHaveText("Which sizes should we ship?");
  await answer.locator("label", { hasText: "Small" }).locator("input").check();
  await answer.locator("label", { hasText: "Large" }).locator("input").check();
  await answer.getByRole("button", { name: "Submit", exact: true }).tap();
  await expect(sheet(page)).toContainText("Small");
  await send();

  // Review page: an ordinary dialog.
  await expect(answer.locator(".question")).toHaveText("Ready to submit your answers?");
  await answer.locator(".choices button", { hasText: "1. Submit answers" }).tap();
  await send();
  await expect(answer).toHaveCount(0);

  // Toggles 1 and 3, four rows down to Submit, Enter: each sent only after
  // the previous one showed up in the pane.
  const keys = (await control.actions()).filter((a) => a.window === "@17").map((a) => a.key);
  expect(keys).toEqual(["2", "1", "3", "Down", "Down", "Down", "Down", "Enter", "1"]);
});

test("blocked layout: answers above the pane, question only, recommended option primary", async ({ page }) => {
  await openWindow(page, "@14");
  const answer = page.locator(".answer");
  await expect(answer).toBeVisible();
  const answerBox = await answer.boundingBox();
  const paneBox = await page.locator(".pane").boundingBox();
  expect(answerBox!.y + answerBox!.height).toBeLessThanOrEqual(paneBox!.y);
  // The pane already shows the dialog; the answer card carries only its question.
  await expect(answer).not.toContainText("Bash command");
  await expect(page.locator(".actions")).toBeEmpty();
  const choices = answer.locator(".choices button");
  await expect(choices).toHaveText(["1. Yes", "2. No, and tell Claude what to do differently", "Dismiss (Esc)"]);
  await expect(choices.nth(0)).toHaveClass(/\bprimary\b/);
  await expect(choices.nth(1)).not.toHaveClass(/\bprimary\b/);
  // Full-width rows.
  const first = await choices.nth(0).boundingBox();
  const second = await choices.nth(1).boundingBox();
  expect(Math.abs(first!.width - second!.width)).toBeLessThan(1);
  expect(second!.y).toBeGreaterThan(first!.y);
  await choices.nth(2).tap();
  await expect(sheet(page)).toContainText("Dismiss the dialog (Esc)?");
});

test("interrupt a working agent", async ({ page, control }) => {
  await openWindow(page, "@13");
  await page.getByRole("button", { name: "Interrupt" }).tap();
  await sheet(page).getByRole("button", { name: "Interrupt" }).tap();
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toEqual([{ kind: "key", session: SHOP, window: "@13", pane: "%13", key: "Escape" }]);
});

test("Interrupt is a small right-aligned button beside the queue composer", async ({ page }) => {
  await openWindow(page, "@13");
  const interrupt = page.getByRole("button", { name: "Interrupt" });
  const queue = page.getByRole("button", { name: "Queue message" });
  await expect(interrupt).toHaveClass(/\bsmall\b/);
  const ib = await interrupt.boundingBox();
  const qb = await queue.boundingBox();
  expect(ib!.x).toBeGreaterThan(qb!.x);
  expect(ib!.width).toBeLessThan(qb!.width);
  expect(ib!.height).toBeLessThan(44);
});

test("after an interrupt, the reply box that follows gets focus", async ({ page, control }) => {
  await openWindow(page, "@13");
  await page.getByRole("button", { name: "Interrupt" }).tap();
  await sheet(page).getByRole("button", { name: "Interrupt" }).tap();
  await control.waitForActions((a) => a.length > 0);
  await control.patchWindow(SHOP, "@13", { state: "idle", pane: "idle" });
  await expect(page.locator(".actions textarea")).toBeFocused();
  await expect(page.locator("#toasts .toast", { hasText: "Interrupted. Tell it what to do next." })).toBeVisible();
});

test("queue a message for a working agent", async ({ page, control }) => {
  await openWindow(page, "@13");
  const text = page.locator(".actions textarea");
  await text.fill("then run the linter");
  await page.getByRole("button", { name: "Queue message" }).tap();
  await expect(sheet(page)).toContainText("Queue message?");
  await expect(sheet(page)).toContainText("Delivered after the current step.");
  await sheet(page).getByRole("button", { name: "Queue" }).tap();
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toEqual([{ kind: "queue", session: SHOP, window: "@13", pane: "%13", text: "then run the linter" }]);
  await expect(text).toHaveValue("");
});

test("an Interrupt sheet left open does not survive navigation to another window", async ({ page, control, guard }) => {
  await openWindow(page, "@13");
  await page.getByRole("button", { name: "Interrupt" }).tap();
  await expect(sheet(page)).toContainText("Interrupt agent?");
  await page.evaluate((h) => (location.hash = h), `#/w/${SHOP}/${encodeURIComponent("@14")}`);
  await expect(page.locator(".answer .question")).toHaveText("Do you want to proceed?");
  await expect(page.locator(".overlay")).toHaveCount(0);
  await page.waitForTimeout(500);
  expect(await control.actions()).toEqual([]);
  expect(guard.unexpected()).toEqual([]);
});

test("a closed window says so and offers the way back", async ({ page, control, guard }) => {
  guard.allow(/HTTP 404|status of 404/);
  await openWindow(page, "@15");
  await page.locator(".actions .close-window").tap();
  await expect(sheet(page)).toContainText("Close window?");
  await sheet(page).getByRole("button", { name: "Close" }).tap();
  await control.waitForActions((a) => a.some((x) => x.kind === "close"));
  await expect(page.locator(".gone")).toContainText("This window was closed.");
  await expect(page.locator("header.bar h1")).toHaveText("Window closed");
  await expect(page.locator(".pane-bar")).toBeHidden();
  await expect(page.locator(".actions")).toBeEmpty();
  await page.locator(".gone").getByRole("link", { name: "Back to Fleet" }).tap();
  await expect(page.locator("header.bar h1")).toHaveText("Fleet");
});

test("header: state pill, project and branch, task line and chips", async ({ page }) => {
  await openWindow(page, "@13");
  await expect(page.locator(".state-pill")).toHaveClass(/\bworking\b/);
  await expect(page.locator(".state-pill")).toHaveText(/^working for \d+m$/);
  await expect(page.locator(".win-where")).toHaveText("shop · agent/test-writer/issue-9");
  await expect(page.locator(".win-task")).toHaveText("Cover the cart service");
  // No recap: no second line (the pane already shows the last tool call).
  await expect(page.locator(".win-recap")).toBeHidden();
  await expect(page.locator(".chips .chip")).toHaveText(["9% ctx", "$0.98", "sonnet-4-5", "@13 · dispatch"]);
  // No header Live terminal button: Live is the third pane segment.
  await expect(page.locator("header.bar .btn")).toHaveCount(0);
  await expect(page.locator(".segmented .seg")).toHaveText(["Reflow", "Exact", "Live"]);
  await expect(page.locator(".segmented a.seg")).toHaveAttribute("href", `#/t/${SHOP}/%4013`);
});

test("header: the recap replaces the last tool call", async ({ page, control }) => {
  await control.patchWindow(SHOP, "@13", { recap: "Writing table tests for the cart totals.", recap_source: "summary" });
  await openWindow(page, "@13");
  await expect(page.locator(".win-task")).toHaveText("Cover the cart service");
  await expect(page.locator(".win-recap")).toHaveText("Writing table tests for the cart totals.");
  await expect(page.locator(".win-head")).not.toContainText("go test ./...");
});

test("header: a short row title keeps the full recap in the window view", async ({ page, control }) => {
  const recap = "Wrote table tests for the cart totals and the discount rounding, next the tax edge cases.";
  await control.patchWindow(SHOP, "@13", { recap, recap_source: "summary", short_title: "Testing Cart Totals" });
  await openWindow(page, "@13");
  await expect(page.locator(".win-recap")).toHaveText(recap);
  await expect(page.locator(".win-head")).not.toContainText("Testing Cart Totals");
});

test("done windows show the PR chip, a full-width Reply and a small Close", async ({ page }) => {
  await openWindow(page, "@12");
  const pr = page.locator(".chips a.chip.pr");
  await expect(pr).toHaveText("PR #8");
  await expect(pr).toHaveAttribute("href", "https://github.com/example/shop/pull/8");
  const reply = await page.getByRole("button", { name: "Reply" }).boundingBox();
  const text = await page.locator(".actions textarea").boundingBox();
  expect(Math.abs(reply!.width - text!.width)).toBeLessThan(1);
  const close = page.locator(".actions .close-window");
  await expect(close).toHaveText("Close window");
  expect((await close.boundingBox())!.y).toBeGreaterThan(reply!.y);
});

test("the pane fills the screen down to the actions", async ({ page }) => {
  await openWindow(page, "@13");
  const viewport = page.viewportSize()!;
  const pane = (await page.locator(".pane").boundingBox())!;
  const actions = (await page.locator(".actions").boundingBox())!;
  const nav = (await page.locator("nav").boundingBox())!;
  expect(pane.height).toBeGreaterThan(viewport.height * 0.3);
  // The pane runs down to the actions, which sit right above the tab bar:
  // no dead band anywhere below the pane.
  expect(actions.y - (pane.y + pane.height)).toBeLessThan(4);
  expect(nav.y - (actions.y + actions.height)).toBeLessThan(24);
});

test("an empty prompt box is left out of Reflow", async ({ page }) => {
  await openWindow(page, "@15");
  // IdlePane is a labelled rule, an empty prompt and a closing rule.
  await expect(page.locator(".pane-reflow")).not.toContainText("❯");
  await expect(page.locator(".pane-reflow .pane-empty")).toHaveText("At an empty prompt.");
});

test("liveness chip: updated, then offline when fetches fail", async ({ page }) => {
  await openWindow(page, "@13");
  const chip = page.locator(".live-chip");
  await expect(chip).toHaveText(/^updated \d+[sm] ago$/);
  // Fail the window's fetches as a dropped network would (page.route does
  // not see requests once the service worker controls the page).
  await page.evaluate(() => {
    const w = window as unknown as { realFetch: typeof fetch };
    w.realFetch = window.fetch;
    window.fetch = (input, init) =>
      String(input).startsWith("/v1/sessions/") ? Promise.reject(new TypeError("Load failed")) : w.realFetch(input, init);
  });
  await expect(chip).toHaveText(/^offline since \d\d:\d\d$/, { timeout: 8000 });
  await expect(chip).toHaveClass(/\boffline\b/);
  await page.evaluate(() => {
    window.fetch = (window as unknown as { realFetch: typeof fetch }).realFetch;
  });
  await expect(chip).toHaveText(/^updated /, { timeout: 8000 });
});

test("new output while scrolled up shows a pill that scrolls to the bottom", async ({ page, control }) => {
  const long = (tag: string) => Array.from({ length: 150 }, (_, i) => `${tag} line ${i}`).join("\n") + "\n";
  await control.patchWindow(SHOP, "@13", { pane_text: long("a") });
  await openWindow(page, "@13");
  const scroller = page.locator(".pane-reflow");
  await expect(scroller).toContainText("a line 149");
  await scroller.evaluate((el) => (el.scrollTop = 0));
  const pill = page.locator(".new-output");
  await expect(pill).toBeHidden();
  await control.patchWindow(SHOP, "@13", { pane_text: long("b") });
  await expect(pill).toBeVisible({ timeout: 8000 });
  await pill.tap();
  await expect(pill).toBeHidden();
  const atBottom = await scroller.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight < 24);
  expect(atBottom).toBe(true);
});

test("Exact shows a one-time hint", async ({ page }) => {
  await openWindow(page, "@13");
  await page.locator(".segmented").getByText("Exact", { exact: true }).tap();
  const hint = page.locator(".exact-hint");
  await expect(hint).toBeVisible();
  await hint.getByRole("button", { name: "Got it" }).tap();
  await expect(hint).toBeHidden();
  await page.reload();
  await expect(page.locator(".pane .xterm")).toBeVisible();
  await expect(hint).toBeHidden();
  await page.evaluate(() => localStorage.clear());
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

test("a cron window says which job started it", async ({ page, control }) => {
  const groups = await control.fleet();
  groups[0].windows.push({ id: "@16", index: 6, agent: "triage", state: "idle", mode: "adhoc", scheduled_by: "nightly-triage", activity: "triaged 3 issues" });
  await control.setFleet(groups);
  await openWindow(page, "@16");
  await expect(page.locator(".win-scheduled")).toBeVisible();
  await expect(page.locator(".win-scheduled")).toHaveText(/^Scheduled run: nightly-triage, fired \w{3} \d{1,2} \w{3} \d{2}:\d{2}$/);
});

test("a window not started by cron shows no schedule line", async ({ page }) => {
  await openWindow(page, "@15");
  await expect(page.locator("header.bar h1")).toHaveText("docs");
  await expect(page.locator(".win-scheduled")).toBeHidden();
});

test("reply with an attached screenshot", async ({ page, control }) => {
  await openWindow(page, "@15");
  // 1x1 transparent PNG
  const png = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
    "base64",
  );
  await page.locator(".actions textarea").fill("what is wrong here?");
  await page.locator(".actions input[type=file]").setInputFiles({ name: "IMG_0001.png", mimeType: "image/png", buffer: png });
  await expect(page.locator(".actions .thumb img")).toHaveCount(1);
  await page.getByRole("button", { name: "Reply" }).tap();
  await expect(sheet(page)).toContainText("+ 1 image");
  await sheet(page).getByRole("button", { name: "Send" }).tap();
  await expect(page.locator("#toasts .toast").first()).toHaveText("Sent");
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toHaveLength(1);
  expect(actions[0].text).toMatch(/^what is wrong here\?\n\n\[Screenshot: \/.*\/uploads\/15\/[0-9a-f]{32}\.png\]$/);
});

test("a screenshot can be removed before sending", async ({ page }) => {
  await openWindow(page, "@15");
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64");
  await page.locator(".actions input[type=file]").setInputFiles({ name: "a.png", mimeType: "image/png", buffer: png });
  await expect(page.locator(".actions .thumb")).toHaveCount(1);
  await page.getByRole("button", { name: "Remove image" }).tap();
  await expect(page.locator(".actions .thumb")).toHaveCount(0);
});
