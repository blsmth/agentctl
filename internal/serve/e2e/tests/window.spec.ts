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
  await page.getByRole("button", { name: "Send reply" }).tap();
  await expect(sheet(page)).toHaveCount(0);
  await expect(page.locator("#toasts .toast").first()).toHaveText("Sent");
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toEqual([{ kind: "reply", session: SHOP, window: "@15", pane: "%15", text: "please also update CHANGELOG" }]);
});

test("a double tap on send sends the reply once", async ({ page, control }) => {
  await openWindow(page, "@15");
  await page.route(/\/reply$/, async (route) => {
    await new Promise((r) => setTimeout(r, 600));
    await route.continue();
  });
  await page.locator(".actions textarea").fill("only once");
  await page.getByRole("button", { name: "Send reply" }).evaluate((b: HTMLElement) => {
    b.click();
    b.click();
  });
  await expect(sheet(page)).toHaveCount(0);
  await control.waitForActions((a) => a.length > 0);
  await page.waitForTimeout(800);
  expect(await control.actions()).toHaveLength(1);
});

test("answer a blocked agent's dialog; a double tap sends one key", async ({ page, control }) => {
  await openWindow(page, "@14");
  await expect(page.locator(".answer .question")).toHaveText("Do you want to proceed?");
  const yes = page.locator(".answer .choices button", { hasText: "1. Yes" });
  await expect(page.locator(".answer .choices button")).toHaveCount(3); // 1, 2, Dismiss
  // Hold the response so a second tap lands while the first is in flight:
  // it must not send a second key.
  await page.route(/\/windows\/%4014\/answer$/, async (route) => {
    await new Promise((r) => setTimeout(r, 600));
    await route.continue();
  });
  await yes.evaluate((b: HTMLElement) => {
    b.click();
    b.click();
  });
  await expect(sheet(page)).toHaveCount(0);
  await control.waitForActions((a) => a.length > 0);
  await page.waitForTimeout(800);
  const actions = await control.actions();
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

  // Tab 1: a digit picks and moves on.
  await expect(answer.locator(".question")).toHaveText("Which color should the button be?");
  await answer.locator(".choices button", { hasText: "2. Green" }).tap();
  await expect(sheet(page)).toHaveCount(0);

  // Tab 2: local checkboxes, one Submit that sends the set.
  await expect(answer.locator(".question")).toHaveText("Which sizes should we ship?");
  await answer.locator("label", { hasText: "Small" }).locator("input").check();
  await answer.locator("label", { hasText: "Large" }).locator("input").check();
  await answer.getByRole("button", { name: "Submit", exact: true }).tap();
  await expect(sheet(page)).toHaveCount(0);

  // Review page: an ordinary dialog.
  await expect(answer.locator(".question")).toHaveText("Ready to submit your answers?");
  await answer.locator(".choices button", { hasText: "1. Submit answers" }).tap();
  await expect(sheet(page)).toHaveCount(0);
  await expect(answer).toHaveCount(0);

  // Toggles 1 and 3, four rows down to Submit, Enter: each sent only after
  // the previous one showed up in the pane.
  const keys = (await control.actions()).filter((a) => a.window === "@17").map((a) => a.key);
  expect(keys).toEqual(["2", "1", "3", "Down", "Down", "Down", "Down", "Enter", "1"]);
});

test("blocked layout: answers above the pane, question only, recommended option primary", async ({ page, control }) => {
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
  // Nothing is sent until the sheet is confirmed.
  expect(await control.actions()).toEqual([]);
});

test("interrupt a working agent", async ({ page, control }) => {
  await openWindow(page, "@13");
  await page.getByRole("button", { name: "Interrupt" }).tap();
  await sheet(page).getByRole("button", { name: "Interrupt" }).tap();
  const actions = await control.waitForActions((a) => a.length > 0);
  expect(actions).toEqual([{ kind: "key", session: SHOP, window: "@13", pane: "%13", key: "Escape" }]);
});

test("queue state: Interrupt on its own line above a chat-style composer", async ({ page }) => {
  await openWindow(page, "@13");
  const interrupt = page.getByRole("button", { name: "Interrupt" });
  await expect(interrupt).toBeVisible();
  await expect(interrupt).toHaveClass(/\bsmall\b/);
  const send = (await page.getByRole("button", { name: "Queue message" }).boundingBox())!;
  const text = (await page.locator(".actions textarea").boundingBox())!;
  const cam = (await page.getByRole("button", { name: "Attach image" }).boundingBox())!;
  const ib = (await interrupt.boundingBox())!;
  const width = page.viewportSize()!.width;
  // One row: camera, textarea, send; all tap targets at least 44px.
  expect(cam.x).toBeLessThan(text.x);
  expect(text.x).toBeLessThan(send.x);
  expect(Math.min(send.width, send.height, cam.width, cam.height)).toBeGreaterThanOrEqual(44);
  expect(text.x + text.width).toBeLessThanOrEqual(width);
  // Interrupt: small, right-aligned, above the composer row.
  expect(ib.height).toBeLessThan(44);
  expect(ib.y + ib.height).toBeLessThanOrEqual(text.y);
  expect(ib.x + ib.width).toBeGreaterThan(send.x);
  await expect(page.locator(".actions textarea")).toHaveAttribute("placeholder", "Queue a message for the agent…");
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
  await expect(sheet(page)).toHaveCount(0);
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

test("closing a window returns to the fleet", async ({ page, control, guard }) => {
  guard.allow(/HTTP 404|status of 404/);
  await openWindow(page, "@15");
  await page.getByRole("button", { name: "Close window" }).tap();
  await expect(sheet(page)).toContainText("Close window?");
  await sheet(page).getByRole("button", { name: "Close" }).tap();
  await control.waitForActions((a) => a.some((x) => x.kind === "close"));
  await expect(page.locator("header.bar h1")).toHaveText("Fleet");
  expect(new URL(page.url()).hash).toBe("#/");
  await expect(page.locator(".gone")).toHaveCount(0);
  await page.waitForTimeout(500);
  await expect(page.locator("#toasts .toast.error")).toHaveCount(0);
});

test("a failed close stays on the window", async ({ page, guard }) => {
  await openWindow(page, "@15");
  // Stub in the page: page.route does not see requests once the service worker controls it.
  await page.evaluate(() => {
    const real = window.fetch;
    window.fetch = (input, init) =>
      String(input).endsWith("/close")
        ? Promise.resolve(new Response(JSON.stringify({ error: "boom" }), { status: 500, headers: { "Content-Type": "application/json" } }))
        : real(input, init);
  });
  await page.getByRole("button", { name: "Close window" }).tap();
  await sheet(page).getByRole("button", { name: "Close" }).tap();
  await expect(page.locator("#toasts .toast.error")).toBeVisible();
  expect(new URL(page.url()).hash).toContain("/w/");
  await expect(page.locator(".pane")).toBeVisible();
  await expect(page.locator("header.bar h1")).toHaveText("docs");
});

test("a window closed elsewhere says so and offers the way back", async ({ page, guard }) => {
  guard.allow(/HTTP 404|status of 404/);
  await page.route(/\/v1\/sessions\/[^/]+\/windows\/[^/?]+(\/pane)?(\?.*)?$/, (r) => r.fulfill({ status: 404, contentType: "application/json", body: "{}" }));
  await page.goto(`/#/w/${SHOP}/${encodeURIComponent("@15")}`);
  await expect(page.locator(".gone")).toBeVisible();
  await expect(page.locator(".gone")).toContainText("This window was closed.");
  await expect(page.locator("header.bar h1")).toHaveText("Window closed");
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
  await expect(page.locator(".chips .chip")).toHaveText(["9% ctx", "$0.98", "sonnet-4-5", "dispatch"]);
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

test("header: a long title ellipsizes and keeps Close on screen", async ({ page, control }) => {
  await control.patchWindow(SHOP, "@13", { recap: "Writing tests.", recap_source: "summary", short_title: "A very long generated title that cannot possibly fit on a phone header", short_kind: "build" });
  await openWindow(page, "@13");
  const h1 = page.locator("header.bar h1");
  await expect(h1).toContainText("A very long generated title");
  await expect(page.locator(".win-where")).toContainText("test-writer");
  await expect(page.locator("header.bar h1 .kind-chip")).toHaveText("build");
  const close = await page.locator(".close-window").boundingBox();
  const vp = page.viewportSize();
  expect(close && vp && close.x + close.width <= vp.width).toBe(true);
});

test("done windows show the PR chip and a chat-style composer with Close in the header", async ({ page }) => {
  await openWindow(page, "@12");
  const pr = page.locator(".chips a.chip.pr");
  await expect(pr).toHaveText("PR #8");
  await expect(pr).toHaveAttribute("href", "https://github.com/example/shop/pull/8");
  const send = (await page.getByRole("button", { name: "Send reply" }).boundingBox())!;
  const text = (await page.locator(".actions textarea").boundingBox())!;
  const cam = (await page.getByRole("button", { name: "Attach image" }).boundingBox())!;
  // One row: camera, textarea, send; all tap targets at least 44px.
  expect(cam.x).toBeLessThan(text.x);
  expect(text.x).toBeLessThan(send.x);
  expect(Math.min(send.width, send.height, cam.width, cam.height)).toBeGreaterThanOrEqual(44);
  expect(text.x + text.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  await expect(page.locator(".actions").getByRole("button", { name: "Close window" })).toHaveCount(0);
  const close = page.locator("header.bar").getByRole("button", { name: "Close window" });
  await expect(close).toBeVisible();
  expect((await close.boundingBox())!.width).toBeGreaterThanOrEqual(44);
});

test("the composer textarea grows with its text, up to a cap", async ({ page }) => {
  await openWindow(page, "@15");
  const ta = page.locator(".actions textarea");
  const one = (await ta.boundingBox())!.height;
  await ta.fill("a\nb\nc");
  const three = (await ta.boundingBox())!.height;
  expect(three).toBeGreaterThan(one);
  await ta.fill(Array(30).fill("x").join("\n"));
  expect((await ta.boundingBox())!.height).toBeLessThan(150);
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

test("double tap expands the reflow pane full screen; again, close or Escape returns", async ({ page }) => {
  await openWindow(page, "@13");
  const wrap = page.locator(".pane-reflow-wrap");
  const scroller = page.locator(".pane-reflow");
  await expect(scroller).toContainText("");
  await scroller.dblclick();
  await expect(wrap).toHaveClass(/\bexpanded\b/);
  const vp = page.viewportSize()!;
  expect((await wrap.boundingBox())!.height).toBeGreaterThanOrEqual(vp.height - 1);
  await expect(page.locator("body")).toHaveClass(/pane-expanded/);
  await page.locator(".pane-expand-close").click();
  await expect(wrap).not.toHaveClass(/\bexpanded\b/);
  await expect(page.locator("body")).not.toHaveClass(/pane-expanded/);
  await scroller.dblclick();
  await expect(wrap).toHaveClass(/\bexpanded\b/);
  await page.keyboard.press("Escape");
  await expect(wrap).not.toHaveClass(/\bexpanded\b/);
  await scroller.dblclick();
  await page.goto("#/");
  await expect(page.locator("body")).not.toHaveClass(/pane-expanded/);
});

test("Load earlier fetches the full scrollback, keeps position, and the live tail returns at the bottom", async ({ page, control }) => {
  const big = Array.from({ length: 6000 }, (_, i) => `row ${i}`).join("\n") + "\n";
  await control.patchWindow(SHOP, "@13", { pane_text: big });
  await openWindow(page, "@13");
  const scroller = page.locator(".pane-reflow");
  await expect(scroller).toContainText("row 5999");
  await expect(scroller).not.toContainText("row 10\n");
  expect(await scroller.locator(".pane-line").count()).toBeLessThan(400);
  await scroller.evaluate((el) => (el.scrollTop = 0));
  const more = page.locator(".load-earlier");
  await expect(more).toBeVisible();
  const t0 = Date.now();
  await more.tap();
  await expect(scroller.locator(".pane-line").first()).toHaveText("row 0");
  console.log(`full history render: ${Date.now() - t0}ms`);
  // The line that was at the top (first of the tail) is still at the top.
  const topIdx = await scroller.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const line = [...el.querySelectorAll(".pane-line")].find((l) => l.getBoundingClientRect().bottom > r.top + 2);
    return line?.textContent;
  });
  expect(topIdx).toMatch(/^row (5799|58\d\d)$/);
  await scroller.evaluate((el) => (el.scrollTop = el.scrollHeight));
  await expect(scroller.locator(".pane-line").first()).not.toHaveText("row 0");
});

test("Load earlier is hidden when there is nothing earlier", async ({ page, control }) => {
  const rows = (n: number) => Array.from({ length: n }, (_, i) => `row ${i}`).join("\n") + "\n";
  await control.patchWindow(SHOP, "@13", { pane_text: rows(50) });
  await openWindow(page, "@13");
  const scroller = page.locator(".pane-reflow");
  await expect(scroller).toContainText("row 49");
  await scroller.evaluate((el) => (el.scrollTop = 0));
  await expect(page.locator(".load-earlier")).toBeHidden();
  // Exactly the tail size: the button shows, one load finds nothing more and hides it.
  await control.patchWindow(SHOP, "@13", { pane_text: rows(200) });
  await expect(scroller).toContainText("row 199", { timeout: 8000 });
  await scroller.evaluate((el) => (el.scrollTop = 0));
  const more = page.locator(".load-earlier");
  await expect(more).toBeVisible();
  await more.tap();
  await expect(more).toBeHidden();
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
  await expect(card).toContainText("reviewer");
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

test("a dispatch approval shows where and how the agent runs, and folds a long prompt", async ({ page, control }) => {
  const prompt = Array.from({ length: 40 }, (_, i) => `line ${i + 1} of the blog turnaround post`).join("\n");
  const id = await control.openApproval({
    kind: "dispatch", session: SHOP, window_id: "@14", asking_agent: "brandon", target_agent: "general-purpose", prompt,
    command: "agentctl dispatch --agent general-purpose --dir ~/code/personal/blsmth.com --prompt-key blog-turnaround-post",
    details: {
      project: "blsmth.com", dir: "~/code/personal/blsmth.com", dir_source: "flag", asking_project: "agentctl", other_project: true,
      agent: "general-purpose", agent_source: "built-in", model: "opus", model_source: "flag", backend: "", harness: "",
      account: "personal", mode: "ad-hoc", worktree: "worktree", permission_mode: "auto",
    },
  });
  await page.goto(`/#/approvals/${id}`);
  const card = page.locator("section.approval");
  await expect(card.locator(".approval-head strong")).toHaveText("brandon wants to dispatch general-purpose to blsmth.com");
  const project = card.locator('.approval-row[data-row="project"]');
  await expect(project).toContainText("blsmth.com");
  await expect(project).toContainText("~/code/personal/blsmth.com");
  await expect(project.locator(".approval-flag")).toHaveText("not agentctl");
  await expect(card.locator('.approval-row[data-row="model"]')).toContainText("opus");
  await expect(card.locator('.approval-row[data-row="account"]')).toContainText("personal");
  await expect(card.locator('.approval-row[data-row="mode"]')).toContainText("worktree · auto permissions");
  await expect(card.locator('.approval-row[data-row="backend"]')).toHaveCount(0);

  const pre = card.locator("pre.approval-text").first();
  await expect(pre).toHaveClass(/folded/);
  const toggle = card.getByRole("button", { name: "Show all 40 lines" });
  await toggle.tap();
  await expect(pre).not.toHaveClass(/folded/);
  await expect(card.getByRole("button", { name: "Show less" })).toBeVisible();
  const box = await pre.boundingBox();
  const full = await pre.evaluate((el) => el.scrollHeight);
  expect(Math.round(box!.height)).toBeGreaterThanOrEqual(full - 1);

  // An approvals update re-renders the list; the expanded prompt must stay open.
  await control.openApproval({ kind: "dispatch", session: SHOP, window_id: "@14", asking_agent: "brandon", target_agent: "general-purpose", command: "agentctl dispatch --prompt x" });
  await expect(page.locator("section.approval")).toHaveCount(1); // still scoped to this id's page
  await page.waitForTimeout(1500);
  await expect(card.locator("pre.approval-text").first()).not.toHaveClass(/folded/);
  await expect(card.getByRole("button", { name: "Show less" })).toBeVisible();

  // No confirm sheet for an approved dispatch: the tap sends the answer.
  await card.getByRole("button", { name: "Approve", exact: true }).tap();
  await expect(sheet(page)).toHaveCount(0);
  const actions = await control.waitForActions((a) => a.some((x) => x.kind === "approval"));
  expect(actions[0]).toMatchObject({ kind: "approval", id, decision: "allow" });
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
  await page.getByRole("button", { name: "Send reply" }).tap();
  await expect(sheet(page)).toHaveCount(0);
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

test("header: tapping the task line shows the full prompt, again collapses, and a poll keeps it open", async ({ page, control }) => {
  const prompt = "Cover the cart service\n\nWrite table tests for totals.\n  Keep <b>markup</b> literal.\n" + "more detail line\n".repeat(60);
  // An ad-hoc window: no ticket title or issue, a summary recap in place.
  await control.patchWindow(SHOP, "@13", { title: "", issue: 0, mode: "adhoc", task: "Cover the cart service", prompt, recap: "Writing table tests.", recap_source: "summary" });
  await openWindow(page, "@13");
  await expect(page.locator(".win-recap")).toHaveText("Writing table tests.");
  const task = page.locator(".win-task");
  await expect(task).toHaveClass(/\bexpandable\b/);
  const short = (await task.textContent()) ?? "";
  await task.tap();
  await expect(task).toHaveClass(/\bopen\b/);
  await expect(task).toContainText("Write table tests for totals.");
  await expect(task).toContainText("Keep <b>markup</b> literal.");
  expect(await task.evaluate((e) => getComputedStyle(e).whiteSpace)).toBe("pre-wrap");
  expect(await task.evaluate((e) => e.scrollHeight > e.clientHeight)).toBe(true);
  // Scroll, then let a poll re-render: it stays open and keeps its scroll.
  await task.evaluate((e) => { e.scrollTop = 40; });
  await control.patchWindow(SHOP, "@13", { activity: "go vet ./..." });
  await page.waitForTimeout(1500);
  await expect(task).toHaveClass(/\bopen\b/);
  expect(await task.evaluate((e) => e.scrollTop)).toBe(40);
  await task.tap();
  await expect(task).not.toHaveClass(/\bopen\b/);
  await expect(task).toHaveText(short);
});

test("header: a task line with nothing more to show is not tappable", async ({ page }) => {
  await openWindow(page, "@13");
  const task = page.locator(".win-task");
  await expect(task).not.toHaveClass(/\bexpandable\b/);
  await task.tap();
  await expect(task).not.toHaveClass(/\bopen\b/);
});
