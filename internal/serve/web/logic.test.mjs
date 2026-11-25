// Run with: node internal/serve/web/logic.test.mjs  (no dependencies; also run by `just web-test` and the CI web job).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  SWIPE_REVEAL, canSwipeClose, swipeAxis, swipeCommit, swipeOffset,
  ansiColor, approvalBannerText, approvalConfirm, approvalNeedsConfirm, approvalHash, approvalsForWindow, approvalSummary, buildApprovalRequest,
  approvalProject, approvalRows, approvalRowText, promptFold, PROMPT_FOLD_LINES,
  DISPATCH_UPLOAD_PATH, buildDispatchRequest, buildShellRequest, canShell, templateFill, templateLabel, templatesPath, choiceLabel, confirmLines, dispatchHash, formatCountdown, launchOptionsPath, modelPicker, normalizePane,
  parseAnsi, parseRoute, removeApproval, resolvedText, secondsLeft, splitPane, upsertApproval, withReceived,
  handleHash, handleApiPath, handleOutcome, windowWho,
  bucketLabel, kindLabel, rowTags, rowChips, rowTitle, rowHeadline, rowRole, detailTitle, truncateWords, agentChip, rowDetailLine, treeRows,
  isScheduled, scheduledChip, scheduledLine, treeLines, collapseKey, groupCounts, groupKey,
  buildCommandRequest, commandConfirm, commandError, commandSuggestions, commandWord, isClaudeWindow, isSlashCommand, SLASH_COMMANDS,
  actionsWithQueue, buildAnswerRequest, checkboxChoices, buildQueueRequest, buildReplyRequest, buildUploadPath, checkAttachment, MAX_IMAGES, dialogQuestion, exactRows, focusedOption, formatCost, hangingIndent, headerChips,
  sortGroups, groupKeys, moveKey, applyGroupOrder, sameOrder, dropIndex, autoscrollDelta, buildGroupOrderRequest,
  defaultAgentFor, liveness, paneLinesForExact, safeHttpsUrl, shortModel, stateLabel, taskLine, headerLines, fullPrompt,
  waitingIndex, parentOf, childrenOf, displayState, waitingLabel, actionsFor, effectiveState, attentionLabel, attentionLine,
  applyGone, shellRows, shellDirName, shellApiPath,
  formatBytes, formatUptime, hostLevels, hostConnection, pushSample, formatSkew,
  advancedSummary, cardLastFailure, cardStatus, changeLines, fieldErrors, runResultCard, timeoutIsPreset, usageOptions, formClientErrors, formatAgo, formatNextFire, newScheduleForm, previewBody, runResultLabel, scheduleBanners, scheduleBody,
  scheduleException, scheduleGlyph, scheduleHash, scheduleLastText, scheduleReview, scheduleWhen, worktreeFact,
  scheduleAgentText, schedulePromptLabel, scheduleRowActivity,
  isTap, isDoubleTap, paneLineCount, canLoadEarlier, historyAddsLines,
  buildSweepRequest, canSweep, sweepConfirm, sweepCounts, nothingToSweep, sweptMessage, runSweep,
  ARTIFACT_TOKEN_MAX_AGE_MS, artifactChipText, artifactHash, artifactIcon, artifactOrigin, artifactRevLabel, artifactRevPill, artifactRowMeta, artifactTokenStale,
  artifactWindowLive, artifactsForWindow, artifactsHash, groupArtifacts,
  ARTIFACT_COLLAPSE_KEY, ARTIFACT_OTHER, UNDO_MS, artifactGroupKey, artifactGroupMove, artifactRevBadge, artifactSubline, buildArtifactOrderRequest,
  createDeleteQueue, dropSettled, SETTLED_TTL_MS, loadCollapsedSet, mergeArtifactOrder, saveCollapsedSet,
  ARTIFACT_FEEDBACK_MAX, artifactCommentStatus, artifactCommentToast, artifactFeedbackFrom,
  artifactPdfLabel, artifactPdfPath, pdfDelivery, pdfFilename,
  isTrustBlocked, isTrustDialog, trustDialogOf, trustExitConfirm, trustFolder, TRUST_EXPLAIN, TRUST_TITLE,
} from "./logic.js";

// Slash commands (phone "Run command").
assert.equal(commandWord("/compact focus"), "/compact");
assert.equal(commandWord("  /clear"), "/clear");
assert.equal(commandWord("/Users/me/file.go is broken"), null);
assert.equal(commandWord("hello /x"), null);
assert.equal(commandWord("!ls"), null);
assert.equal(commandWord("#note"), null);
assert.equal(isSlashCommand("/"), true);
assert.equal(isSlashCommand("/plugin:skill a b"), true);
assert.deepEqual(commandSuggestions("/co").map((c) => c.name), ["/compact", "/context", "/cost"]);
assert.deepEqual(commandSuggestions("/CLE").map((c) => c.name), ["/clear"]);
assert.equal(commandSuggestions("/").length, SLASH_COMMANDS.length);
assert.deepEqual(commandSuggestions("/compact"), []);
assert.deepEqual(commandSuggestions("/compact x"), []);
assert.deepEqual(commandSuggestions("/Users/x"), []);
assert.deepEqual(commandSuggestions("hello"), []);
assert.equal(commandError("/compact x"), "");
assert.equal(commandError("  /plugin:skill-x "), "");
for (const bad of ["/", "/ x", "/-x", "/a\nb", "hello", "!x", "#x", "/a b".padEnd(2001, "x")]) assert.notEqual(commandError(bad), "", bad);
assert.equal(commandConfirm("/clear").title, "Clear this conversation?");
assert.equal(commandConfirm(" /clear ").danger, true);
assert.equal(commandConfirm("/compact"), null);
assert.equal(commandConfirm("/clearx"), null);
assert.equal(isClaudeWindow({}), true);
assert.equal(isClaudeWindow({ harness: "claude" }), true);
assert.equal(isClaudeWindow({ harness: "pi" }), false);
assert.deepEqual(buildCommandRequest("s", "@1", "/compact").body, { text: "/compact" });
assert.match(buildCommandRequest("s", "@1", "/compact").path, /\/command$/);

const fixtures = JSON.parse(readFileSync(new URL("./logic.fixtures.json", import.meta.url), "utf8"));

// Compact view of a normalised block: a plain string, or {rule: label}.
const view = (b) => (b.rule !== undefined ? { rule: b.rule } : b.runs.map((r) => r.t).join(""));

let n = 0;
for (const f of fixtures.normalize) {
  assert.deepEqual(normalizePane(f.input).map(view), f.expect, f.name);
  n++;
}
for (const f of fixtures.split) {
  const got = splitPane(f.input);
  assert.deepEqual(got.body.map(view), f.body, `${f.name}: body`);
  assert.deepEqual(got.status ? got.status.lines : null, f.status, `${f.name}: status`);
  if (f.prompt !== undefined) assert.deepEqual(got.prompt, f.prompt, `${f.name}: prompt`);
  n++;
}
for (const f of fixtures.exact) {
  assert.deepEqual(paneLinesForExact(f.input), f.expect, f.name);
  n++;
}
for (const f of fixtures.sgr) {
  assert.deepEqual(parseAnsi(f.input)[0], f.expect, f.name);
  n++;
}
assert.equal(ansiColor(1), "#cd3131");
assert.equal(ansiColor(16), "#000000");
assert.equal(ansiColor(196), "#ff0000");
assert.equal(ansiColor(232), "#080808");

// Group order: the server's order wins over urgency; windows still sort.
{
  const win = (id, state) => ({ window_id: id, state });
  const gs = sortGroups([
    { key: "/idle", windows: [win("@1", "idle")] },
    { key: "/blocked", windows: [win("@2", "idle"), win("@3", "blocked")] },
  ]);
  assert.deepEqual(gs.map((g) => g.key), ["/idle", "/blocked"]);
  assert.deepEqual(gs.map((g) => g.rollup), ["idle", "blocked"]);
  assert.deepEqual(gs[1].windows.map((w) => w.window_id), ["@3", "@2"]);
  assert.deepEqual(groupKeys([{ key: "/a" }, {}, { key: "" }, { key: "/b" }]), ["/a", "/b"]);
  assert.deepEqual(moveKey(["a", "b", "c"], "a", 2), ["b", "c", "a"]);
  assert.deepEqual(moveKey(["a", "b", "c"], "c", 0), ["c", "a", "b"]);
  assert.deepEqual(moveKey(["a", "b"], "b", 9), ["a", "b"]);
  assert.deepEqual(moveKey(["a", "b"], "x", 0), ["a", "b"]);
  assert.deepEqual(applyGroupOrder([{ key: "a" }, { key: "b" }, { key: "c" }], ["c", "a"]).map((g) => g.key), ["c", "a", "b"]);
  assert.equal(sameOrder(["a", "x", "b"], ["b", "a"]), false);
  assert.equal(sameOrder(["a", "x", "b"], ["a", "b", "y"]), true);
  assert.equal(dropIndex([10, 30, 50], 40), 2);
  assert.equal(dropIndex([10, 30, 50], 5), 0);
  assert.equal(autoscrollDelta(300, 600), 0);
  assert.ok(autoscrollDelta(10, 600) < 0 && autoscrollDelta(595, 600) > 0);
  assert.ok(autoscrollDelta(0, 600) < autoscrollDelta(40, 600));
  assert.deepEqual(buildGroupOrderRequest(["/a"]), { method: "POST", path: "/v1/group-order", body: { keys: ["/a"] } });
  n += 17;
}

console.log(`ok: ${n} fixtures`);

// Launch options helpers.
assert.deepEqual(buildDispatchRequest("a", "/d", "p").body, { agent: "a", project_dir: "/d", prompt: "p" });
assert.deepEqual(buildDispatchRequest("a", "/d", "p", { backend: "local", model: "", effort: "high" }).body, {
  agent: "a", project_dir: "/d", prompt: "p", backend: "local", effort: "high",
});
assert.equal(launchOptionsPath("a b", "/x&y"), "/v1/launch-options?agent=a%20b&project_dir=%2Fx%26y");
const lo = {
  models: {
    anthropic: { models: [{ value: "inherit", note: "", default: true }, { value: "opus", note: "alias", default: false }] },
    local: { models: [{ value: "qwen3", note: "", default: true }], model_list_error: "timed out" },
  },
};
assert.deepEqual(modelPicker(lo, "anthropic").def, "inherit");
assert.equal(modelPicker(lo, "anthropic").freeText, false);
assert.equal(modelPicker(lo, "local").freeText, true);
assert.equal(modelPicker(lo, "local").def, "qwen3");
assert.equal(modelPicker(lo, "missing").choices.length, 0);
assert.equal(choiceLabel({ value: "opus", note: "alias", default: false }), "opus (alias)");
assert.equal(choiceLabel({ value: "inherit", note: "", default: true }), "inherit \u2605 default");
assert.deepEqual(confirmLines({ agent: "a", project_dir: "/d", prompt: "p", backend: "local", model: "m", effort: "low" }), [
  "agent: a\nproject: /d\nbackend: local\nmodel: m\neffort: low", "p",
]);
assert.deepEqual(confirmLines({ agent: "a", project_dir: "/d", prompt: "p" }), ["agent: a\nproject: /d", "p"]);
assert.deepEqual(confirmLines({ agent: "a", project_dir: "/d", prompt: "p" }, 2), ["agent: a\nproject: /d\nimages: 2", "p"]);
assert.deepEqual(buildDispatchRequest("a", "/d", "p", {}, ["x.png"]).body, { agent: "a", project_dir: "/d", prompt: "p", images: ["x.png"] });
assert.deepEqual(buildDispatchRequest("a", "/d", "p", {}, []).body, { agent: "a", project_dir: "/d", prompt: "p" });
assert.deepEqual(buildDispatchRequest("a", "/d", "p", {}, [], "  be terse \n").body, { agent: "a", project_dir: "/d", prompt: "p", instructions: "be terse" });
assert.deepEqual(buildDispatchRequest("a", "/d", "p", {}, [], "  \n").body, { agent: "a", project_dir: "/d", prompt: "p" });
assert.deepEqual(buildDispatchRequest("a", "/d", "p", {}, [], undefined).body, { agent: "a", project_dir: "/d", prompt: "p" });
assert.deepEqual(buildDispatchRequest("a", "/d", "p", {}, [], "", true).body, { agent: "a", project_dir: "/d", prompt: "p", skip_review: true });
assert.deepEqual(buildDispatchRequest("a", "/d", "p", {}, [], "", false).body, { agent: "a", project_dir: "/d", prompt: "p" });
assert.ok(confirmLines({ agent: "a", project_dir: "/d", prompt: "p", skip_review: true })[0].includes("code review: skipped"));
assert.deepEqual(confirmLines({ agent: "a", project_dir: "/d", prompt: "p", instructions: "be terse" }), [
  "agent: a\nproject: /d", "p", "additional instructions (system prompt):\nbe terse",
]);
assert.equal(confirmLines({ agent: "a", project_dir: "/d", prompt: "p" }).length, 2);
assert.equal(DISPATCH_UPLOAD_PATH, "/v1/dispatch/upload");
console.log("ok: launch option helpers");

assert.deepEqual(parseRoute("#/dispatch"), { name: "dispatch" });
assert.deepEqual(parseRoute("#/dispatch/%2Fa%20b%2Fc"), { name: "dispatch", dir: "/a b/c" });
assert.deepEqual(parseRoute("#/dispatch/%E0%A4%A"), { name: "dispatch" });
assert.equal(dispatchHash("/a b/c"), "#/dispatch/%2Fa%20b%2Fc");
assert.deepEqual(parseRoute(dispatchHash("/x y/z&w")), { name: "dispatch", dir: "/x y/z&w" });
console.log("ok: dispatch route");
// Remote approvals.
assert.deepEqual(parseRoute("#/approvals"), { name: "approvals", id: "" });
assert.deepEqual(parseRoute("#/approvals/abc123"), { name: "approvals", id: "abc123" });
assert.deepEqual(parseRoute("#/approvals/%E0%A4%A"), { name: "approvals", id: "" });
assert.equal(approvalHash("ab"), "#/approvals/ab");
assert.equal(approvalHash(""), "#/approvals");
const ap = {
  id: "a1", kind: "dispatch", asking_agent: "orch", session: "agentctl-p", project: "p", window_id: "@5",
  target_agent: "go-engineer", issue: 0, prompt: "do it", command: "agentctl dispatch -k k",
  opened: "2026-11-17T10:00:00Z", seconds_remaining: 540, payload_sha256: "h1",
};
assert.deepEqual(buildApprovalRequest(ap, "allow"), {
  method: "POST", path: "/v1/approvals/a1", body: { decision: "allow", payload_sha256: "h1" },
});
assert.deepEqual(buildApprovalRequest(ap, "deny").body.decision, "deny");
assert.throws(() => buildApprovalRequest(ap, "yes"));
assert.throws(() => buildApprovalRequest({ id: "a1" }, "allow"));
assert.equal(approvalSummary(ap), "orch wants to dispatch go-engineer");
assert.equal(approvalSummary({ kind: "bash", asking_agent: "w" }), "w wants to run a command");
assert.equal(approvalSummary({ kind: "cron", asking_agent: "w", target_agent: "go-engineer" }), "w wants to schedule go-engineer");
assert.equal(approvalBannerText(0), "");
assert.equal(approvalBannerText(1), "1 approval waiting");
assert.equal(approvalBannerText(3), "3 approvals waiting");
assert.equal(formatCountdown(539), "8:59");
assert.equal(formatCountdown(-3), "0:00");
const stamped = withReceived(ap, 1000);
assert.equal(secondsLeft(stamped, 1000), 540);
assert.equal(secondsLeft(stamped, 61500), 480);
assert.equal(secondsLeft(stamped, 10_000_000), 0);
const b = { ...ap, id: "b2", opened: "2026-11-17T09:00:00Z", window_id: "@6" };
let list = upsertApproval([], ap);
list = upsertApproval(list, b);
assert.deepEqual(list.map((x) => x.id), ["b2", "a1"]);
list = upsertApproval(list, { ...ap, prompt: "x" });
assert.equal(list.length, 2);
assert.deepEqual(approvalsForWindow(list, "agentctl-p", "@5").map((x) => x.id), ["a1"]);
assert.deepEqual(removeApproval(list, "a1").map((x) => x.id), ["b2"]);
assert.equal(approvalNeedsConfirm({ kind: "dispatch" }, "allow"), false);
assert.equal(approvalNeedsConfirm({ kind: "dispatch" }, "deny"), true);
assert.equal(approvalNeedsConfirm({ kind: "cron" }, "allow"), true);
assert.equal(approvalNeedsConfirm({ kind: "bash" }, "allow"), true);
assert.equal(approvalNeedsConfirm({ kind: "template" }, "allow"), true);
const conf = approvalConfirm(ap, "allow");
assert.equal(conf.confirmLabel, "Approve");
assert.ok(!conf.lines.some((l) => /window: @/.test(l)), "approval confirm must not show the raw window id");
assert.equal(conf.danger, false);
assert.ok(conf.lines.includes("do it") && conf.lines.includes("agentctl dispatch -k k"));
assert.equal(approvalConfirm({ ...ap, kind: "bash", prompt: "" }, "deny").danger, true);
const cronConf = approvalConfirm({ ...ap, kind: "cron", prompt: "Job: nightly" }, "allow");
assert.equal(cronConf.title, "Approve this schedule?");
assert.ok(cronConf.lines[0].includes("scheduled agent: go-engineer") && cronConf.lines.includes("Job: nightly"));

// Dispatch where/how details.
const details = {
  project: "blsmth.com", dir: "~/code/personal/blsmth.com", dir_source: "flag", asking_project: "agentctl", other_project: true,
  agent: "general-purpose", agent_source: "built-in", model: "opus", model_source: "agent file", backend: "", harness: "",
  account: "personal (Personal)", mode: "ad-hoc", worktree: "worktree", permission_mode: "auto",
};
const apd = { ...ap, target_agent: "general-purpose", details };
assert.equal(approvalSummary(apd), "orch wants to dispatch general-purpose to blsmth.com");
assert.equal(approvalSummary({ ...ap, details: { ...details, project: "" } }), "orch wants to dispatch general-purpose");
assert.equal(approvalSummary({ kind: "dispatch", asking_agent: "orch", target_agent: "x", target_project: "site" }), "orch wants to dispatch x to site");
assert.equal(approvalProject({ target_project: "site" }), "site");
assert.equal(approvalProject({}), "");
assert.deepEqual(approvalRows(apd), [
  { label: "Project", value: "blsmth.com", detail: "~/code/personal/blsmth.com", flag: "not agentctl" },
  { label: "Agent", value: "general-purpose", detail: "built-in", flag: "" },
  { label: "Model", value: "opus", detail: "agent file", flag: "" },
  { label: "Account", value: "personal (Personal)", detail: "", flag: "" },
  { label: "Mode", value: "ad-hoc", detail: "worktree · auto permissions", flag: "" },
]);
const sameProject = approvalRows({ ...apd, details: { ...details, other_project: false, backend: "nous", harness: "pi", account: "", permission_mode: "" } });
assert.deepEqual(sameProject.map((r) => r.label), ["Project", "Agent", "Model", "Backend", "Harness", "Mode"]);
assert.equal(sameProject[0].flag, "");
assert.equal(sameProject.at(-1).detail, "worktree");
// No details (older hook) or another kind: no rows, the old facts stay.
assert.deepEqual(approvalRows(ap), []);
assert.deepEqual(approvalRows({ ...ap, kind: "bash", details }), []);
assert.equal(approvalRowText({ label: "Project", value: "blsmth.com", detail: "~/x", flag: "not agentctl" }), "project: blsmth.com (~/x · ⚠ not agentctl)");
assert.equal(approvalRowText({ label: "Model", value: "default", detail: "", flag: "" }), "model: default");
const confd = approvalConfirm(apd, "allow");
assert.ok(confd.lines[0].startsWith("orch wants to dispatch general-purpose to blsmth.com\n"));
assert.ok(confd.lines[0].includes("project: blsmth.com (~/code/personal/blsmth.com · ⚠ not agentctl)"));
assert.ok(confd.lines[0].includes("model: opus (agent file)"));
assert.ok(!confd.lines[0].includes("target agent:"));
assert.ok(conf.lines[0].includes("target agent: go-engineer"));
// Long prompts fold.
assert.deepEqual(promptFold(""), { lines: 0, fold: false, label: "Show all" });
assert.equal(promptFold("one").fold, false);
assert.equal(promptFold(Array(PROMPT_FOLD_LINES).fill("x").join("\n")).fold, false);
assert.deepEqual(promptFold(Array(PROMPT_FOLD_LINES + 1).fill("x").join("\n")),
  { lines: PROMPT_FOLD_LINES + 1, fold: true, label: `Show all ${PROMPT_FOLD_LINES + 1} lines` });
// One very long line folds too, without claiming "1 line".
assert.deepEqual(promptFold("x".repeat(PROMPT_FOLD_LINES * 80 + 1)), { lines: 1, fold: true, label: "Show all" });
// Unknown project (the command changes dir first) is a flagged row, and no "to ..." in the title.
const unknownDir = { ...apd, details: { ...details, project: "", dir: "", asking_project: "", other_project: false, dir_source: "command changes dir first" } };
assert.deepEqual(approvalRows(unknownDir)[0], { label: "Project", value: "unknown", detail: "", flag: "command changes dir first" });
assert.equal(approvalSummary(unknownDir), "orch wants to dispatch general-purpose");
console.log("ok: approval helpers");

// Resolved approval text.
const T0 = Date.parse("2026-11-17T10:10:00Z");
const res = (o) => ({ opened: "2026-11-17T10:00:00Z", resolved_at: "2026-11-17T10:08:00Z", ...o });
assert.equal(resolvedText(res({ outcome: "denied", source: "laptop" }), T0), "Already answered on the laptop: denied · 2m ago");
assert.equal(resolvedText(res({ outcome: "answered", source: "laptop" }), T0), "Already answered on the laptop · 2m ago");
assert.equal(resolvedText(res({ outcome: "approved", source: "device-00bb" }), T0), "Approved from device-00bb · 2m ago");
assert.equal(resolvedText(res({ outcome: "denied", source: "device-00bb" }), T0), "Denied from device-00bb · 2m ago");
assert.equal(resolvedText(res({ outcome: "timed_out", resolved_at: "2026-11-17T10:09:00Z" }), T0), "Timed out after 9m · 1m ago");
assert.equal(resolvedText(res({ outcome: "timed_out", resolved_at: "2026-11-17T10:00:30Z" }), T0), "Timed out after 30s · 9m ago");
assert.equal(resolvedText(res({ outcome: "cancelled" }), T0), "No longer waiting · 2m ago");
assert.equal(resolvedText({ outcome: "weird" }, T0), "No longer waiting");
console.log("ok: resolved text");

// Swipe-to-close.
{
  const idle = { state: "idle" };
  const done = { state: "idle", done: true };
  const feats = ["read", "write"];
  assert.equal(canSwipeClose(idle, "s", feats), true);
  assert.equal(canSwipeClose(done, "s", feats), true);
  assert.equal(canSwipeClose({ state: "working" }, "s", feats), false);
  assert.equal(canSwipeClose({ state: "blocked", done: true }, "s", feats), false);
  assert.equal(canSwipeClose(idle, "s", ["read"]), false);
  assert.equal(canSwipeClose(idle, "s", undefined), false);
  assert.equal(canSwipeClose(idle, "agentctl-hub", feats), false);
  {
    const parent = { window_id: "@1", session: "s", state: "idle", done: true };
    const kid = (state, extra = {}) => ({ window_id: "@2", session: "s", state, parent: "@1", ...extra });
    const busy = waitingIndex([{ windows: [parent, kid("working")] }]);
    assert.equal(canSwipeClose(parent, "s", feats, busy), false);
    assert.equal(canSwipeClose(parent, "s", feats, waitingIndex([{ windows: [parent, kid("idle", { done: true })] }])), true);
    assert.equal(canSwipeClose({ ...parent, waiting_on: busy.get("@1") }, "s", feats), false);
  }
  assert.equal(swipeAxis(3, 4), "");
  assert.equal(swipeAxis(-12, 4), "x");
  assert.equal(swipeAxis(5, 14), "y");
  assert.equal(swipeAxis(-11, 11), "y");
  assert.equal(swipeOffset(30, 300), 0);
  assert.equal(swipeOffset(-100, 300), -100);
  assert.equal(swipeOffset(-400, 300), -300);
  assert.equal(swipeCommit(-120, 300), true);
  assert.equal(swipeCommit(-100, 300), false);
  assert.equal(swipeCommit(-60, 300, -1), true);
  assert.equal(swipeCommit(-20, 300, -2), false);
  assert.equal(swipeCommit(50, 300, -2), false);
  assert.equal(swipeCommit(-200, 0), false);
  assert.equal(SWIPE_REVEAL, 88);
  console.log("ok: swipe close");
}

{
  assert.equal(rowRole({ role: "build" }), "build");
  assert.equal(rowRole({}), "");
  assert.equal(rowRole({ role: "<b>x</b>" }), "");
  assert.equal(rowRole({ kind: "go" }), "");
  console.log("ok: rowRole");
}

{
  assert.deepEqual(detailTitle({ agent: "go-engineer:3", title: " Fix retry ", role: "build" }, "@5"), { text: "Fix retry", agent: "go-engineer", role: "build" });
  assert.deepEqual(detailTitle({ agent: "brandon:2", title: "Wire titles" }, "@5"), { text: "Wire titles", agent: "brandon", role: "" });
  assert.deepEqual(detailTitle({ agent: "brandon:2", role: "build", recap: "Some recap sentence." }, "@5"), { text: "brandon:2", agent: "", role: "" });
  assert.deepEqual(detailTitle({}, "@5"), { text: "@5", agent: "", role: "" });
  assert.deepEqual(detailTitle(undefined, "@5"), { text: "@5", agent: "", role: "" });
  assert.deepEqual(detailTitle({ agent: "go-engineer:3", label: "#306 · names" }, "@5"), { text: "#306 · names", agent: "", role: "" });
  console.log("ok: detailTitle");
}

{
  const ticket = { agent: "go-engineer:3", recap: "#7 Add a checkout retry", recap_source: "task", activity: "go test" };
  assert.equal(rowHeadline(ticket), "Add a checkout retry");
  assert.equal(agentChip(ticket), "go-engineer");
  assert.equal(rowDetailLine(ticket), "go test");
  const summary = { agent: "brandon:55", recap: "Wiring recaps into the phone app. Next up: tests.", recap_source: "summary", activity: "ls" };
  assert.equal(rowHeadline(summary), "Wiring recaps into the phone app");
  assert.equal(rowDetailLine(summary), summary.recap);
  assert.equal(agentChip(summary), "brandon");
  const long = rowHeadline({ agent: "a", recap: "word ".repeat(40), recap_source: "title" });
  assert.ok(long.length <= 80 && long.endsWith("…"));
  assert.ok(/(^| )word…$/.test(long), `cut mid-word: ${long}`);
  assert.equal(truncateWords("Fixing the hub sort order", 11), "Fixing the…");
  assert.equal(truncateWords("Rebuilt sort, then tests", 14), "Rebuilt sort…");
  assert.equal(truncateWords("https://github.com/x/y", 10), "https://g…");
  assert.equal(truncateWords("short", 10), "short");
  const act = { agent: "brandon:55", recap: "go test ./...", recap_source: "activity", activity: "go test ./..." };
  assert.equal(rowHeadline(act), "brandon:55");
  assert.equal(agentChip(act), "");
  assert.equal(rowDetailLine(act), "go test ./...");
  // A generated title over an activity row is the headline.
  const actTitled = { agent: "claude:2", recap: "Bash: go test ./...", recap_source: "activity", title: "Fix flaky tests", activity: "Bash: go test ./..." };
  assert.equal(rowHeadline(actTitled), "Fix flaky tests");
  assert.equal(rowDetailLine(actTitled), "Bash: go test ./...");
  // A title standing in for a missing recap is not repeated on the second line.
  const standIn = { agent: "claude:2", recap: "Fix flaky tests", recap_source: "title", title: "", activity: "" };
  assert.equal(rowHeadline(standIn), "Fix flaky tests");
  assert.equal(rowDetailLine(standIn), "");
  const none = { agent: "", activity: "" };
  assert.equal(rowHeadline(none), "Claude");
  assert.equal(agentChip(none), "");
  assert.equal(rowDetailLine(none), "");
  // A generated short title wins the headline; the full recap stays the second line.
  const titled = { ...summary, title: "Wiring Phone Recaps" };
  assert.equal(rowHeadline(titled), "Wiring Phone Recaps");
  assert.equal(rowDetailLine(titled), summary.recap);
  assert.equal(agentChip(titled), "brandon");
  const titledTask = { agent: "a:1", recap: "please sort every photo into albums by year", recap_source: "task", title: "Sorting Photos by Year", activity: "ls" };
  assert.equal(rowHeadline(titledTask), "Sorting Photos by Year");
  assert.equal(rowDetailLine(titledTask), "ls");
  // The generated title shows over an activity recap too (a bare window's first prompt).
  assert.equal(rowHeadline({ agent: "a:1", recap: "go test", recap_source: "activity", title: "Testing Things" }), "Testing Things");
  console.log("ok: row headline");
}

{
  assert.equal(rowTitle({ agent: "go-engineer" }), "go-engineer");
  assert.equal(rowTitle({ agent: "" }), "Claude");
  assert.deepEqual(rowTags({}), []);
  assert.deepEqual(rowTags({ issue: 42 }), ["#42"]);
  assert.deepEqual(rowTags({ issue: 42, pr_number: 99, pr_url: "https://github.com/o/r/pull/7" }), ["#42", "PR #99"]);
  assert.deepEqual(rowTags({ pr_url: "https://github.com/o/r/pull/7" }), ["PR #7"]);
  assert.deepEqual(rowTags({ pr_url: "https://example.com/x" }), ["PR"]);
  assert.equal(kindLabel({ mode: "dispatch" }), "task");
  assert.equal(kindLabel({ mode: "adhoc" }), "task");
  assert.equal(kindLabel({ mode: "bare" }), "session");
  assert.equal(kindLabel({}), "session");
  assert.equal(kindLabel({ mode: "adhoc", detached: true }), "session");
  assert.equal(bucketLabel(2), "Tasks from other sessions (2)");
  const bucket = treeRows([{ window_id: "@1", mode: "dispatch", parent: "@9" }]).find((e) => e.bucket);
  assert.equal(bucket.count, 1);
  console.log("ok: fleet row labels");
}

// Shared tree cases (internal/tui/testdata/tree_cases.json, also run by Go's
// TestTreeRows): the JS port must flatten them identically.
{
  const tree = JSON.parse(readFileSync(new URL("../../tui/testdata/tree_cases.json", import.meta.url), "utf8"));
  const prefix = (e) => {
    if (e.depth === 0) return "";
    const { through, last } = treeLines(e);
    return through.map((c) => (c ? "│  " : "   ")).join("") + (last ? "└─ " : "├─ ");
  };
  for (const c of tree.cases) {
    const wins = c.rows.map((r) => ({
      window_id: r.window_id, parent: r.parent, mode: r.dispatched ? "dispatch" : "", state: r.state,
      window_index: r.index, detached: r.detached, scheduled_by: r.scheduled_by ?? "",
    }));
    const got = treeRows(wins).map((e) => (e.bucket ? { label: e.label } : { id: e.win.window_id, depth: e.depth, prefix: prefix(e) }));
    assert.deepEqual(got, c.want, c.name);
  }
  console.log(`ok: ${tree.cases.length} shared tree cases`);
}

// parentOf / childrenOf: fleet-feed parent and child links.
{
  const groups = [
    { session: "a", windows: [
      { window_id: "@1", session: "a", state: "idle" },
      { window_id: "@2", session: "a", parent: "@1", state: "idle", window_index: 2 },
      { window_id: "@3", session: "a", parent: "@1", state: "working", window_index: 3 },
      { window_id: "@4", session: "a", parent: "@4", state: "idle" },
      { window_id: "@5", session: "a", parent: "@9", state: "idle" },
      { window_id: "@6", session: "a", parent: "@7", state: "idle" },
      { window_id: "@7", session: "a", parent: "@6", state: "idle" },
    ] },
    { session: "b", windows: [{ window_id: "@8", session: "b", parent: "@1", state: "idle", window_index: 1 }] },
  ];
  const win = (id) => groups.flatMap((g) => g.windows).find((w) => w.window_id === id);
  assert.equal(parentOf(groups, win("@1")), null);
  assert.equal(parentOf(groups, win("@2")).window_id, "@1");
  assert.equal(parentOf(groups, win("@8")).window_id, "@1"); // other group
  assert.equal(parentOf(groups, win("@4")), null); // self
  assert.equal(parentOf(groups, win("@5")), null); // gone
  assert.equal(parentOf(groups, win("@6")).window_id, "@7"); // cycle: terminates
  assert.deepEqual(childrenOf(groups, win("@1")).map((w) => w.window_id), ["@3", "@8", "@2"]);
  assert.deepEqual(childrenOf(groups, win("@4")), []);
  assert.deepEqual(childrenOf(groups, win("@7")).map((w) => w.window_id), ["@6"]);
  assert.equal(parentOf(undefined, win("@2")), null);
  assert.deepEqual(childrenOf(null, win("@1")), []);
  assert.deepEqual(childrenOf(groups, { parent: "@1" }), []);
  console.log("ok: parentOf/childrenOf");
}

// Scheduled (cron) windows.
{
  assert.equal(isScheduled({}), false);
  assert.equal(isScheduled({ scheduled_by: "nightly-triage" }), true);
  assert.equal(scheduledChip({}), "");
  assert.equal(scheduledChip({ scheduled_by: "nightly-triage" }), "⏲ nightly-triage");
  assert.equal(bucketLabel(3, "scheduled"), "Scheduled jobs (3)");
  assert.equal(bucketLabel(3, "dispatched"), "Tasks from other sessions (3)");
  assert.equal(scheduledLine({}), "");
  assert.equal(scheduledLine({ scheduled_by: "nightly-triage" }), "Scheduled run: nightly-triage");
  // Local-time fields, so the expectation is built the same way in any zone.
  const at = new Date(2026, 10, 18, 7, 0);
  assert.equal(scheduledLine({ scheduled_by: "nightly-triage", scheduled_at: at.toISOString() }), "Scheduled run: nightly-triage, fired Wed 18 Nov 07:00");
  const entries = treeRows([
    { window_id: "@1", mode: "adhoc", parent: "" },
    { window_id: "@2", mode: "adhoc", scheduled_by: "nightly-triage" },
  ]);
  const buckets = entries.filter((e) => e.bucket).map((e) => [e.kind, e.count]);
  assert.deepEqual(buckets, [["dispatched", 1], ["scheduled", 1]]);
  console.log("ok: scheduled windows");
}

// Window view helpers.
{
  const dialog = {
    text: "Bash command\ngo test ./...\nDo you want to proceed?\n1. Yes\n2. No, and tell Claude what to do differently",
    hash: "h",
    options: [{ key: "1", label: "Yes" }, { key: "2", label: "No, and tell Claude what to do differently" }],
  };
  assert.equal(dialogQuestion(dialog), "Do you want to proceed?");
  const multi = { ...dialog, multi: true };
  assert.throws(() => buildAnswerRequest("s", "@1", multi, "1"), /multi-select/);
  assert.equal(buildAnswerRequest("s", "@1", multi, "esc").body.key, "esc");
  // Multi-select submit: one request carrying the chosen keys.
  assert.throws(() => buildAnswerRequest("s", "@1", multi, "submit", ["1"]), /cannot be submitted/);
  const withSubmit = { ...multi, submit: { label: "Next", moves: 3 } };
  const sub = buildAnswerRequest("s", "@1", withSubmit, "submit", ["2", "1"]);
  assert.deepEqual(sub.body, { key: "submit", dialog_hash: "h", select: ["2", "1"] });
  assert.equal(buildAnswerRequest("s", "@1", withSubmit, "submit").body.select.length, 0);
  assert.throws(() => buildAnswerRequest("s", "@1", dialog, "submit", []), /cannot be submitted/);
  assert.deepEqual(
    checkboxChoices({ options: [
      { key: "1", label: "[ ] A" }, { key: "2", label: "[✔] B" }, { key: "3", label: "[ ] Type something" },
      { key: "4", label: "Chat about this" },
    ] }),
    [{ key: "1", label: "A", checked: false }, { key: "2", label: "B", checked: true }],
  );
  assert.equal(dialogQuestion({ text: "Edit file\nfoo.go\n1. Yes\n2. No" }), "foo.go");
  assert.equal(dialogQuestion({ text: "" }), "");
  assert.equal(focusedOption("│ \x1b[36m❯ 1. Yes\x1b[0m │\n│   2. No │", dialog), "1");
  assert.equal(focusedOption("❯ 2. No", dialog), "2");
  assert.equal(focusedOption("❯ 7. Other", dialog), "");
  assert.equal(focusedOption("❯ run tests", dialog), "");

  assert.equal(shortModel("claude-sonnet-4-20250514"), "sonnet-4");
  assert.equal(shortModel("claude-opus-4-1"), "opus-4-1");
  assert.equal(shortModel("claude-opus-4-1[1m]"), "opus-4-1");
  assert.equal(shortModel("qwen3:32b"), "qwen3:32b");
  assert.equal(shortModel(undefined), "");
  assert.equal(formatCost(0.98), "$0.98");
  assert.equal(formatCost(12.3), "$12.30");
  assert.equal(formatCost(0.001), "<$0.01");
  assert.equal(formatCost(0), "");

  assert.equal(taskLine({ detail: { title: " Cover the cart " }, issue: 9, activity: "x" }), "Cover the cart");
  assert.equal(taskLine({ detail: {}, issue: 9, activity: "x" }), "Issue #9");
  assert.equal(taskLine({ ticket: "42", activity: "x" }), "Issue #42");
  assert.equal(taskLine({ activity: "go test" }), "");
  assert.equal(taskLine({}), "");

  const hl = (w) => headerLines(w);
  assert.deepEqual(hl({ recap: "Fixing the cart totals.", recap_source: "summary", activity: "Bash" }), { task: "", recap: "Fixing the cart totals." });
  assert.deepEqual(hl({ detail: { title: "Cover the cart" }, recap: "Writing tests.", recap_source: "summary", activity: "Bash" }), { task: "Cover the cart", recap: "Writing tests." });
  assert.deepEqual(hl({ recap: "Cart totals", recap_source: "title", activity: "Bash" }), { task: "", recap: "Cart totals" });
  assert.deepEqual(hl({ recap: "Bash", recap_source: "activity", activity: "Bash" }), { task: "Bash", recap: "" });
  assert.deepEqual(hl({ activity: "go test" }), { task: "go test", recap: "" });
  assert.deepEqual(hl({ detail: { title: "Cover the cart" }, activity: "go test" }), { task: "Cover the cart", recap: "" });
  assert.deepEqual(hl({ recap: "#7 Add a retry", recap_source: "task", issue: 7, activity: "Edit" }), { task: "Issue #7", recap: "" });
  assert.deepEqual(hl({ recap: "#7 Add a retry", recap_source: "task", activity: "Edit" }), { task: "Add a retry", recap: "" });
  assert.deepEqual(hl({}), { task: "", recap: "" });
  // Ad-hoc: recorded task survives a summary recap; bare: first prompt line beats the activity fallback.
  assert.deepEqual(hl({ detail: { task: "Fix cart", prompt: "Fix cart\nmore" }, recap: "Working.", recap_source: "summary", activity: "Bash" }), { task: "Fix cart", recap: "Working." });
  assert.deepEqual(hl({ detail: { prompt: "\n Do the thing\nmore" }, activity: "go test" }), { task: "Do the thing", recap: "" });

  assert.equal(fullPrompt({ detail: { prompt: " Fix the bug\n\nin detail " } }, "Fix the bug"), "Fix the bug\n\nin detail");
  assert.equal(fullPrompt({ detail: { prompt: "Fix  the\nbug" } }, "Fix the bug"), "");
  assert.equal(fullPrompt({ detail: {} }, "x"), "");
  assert.equal(fullPrompt({}, "x"), "");
  assert.equal(fullPrompt({ detail: { prompt: "p" } }, ""), "p");

  const now = Date.parse("2026-11-17T10:03:00Z");
  assert.equal(stateLabel({ state: "working", state_since: "2026-11-17T10:00:00Z" }, now), "working for 3m");
  assert.equal(stateLabel({ state: "blocked", blocked_since: "2026-11-17T10:02:30Z", state_since: "x" }, now), "blocked for 30s");
  assert.equal(stateLabel({ state: "idle", done: true }, now), "done");

  const chips = headerChips({
    pr_url: "https://github.com/o/r/pull/8", context_pct: 41.6, window_id: "@12", mode: "dispatch",
    detail: { cost_usd: 1.84, model: "claude-opus-4-1" },
  }, safeHttpsUrl);
  assert.deepEqual(chips.map((c) => c.text), ["PR #8", "42% ctx", "$1.84", "opus-4-1", "dispatch"]);
  assert.equal(chips[0].href, "https://github.com/o/r/pull/8");
  assert.deepEqual(headerChips({ pr_url: "javascript:alert(1)", pr_number: 3, window_id: "@1", handle: "a3f9" }, safeHttpsUrl).map((c) => c.text), ["a3f9"]);
  assert.deepEqual(headerChips({ window_id: "@1" }, safeHttpsUrl), []);
  assert.deepEqual(headerChips({ pr_url: "https://x.test/p", pr_number: 3 }, safeHttpsUrl).map((c) => c.text), ["PR #3"]);

  const t0 = Date.parse("2026-11-17T10:00:00Z");
  assert.deepEqual(liveness({}, t0), { text: "", kind: "stale" });
  assert.deepEqual(liveness({ lastChangeAt: t0 - 4000 }, t0), { text: "updated 4s ago", kind: "fresh" });
  assert.deepEqual(liveness({ lastChangeAt: t0 - 45000 }, t0), { text: "updated 45s ago", kind: "stale" });
  assert.deepEqual(liveness({ lastChangeAt: t0 - 180000 }, t0), { text: "updated 3m ago", kind: "stale" });
  assert.equal(liveness({ lastChangeAt: t0, offlineSince: t0 - 1000 }, t0).kind, "offline");
  assert.match(liveness({ offlineSince: t0 }, t0).text, /^offline since \d\d:\d\d$/);

  assert.equal(hangingIndent("plain text"), 2);
  assert.equal(hangingIndent("● Bash(go test)"), 2);
  assert.equal(hangingIndent("  ⎿  ok  github.com/x"), 5);
  assert.equal(hangingIndent("      41      srv := x"), 6);
  assert.equal(hangingIndent(" ".repeat(40) + "x"), 16);

  assert.equal(exactRows(50, 24, 600, 15), 24);
  assert.equal(exactRows(10, 24, 600, 15), 10);
  assert.equal(exactRows(50, 60, 300, 15), 20);
  assert.equal(exactRows(50, 24, 0, 15), 24);
  assert.equal(exactRows(0, 24, 300, 15), 1);

  const working = { state: "working" };
  assert.equal(actionsWithQueue(working, { verdict: "working" }, true), "queue");
  assert.equal(actionsWithQueue(working, { verdict: "working" }, false), "interrupt");
  assert.equal(actionsWithQueue({ state: "idle" }, { verdict: "idle" }, true), "reply");
  assert.equal(actionsWithQueue({ state: "blocked" }, { verdict: "awaiting_input", dialog }, true), "answer");
  assert.deepEqual(buildQueueRequest("s", "@1", "hi"), { method: "POST", path: "/v1/sessions/s/windows/%401/queue", body: { text: "hi" } });
  assert.deepEqual(buildQueueRequest("s", "@1", "hi", ["a.png"]).body, { text: "hi", images: ["a.png"] });
  assert.deepEqual(buildReplyRequest("s", "@1", "hi", []).body, { text: "hi" });
  assert.deepEqual(buildReplyRequest("s", "@1", "", ["a.png"]).body, { text: "", images: ["a.png"] });
  assert.equal(buildUploadPath("s", "@1"), "/v1/sessions/s/windows/%401/upload");
  const png = { name: "a.png", type: "image/png", size: 1000 };
  assert.equal(checkAttachment(png, 0), "");
  assert.match(checkAttachment(png, MAX_IMAGES), /At most/);
  assert.match(checkAttachment({ ...png, type: "image/heic" }, 0), /not a png/);
  assert.match(checkAttachment({ ...png, size: 11 * 1024 * 1024 }, 0), /larger than 10 MB/);
  console.log("ok: window view helpers");
}

{
  assert.deepEqual(groupCounts([]), []);
  assert.deepEqual(
    groupCounts([{ state: "idle" }, { state: "working" }, { state: "idle", done: true }, { state: "blocked" }, { state: "idle" }, { state: "working" }]),
    [{ state: "blocked", n: 1 }, { state: "working", n: 2 }, { state: "done", n: 1 }, { state: "idle", n: 2 }],
  );
  assert.deepEqual(groupCounts([{ state: "idle" }]), [{ state: "idle", n: 1 }]);
  assert.notEqual(groupKey("s"), collapseKey("s", { win: { window_id: "@1" } }));
  assert.notEqual(groupKey("s"), collapseKey("s", { bucket: true }));
  assert.notEqual(groupKey("a"), groupKey("b"));
  console.log("ok: project fold helpers");
}

{
  const write = ["fleet", "write"];
  assert.deepEqual(buildSweepRequest("agentctl-shop", true), { method: "POST", path: "/v1/sweep/preview", body: { session: "agentctl-shop" } });
  assert.deepEqual(buildSweepRequest("agentctl-shop", false), { method: "POST", path: "/v1/sweep", body: { session: "agentctl-shop" } });
  assert.deepEqual(buildSweepRequest("agentctl-shop", true, true), { method: "POST", path: "/v1/sweep/preview", body: { session: "agentctl-shop", include_bare: true } });
  assert.deepEqual(buildSweepRequest("agentctl-shop", false, true).body, { session: "agentctl-shop", include_bare: true });
  assert.equal(canSweep({ session: "agentctl-shop", dir: "/x" }, write), true);
  assert.equal(canSweep({ session: "agentctl-shop", dir: "/x" }, ["fleet"]), false, "needs write");
  assert.equal(canSweep({ session: "agentctl-hub", dir: "/x" }, write), false, "never the hub");
  assert.equal(canSweep({ session: "agentctl-shop", dir: "" }, write), false, "needs a project dir");
  assert.equal(canSweep(null, write), false);

  const row = (name, detail) => ({ window_id: "@" + name, name, detail });
  const preview = { session: "s", closed: [row("a", "done"), row("b")], attention: [row("c")], in_progress: [row("d"), row("e")] };
  assert.equal(sweepCounts(preview), "1 needs attention, 2 in progress");
  assert.deepEqual(sweepConfirm(preview, "shop"), {
    title: "Sweep shop?",
    lines: ["Close 2 windows:\na  (done)\nb", "1 needs attention, 2 in progress"],
    confirmLabel: "Close 2",
    toggle: { label: "Full sweep (also close finished interactive sessions)", value: false },
  });
  assert.equal(sweepConfirm(preview, "shop", true).toggle.value, true);
  assert.equal(sweepConfirm({ session: "s", closed: [], attention: [row("c")] }, "shop"), null);
  assert.equal(nothingToSweep({ closed: [], attention: [row("c"), row("d")] }), "Nothing to sweep (2 need attention)");
  assert.equal(nothingToSweep({ closed: [] }), "Nothing to sweep");
  assert.deepEqual(sweptMessage({ closed: [row("a")] }), { text: "Swept 1", kind: "info" });
  assert.deepEqual(sweptMessage({ closed: [row("a"), { name: "b", errors: ["kill failed"] }] }), { text: "Swept 1; 1 failed: b: kill failed", kind: "error" });

  // The confirm flow, effects stubbed.
  const flow = async ({ previewRep, applyRep, confirmed, failPreview, failApply }) => {
    const sent = [];
    const toasts = [];
    const sheets = [];
    const result = await runSweep({ session: "agentctl-shop", project: "shop" }, {
      send: async (req) => {
        sent.push(req.path);
        if (req.path.endsWith("/preview")) {
          if (failPreview) throw new Error(failPreview);
          return previewRep;
        }
        if (failApply) throw new Error(failApply);
        return applyRep;
      },
      confirm: async (sheet) => {
        sheets.push(sheet);
        return confirmed;
      },
      toast: (text, kind) => toasts.push([text, kind]),
    });
    return { result, sent, toasts, sheets };
  };
  let r = await flow({ previewRep: preview, applyRep: { closed: [row("a")] }, confirmed: true });
  assert.equal(r.result, "applied");
  assert.deepEqual(r.sent, ["/v1/sweep/preview", "/v1/sweep"]);
  assert.equal(r.sheets[0].title, "Sweep shop?");
  assert.deepEqual(r.toasts, [["Swept 1", "info"]], "reports what actually closed, not the preview");
  r = await flow({ previewRep: preview, confirmed: false });
  assert.equal(r.result, "cancelled");
  assert.deepEqual(r.sent, ["/v1/sweep/preview"], "cancel never applies");
  assert.deepEqual(r.toasts, []);
  r = await flow({ previewRep: { closed: [], attention: [], in_progress: [] }, confirmed: true });
  assert.equal(r.result, "nothing");
  assert.equal(r.sheets.length, 0, "no confirm when nothing would close");
  assert.deepEqual(r.toasts, [["Nothing to sweep", "info"]]);
  r = await flow({ failPreview: "unknown session" });
  assert.equal(r.result, "failed");
  assert.deepEqual(r.toasts, [["unknown session", "error"]]);
  r = await flow({ previewRep: preview, confirmed: true, failApply: 'sweep s: no tmux session "s"' });
  assert.equal(r.result, "failed");
  assert.deepEqual(r.toasts, [['sweep s: no tmux session "s"', "error"]]);

  // Full sweep defaults on, flipping the toggle re-runs the preview, and the
  // apply uses the value of the preview that was confirmed.
  const bodies = [];
  const answers = [{ fullSweep: false }, { fullSweep: true }, true];
  const sheetsSeen = [];
  const res = await runSweep({ session: "agentctl-shop", project: "shop" }, {
    send: async (req) => {
      bodies.push([req.path, req.body.include_bare === true]);
      return req.path.endsWith("/preview") ? preview : { closed: [row("a")] };
    },
    confirm: async (sheet) => {
      sheetsSeen.push(sheet.toggle.value);
      return answers.shift();
    },
    toast: () => {},
  });
  assert.equal(res, "applied");
  assert.deepEqual(bodies, [
    ["/v1/sweep/preview", true],
    ["/v1/sweep/preview", false],
    ["/v1/sweep/preview", true],
    ["/v1/sweep", true],
  ]);
  assert.deepEqual(sheetsSeen, [true, false, true]);
  const bodies2 = [];
  const answers2 = [{ fullSweep: false }, true];
  await runSweep({ session: "s" }, {
    send: async (req) => { bodies2.push([req.path, req.body.include_bare === true]); return req.path.endsWith("/preview") ? preview : { closed: [] }; },
    confirm: async () => answers2.shift(),
    toast: () => {},
  });
  assert.deepEqual(bodies2.at(-1), ["/v1/sweep", false], "apply matches the previewed toggle");
  console.log("ok: sweep helpers and confirm flow");
}
for (const f of fixtures.defaultAgent) {
  assert.equal(defaultAgentFor(f.projects, f.dir, f.agents, f.userPicked), f.expect, f.name);
}
console.log("ok: dispatch default agent");

assert.deepEqual(buildShellRequest("/code/p"), { method: "POST", path: "/v1/shell", body: { project_dir: "/code/p" } });
assert.equal(canShell(["write", "shell"]), true);
assert.equal(canShell(["write"]), false);
{
  const F = ["write", "shell"];
  const g = { session: "agentctl-p", dir: "/code/p", shells: [{ window_id: "@7", dir: "/code/p" }, { window_id: "@8", dir: "/code/p/sub/" }, { window_id: "@9", dir: "" }] };
  assert.deepEqual(shellRows(g, F).map((r) => [r.window_id, r.label]), [["@7", "$ shell"], ["@8", "$ shell · sub"], ["@9", "$ shell"]]);
  assert.deepEqual(shellRows(g, ["write"]), [], "feature off: no shell rows");
  assert.deepEqual(shellRows({ ...g, shells: undefined }, F), []);
  assert.deepEqual(shellRows({ ...g, session: "agentctl-hub" }, F), []);
  assert.equal(shellDirName("/a/b/"), "b");
  const fl = { groups: [{ session: "s", windows: [{ window_id: "@5" }], shells: [{ window_id: "@9" }] }, { session: "t", windows: [{ window_id: "@6" }], shells: [] }] };
  const after = applyGone(fl, "s", "@5");
  assert.equal(after.groups.length, 2, "group with only a shell survives its last agent");
  assert.equal(applyGone(fl, "t", "@6").groups.length, 1, "group with nothing left goes");
  assert.equal(shellApiPath("agentctl-p", "@7"), "/v1/sessions/agentctl-p/shells/%407/close");
}
assert.equal(canShell(["shell"]), false);
assert.equal(canShell([]), false);
console.log("ok: shell request");

{
  const texts = (w) => rowChips(w).map((c) => c.text);
  const full = { agent: "go-engineer:3", recap: "#7 Add a retry", recap_source: "task", issue: 7, pr_number: 8, model: "claude-opus-4-20250514", backend: "nous", scheduled_by: "nightly" };
  assert.deepEqual(texts(full), ["PR #8", "#7", "go-engineer", "opus-4", "nous", "⏲ nightly"]);
  assert.deepEqual(texts({ agent: "x:1", recap: "t", recap_source: "title", title: "t", backend: "anthropic" }), ["x"]);
  assert.deepEqual(rowChips({ agent: "brandon:5" }), []);
  assert.deepEqual(texts({ agent: "brandon:5", model: "claude-sonnet-4-5" }), ["sonnet-4-5"]);
  assert.deepEqual(rowChips(full).map((c) => c.kind), ["pr", "issue", "agent-chip", "model-chip", "backend-chip", "sched-chip"]);
  assert.deepEqual(texts({ agent: "x", pr_url: "https://h/o/r/pull/9" }), ["PR #9"]);
  console.log("ok: rowChips");
}

{
  // turn_done (a bare window at a turn end) displays as done, display only.
  assert.equal(effectiveState({ state: "idle", turn_done: true }), "done");
  assert.equal(effectiveState({ state: "idle", turn_done: false }), "idle");
  assert.equal(effectiveState({ state: "idle" }), "idle"); // older server
  assert.equal(effectiveState({ state: "working", turn_done: true }), "working");
  assert.equal(effectiveState({ state: "blocked", turn_done: true }), "blocked");
  assert.equal(displayState({ state: "idle", turn_done: true }, new Map()), "done");
  assert.equal(stateLabel({ state: "idle", turn_done: true }, 0), "done");
  assert.equal(actionsFor({ state: "idle", turn_done: true }), "reply");
  assert.equal(canSwipeClose({ state: "idle", turn_done: true }, "s", ["write"]), true);
  // Still waiting when a child works.
  const fl = [{ windows: [
    { window_id: "@1", session: "s", state: "idle", turn_done: true },
    { window_id: "@2", session: "s", state: "working", parent: "@1" },
  ] }];
  assert.equal(displayState(fl[0].windows[0], waitingIndex(fl)), "waiting");
  assert.deepEqual(groupCounts([{ state: "idle", turn_done: true }, { state: "idle" }]), [{ state: "done", n: 1 }, { state: "idle", n: 1 }]);
}

{
  // attention (the ! state): ⏸ > ● > ! > ✓ > ○, display only.
  const q = { state: "idle", attention: "question", attention_detail: "Merge it?" };
  assert.equal(effectiveState(q), "attention");
  assert.equal(effectiveState({ ...q, done: true }), "attention");
  assert.equal(effectiveState({ ...q, turn_done: true }), "attention");
  assert.equal(effectiveState({ ...q, state: "working" }), "working");
  assert.equal(effectiveState({ ...q, state: "blocked" }), "blocked");
  assert.equal(attentionLabel("question"), "asks you");
  assert.equal(attentionLabel("errors"), "stuck");
  assert.equal(attentionLabel("message"), "review verdict");
  assert.equal(attentionLabel("other"), "needs you");
  assert.equal(attentionLine(q), "asks you: Merge it?");
  assert.equal(attentionLine({ state: "idle", attention: "errors" }), "stuck");
  assert.equal(attentionLine({ ...q, state: "working" }), "");
  assert.equal(rowDetailLine({ ...q, recap: "Reworking the hub", recap_source: "summary" }), "asks you: Merge it?");
  assert.equal(stateLabel(q, 0), "asks you");
  assert.equal(actionsFor(q), "reply");
  assert.equal(canSwipeClose(q, "s", ["write"]), false); // never swiped away unanswered
  // An attention parent with a working child shows !, not waiting.
  const fl = [{ windows: [
    { window_id: "@1", session: "s", ...q },
    { window_id: "@2", session: "s", state: "working", parent: "@1" },
  ] }];
  assert.equal(displayState(fl[0].windows[0], waitingIndex(fl)), "attention");
  assert.deepEqual(groupCounts([q, { state: "idle", done: true }, { state: "blocked" }, { state: "idle" }]),
    [{ state: "blocked", n: 1 }, { state: "attention", n: 1 }, { state: "done", n: 1 }, { state: "idle", n: 1 }]);
  // Sorts after working, before done.
  const sorted = sortGroups([{ session: "s", windows: [
    { window_id: "@3", state: "idle", done: true }, { window_id: "@4", ...q }, { window_id: "@5", state: "working" },
  ] }]);
  assert.deepEqual(sorted[0].windows.map((w) => w.window_id), ["@5", "@4", "@3"]);
  assert.equal(sorted[0].rollup, "working");
  console.log("ok: attention");
}

{
  const w = (id, state, parent, extra = {}) => ({ window_id: id, session: "s", state, parent, ...extra });
  const ids = (m, id) => (m.get(id) ?? []).map((k) => k.window_id);
  // Zero windows, no groups.
  assert.equal(waitingIndex([]).size, 0);
  assert.equal(waitingIndex(undefined).size, 0);
  // Idle and done parents wait on working and blocked children, across sessions.
  const fleet = [
    { windows: [w("@1", "idle"), w("@2", "idle", undefined, { done: true }), w("@3", "working", "@1")] },
    { windows: [w("@4", "blocked", "@1"), w("@5", "idle", "@2", { done: true }), w("@6", "working", "@2")] },
  ];
  const idx = waitingIndex(fleet);
  assert.deepEqual(ids(idx, "@1"), ["@3", "@4"]);
  assert.deepEqual(ids(idx, "@2"), ["@6"]); // the done child @5 does not count
  assert.equal(displayState(fleet[0].windows[0], idx), "waiting");
  assert.equal(displayState(fleet[0].windows[2], idx), "working");
  assert.equal(displayState(fleet[1].windows[0], idx), "blocked");
  assert.equal(waitingLabel(1), "waiting on 1 agent");
  assert.equal(waitingLabel(2), "waiting on 2 agents");
  assert.equal(stateLabel(fleet[0].windows[0], Date.now(), idx), "waiting on 2 agents");
  // A grandchild under an idle child does not make the grandparent wait; a waiting child does.
  const deep = waitingIndex([{ windows: [w("@1", "idle"), w("@2", "idle", "@1"), w("@3", "working", "@2")] }]);
  assert.ok(deep.has("@2") && deep.has("@1")); // @2 waits on @3, so @1 waits on waiting @2
  const gap = waitingIndex([{ windows: [w("@1", "idle"), w("@2", "done", "@1", { done: true }), w("@3", "working", "@2", { state: "idle", done: true })] }]);
  assert.equal(gap.size, 0);
  // Missing parent, self parent, and cycles neither crash nor flag.
  assert.equal(waitingIndex([{ windows: [w("@2", "working", "@gone"), w("@3", "idle", "@3")] }]).size, 0);
  assert.equal(waitingIndex([{ windows: [w("@1", "idle", "@2"), w("@2", "idle", "@1")] }]).size, 0);
  // A cycle with one working member waits (terminates).
  assert.ok(waitingIndex([{ windows: [w("@1", "idle", "@2"), w("@2", "working", "@1")] }]).has("@1"));
  // Last child finishing flips the parent back to idle.
  const done = [{ windows: [w("@1", "idle"), w("@3", "idle", "@1", { done: true })] }];
  assert.equal(waitingIndex(done).size, 0);
  // sortGroups: annotation, rank (after blocked, with working), rollup and counts.
  const sorted = sortGroups([{ session: "s", windows: [w("@1", "idle", undefined, { window_index: 1 }), w("@9", "idle", undefined, { window_index: 0 }), w("@3", "working", "@1", { window_index: 2 }), w("@4", "blocked", undefined, { window_index: 3 })] }]);
  assert.deepEqual(sorted[0].windows.map((x) => x.window_id), ["@4", "@1", "@3", "@9"]);
  assert.equal(sorted[0].rollup, "blocked");
  assert.deepEqual(groupCounts(sorted[0].windows), [{ state: "blocked", n: 1 }, { state: "working", n: 1 }, { state: "waiting", n: 1 }, { state: "idle", n: 1 }]);
  assert.equal(sortGroups([{ session: "s", windows: [w("@1", "idle"), w("@3", "working", "@1")] }])[0].rollup, "working");
  // Waiting keeps the reply composer.
  assert.equal(actionsFor(sorted[0].windows[1], null), "reply");
  console.log("ok: waiting on sub-agents");
}

{
  assert.deepEqual(parseRoute("#/host"), { name: "host" });
  assert.equal(formatBytes(1536 * 1024 * 1024), "1.5 GB");
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatUptime(3 * 86400 + 4 * 3600), "3d 4h");
  assert.equal(formatUptime(7500), "2h 05m");
  assert.equal(formatUptime(30), "30s");
  const body = { host: { cores: 8, load: { l1: 9 }, cpu_percent: 50, memory: { total: 100, available: 5 }, swap: { total: 100, used: 80 }, memory_pressure: "warn" } };
  assert.deepEqual(hostLevels(body), { load: "bad", cpu: "", memory: "bad", swap: "bad", pressure: "warn" });
  assert.deepEqual(hostLevels({ host: { cores: 8, load: { l1: 1 } } }), { load: "", cpu: "", memory: "", swap: "", pressure: "" });
  assert.deepEqual(hostLevels(null), { load: "", cpu: "", memory: "", swap: "", pressure: "" });
  const c = hostConnection("2026-01-01T00:00:01.000Z", Date.parse("2026-01-01T00:00:00.000Z"), Date.parse("2026-01-01T00:00:00.200Z"));
  assert.deepEqual(c, { rtt: 200, skew: 900 });
  assert.equal(hostConnection("nope", 0, 10).skew, null);
  assert.deepEqual(pushSample([1, 2, 3], 4, 3), [2, 3, 4]);
  assert.equal(formatSkew(120), "in sync");
  assert.equal(formatSkew(-2500), "-2.5s");
  console.log("ok: host view logic");
}

// Schedules: routes, row text, and the form round trip.
{
  assert.deepEqual(parseRoute("#/schedules"), { name: "schedules", view: "list" });
  assert.deepEqual(parseRoute("#/schedules/new"), { name: "schedules", view: "new" });
  assert.deepEqual(parseRoute("#/schedules/nightly-triage"), { name: "schedules", view: "job", job: "nightly-triage" });
  assert.deepEqual(parseRoute("#/schedules/a/edit"), { name: "schedules", view: "edit", job: "a" });
  assert.deepEqual(parseRoute("#/schedules/a/copy"), { name: "schedules", view: "copy", job: "a" });
  assert.equal(scheduleHash("a b", "edit"), "#/schedules/a%20b/edit");

  const now = new Date(2026, 10, 22, 8, 0).getTime();
  const at = (d, h, m) => new Date(2026, 10, d, h, m).toISOString();
  assert.equal(formatNextFire(at(22, 8, 20), now), "in 20m");
  assert.equal(formatNextFire(at(22, 11, 20), now), "in 3h 20m");
  assert.equal(formatNextFire(at(22, 11, 0), now), "in 3h");
  assert.equal(formatNextFire(at(24, 7, 31), now), "Tue 07:31");
  assert.equal(formatNextFire(undefined, now), "");
  assert.equal(formatAgo(at(22, 6, 0), now), "2h ago");

  assert.equal(runResultLabel({ result: "skipped:usage", note: "usage 5h 82%" }), "skipped: usage 5h 82%");
  assert.equal(runResultLabel({ result: "failed:launch", note: "boom" }), "failed: boom");
  assert.equal(runResultLabel({ result: "fired" }), "fired");
  assert.equal(scheduleException({ last: { result: "skipped:overlap", note: "still running" } }), "skipped: still running");
  assert.equal(scheduleException({ last: { result: "fired" } }), "");
  assert.deepEqual(scheduleGlyph({ enabled: false, failed_last: true }), { glyph: "\u23F8", kind: "paused" });
  assert.equal(scheduleGlyph({ enabled: true, failed_last: true }).glyph, "!");
  assert.equal(scheduleGlyph({ enabled: true, running: true }).glyph, "\u25CF");
  assert.equal(scheduleGlyph({ enabled: true }).glyph, "\u23F2");
  assert.equal(scheduleWhen({ enabled: false }, now), "paused");
  assert.equal(scheduleLastText({ last: { ts: at(22, 7, 0), result: "fired" } }, now), "last: fired \u00B7 1h ago");
  assert.deepEqual(scheduleBanners({ scheduler_installed: true, enabled: true }), []);
  assert.equal(scheduleBanners({ scheduler_installed: false, enabled: false }).length, 2);
  assert.deepEqual(fieldErrors([{ field: "name", message: "a" }, { field: "name", message: "b" }]), { name: "a" });

  // Edit round trip: fields the form does not show survive, worktree is explicit.
  const stored = { name: "j", schedule: "30 7 * * 1-5", dir: "/x", agent: "go", prompt: "p", harness: "claude", mode: "window",
    worktree: false, overlap: "allow", catchup: "none", account: "work", enabled: false, skip_if_usage_above: { five_hour: 70 }, extra_future: 1 };
  const f = newScheduleForm({ job: stored, simple: { kind: "weekdays", minute: 30, hour: 7 } });
  const body = scheduleBody(f);
  assert.equal(body.job.name, "j");
  assert.equal(body.job.worktree, false);
  assert.equal(body.job.account, "work");
  assert.equal(body.job.extra_future, 1);
  assert.deepEqual(body.job.skip_if_usage_above, { five_hour: 70 });
  assert.equal(body.job.enabled, undefined, "an edit never sends enabled");
  assert.equal(body.job.schedule, undefined);
  assert.deepEqual(body.simple, { kind: "weekdays", minute: 30, hour: 7 });
  assert.equal(previewBody(f).original, "j");
  // Default worktree stays explicit true.
  assert.equal(scheduleBody(newScheduleForm({ job: { ...stored, worktree: undefined }, simple: { kind: "daily", minute: 0, hour: 1 } })).job.worktree, true);
  // A schedule the picker cannot show opens in Custom and is sent raw, untouched.
  const raw = newScheduleForm({ job: { ...stored, schedule: "*/20 9-17 * * 1-5" }, simple: null });
  assert.equal(raw.mode, "custom");
  assert.deepEqual(scheduleBody(raw).job.schedule, "*/20 9-17 * * 1-5");
  assert.equal(scheduleBody(raw).simple, undefined);
  // Days mode.
  const days = newScheduleForm({ job: stored, simple: { kind: "days", minute: 5, hour: 6, days: [3, 1] } });
  assert.deepEqual(scheduleBody(days).simple, { kind: "days", minute: 5, hour: 6, days: [1, 3] });
  days.days = [];
  assert.equal(formClientErrors(days).schedule, "Pick at least one day");
  // prompt_key survives until the text is edited.
  const pk = newScheduleForm({ job: { ...stored, prompt: "", prompt_key: "k" }, simple: { kind: "daily", minute: 0, hour: 1 }, promptText: "resolved" });
  assert.equal(pk.prompt, "resolved");
  assert.equal(scheduleBody(pk).job.prompt_key, "k");
  assert.equal(scheduleBody(pk).job.prompt, undefined);
  pk.prompt = "mine";
  assert.equal(scheduleBody(pk).job.prompt, "mine");
  assert.equal(scheduleBody(pk).job.prompt_key, undefined);
  // Duplicate: new name, saved paused, no original.
  const cp = newScheduleForm({ job: stored, simple: { kind: "daily", minute: 0, hour: 1 }, copy: true });
  assert.equal(cp.name, "j-copy");
  assert.equal(scheduleBody(cp).job.enabled, false);
  assert.equal(previewBody(cp).original, undefined);
  // New job: no extra fields, default worktree on.
  assert.equal(scheduleBody({ ...newScheduleForm(), name: " x " }).job.name, "x");
  assert.equal(formClientErrors({ ...newScheduleForm(), name: "new" }).name, '"new" is reserved; pick another name');
  assert.equal(advancedSummary(f), "no worktree \u00B7 account work \u00B7 overlap allow \u00B7 catchup none \u00B7 usage gate 70/default");
  assert.equal(advancedSummary(newScheduleForm()), "");

  const lines = scheduleReview({ job: { agent: "brandon", dir: "/Users/b/code/x", worktree: true, prompt: "Do it" }, words: "weekdays 07:30",
    time_zone: "EST", permission_mode: "bypassPermissions", next_fires_text: ["Mon 23 Nov 07:31"] }, newScheduleForm());
  assert.equal(lines[0], [
    "Runs   brandon in ~/code/x (a worktree per fire)",
    "When   weekdays 07:30 EST",
    "Mode   unattended, permission mode bypassPermissions, until paused",
  ].join("\n"));
  assert.equal(lines[1], "Next: Mon 23 Nov 07:31");
  assert.match(lines.at(-1), /Prompt:\nDo it/);
  assert.equal(worktreeFact({ worktree: false }), "off (runs in the project checkout)");
  assert.equal(worktreeFact({}), "on (a worktree per fire)");
  assert.doesNotMatch(scheduleReview({ job: { agent: "a", dir: "/d", worktree: false, prompt: "p" }, schedule: "x" }, newScheduleForm())[0], /permission mode/);
  assert.equal(newScheduleForm().mode, "weekdays");
  // old -> new in the review of an edit
  const old = { name: "n", dir: "/d", agent: "a", prompt: "p", schedule: "0 9 * * *", worktree: true, overlap: "skip" };
  const ef = newScheduleForm({ job: old, simple: { kind: "daily", hour: 9, minute: 0 }, words: "daily 09:00" });
  const rv = scheduleReview({ job: { ...old, model: "opus", account: "work", schedule: "0 9 * * 1-5" }, words: "weekdays 09:00" }, ef);
  assert.ok(rv.includes("Changes:\nwhen  daily 09:00 \u2192 weekdays 09:00\nmodel  agent default \u2192 opus\naccount  default \u2192 work"));
  assert.deepEqual(changeLines(old, old), []);
  assert.deepEqual(changeLines(old, { ...old, skip_if_usage_above: { five_hour: 100 } }), ["skip above  global \u2192 5h off, 7d global"]);
  // usage options and timeout presets
  assert.deepEqual(usageOptions(80).map((o) => o[1]), ["global (80%)", "50", "60", "70", "80", "90", "off"]);
  assert.ok(usageOptions(80, "85").some(([v]) => v === "85"));
  assert.equal(timeoutIsPreset("45m"), true);
  assert.equal(timeoutIsPreset("20m"), false);
  // card
  assert.equal(runResultCard({ result: "failed:launch", note: "no such agent" }), "failed \u00B7 launch: no such agent");
  assert.equal(runResultCard({ result: "skipped:usage", note: "usage 5h 82%" }), "skipped \u00B7 usage 5h 82%");
  assert.deepEqual(cardStatus({ enabled: true, running: false }, "Mon 07:30"), { glyph: "\u23F2", kind: "enabled", text: "enabled \u00B7 next Mon 07:30" });
  assert.equal(cardStatus({ enabled: false }).text, "paused");
  assert.equal(cardLastFailure({ last: { result: "fired" } }), "");
  assert.match(cardLastFailure({ last: { result: "failed:launch", note: "boom", ts: new Date().toISOString() } }), /^Last run failed \u00B7 launch: boom/);
  console.log("ok: schedules logic");
}

// Schedules: a template as the prompt source.
{
  const stored = { name: "j", schedule: "0 9 * * *", dir: "/x", agent: "", template: "triage", harness: "claude", mode: "window" };
  const f = newScheduleForm({ job: stored, simple: { kind: "daily", minute: 0, hour: 9 }, templateText: "Triage it." });
  assert.equal(f.source, "template");
  assert.equal(f.template, "triage");
  let body = scheduleBody(f);
  assert.equal(body.job.template, "triage");
  assert.equal(body.job.agent, "");
  assert.equal(body.job.prompt, undefined);
  assert.equal(body.job.prompt_key, undefined);
  // Template -> prompt drops the template; and back again drops the prompt.
  f.source = "prompt";
  f.prompt = "Inline.";
  f.agent = "go";
  body = scheduleBody(f);
  assert.equal(body.job.template, undefined);
  assert.equal(body.job.prompt, "Inline.");
  f.source = "template";
  body = scheduleBody(f);
  assert.equal(body.job.template, "triage");
  assert.equal(body.job.prompt, undefined);
  // A prompt-key job switched to a template loses the key.
  const pk = newScheduleForm({ job: { ...stored, template: undefined, agent: "go", prompt_key: "k" }, simple: null, promptText: "t" });
  assert.equal(pk.source, "prompt");
  pk.source = "template";
  pk.template = "triage";
  assert.equal(scheduleBody(pk).job.prompt_key, undefined);
  // No template picked is a client error.
  assert.equal(formClientErrors({ ...newScheduleForm(), source: "template" }).template, "Pick a template");
  assert.equal(formClientErrors(newScheduleForm()).template, undefined);
  // Labels.
  assert.equal(schedulePromptLabel({ template: "triage", prompt: "x" }), "template triage");
  assert.equal(schedulePromptLabel({ prompt_key: "k" }), "context key k");
  assert.equal(scheduleAgentText({ template: "triage" }, "go-engineer"), "go-engineer (from template triage)");
  assert.equal(scheduleAgentText({ template: "triage" }), "the agent of template triage");
  assert.equal(scheduleAgentText({ agent: "a", template: "triage" }, "go-engineer"), "a");
  assert.equal(scheduleRowActivity({ template: "triage" }, "proj"), "proj \u203A template triage");
  assert.equal(scheduleRowActivity({ agent: "a", template: "triage" }, "proj"), "proj \u203A a \u00B7 template triage");
  assert.equal(scheduleRowActivity({ agent: "a", prompt: "p" }, "proj"), "proj \u203A a");
  assert.equal(scheduleRowActivity({ agent: "a" }, ""), "a");
  // An improve job needs no prompt or agent of its own.
  assert.equal(scheduleAgentText({ mode: "improve" }), "fleet-improver");
  assert.equal(scheduleRowActivity({ mode: "improve" }, "proj"), "proj \u203A fleet-improver \u00B7 fleet improve");
  assert.deepEqual(scheduleBody(newScheduleForm({ job: { name: "j", dir: "/d", mode: "improve", schedule: "0 22 * * *", overlap: "skip" } })).job.mode, "improve");
  // Review and changes name the template.
  const rv = scheduleReview({ job: { agent: "", template: "triage", dir: "/d", worktree: true }, schedule: "x" }, { ...f, templateAgent: "go-engineer" });
  assert.match(rv[0], /Runs {3}go-engineer \(from template triage\) in \/d/);
  assert.match(rv.at(-1), /^Prompt: template triage \(read from the project at every run\)\nTriage it\.$/);
  assert.deepEqual(changeLines({ agent: "a", prompt: "p" }, { agent: "a", template: "triage" }), ["prompt  p \u2192 template triage"]);
  console.log("ok: schedule templates");
}

// Prompt templates on the dispatch sheet.
assert.deepEqual(buildDispatchRequest("a", "/d", "p", {}, [], "", false, "triage").body, { agent: "a", project_dir: "/d", prompt: "p", template: "triage" });
assert.ok(confirmLines({ agent: "a", project_dir: "/d", prompt: "p", template: "triage" }).join("\n").includes("template: triage"));
assert.equal(templatesPath(""), "/v1/templates");
assert.equal(templatesPath("/a b"), "/v1/templates?dir=%2Fa%20b");
assert.equal(templateLabel({ name: "t", description: "d" }), "t · d");
assert.equal(templateLabel({ name: "t" }), "t");
{
  const projects = [{ dir: "/p1" }];
  assert.deepEqual(templateFill({ prompt: "x", agent: "a", dir: "/p1", model: "opus", effort: "high" }, projects, ["a"]), {
    prompt: "x", agent: "a", project: "/p1", preset: { model: "opus", effort: "high" },
  });
  // Unknown agent or project is left alone; the prompt still fills.
  assert.deepEqual(templateFill({ prompt: "x", agent: "zz", dir: "/nope" }, projects, ["a"]), { prompt: "x", preset: {} });
}
console.log("ok: templates");

// Approval cards name the template.
{
  const tpl = { kind: "template", asking_agent: "w", details: { template: "triage", mode: "add template", agent: "go-engineer" } };
  assert.equal(approvalSummary(tpl), "w wants to add template triage");
  assert.deepEqual(approvalRows(tpl).map((r) => [r.label, r.value]), [["Template", "triage"], ["Action", "add template"], ["Agent", "go-engineer"]]);
  const disp = { kind: "dispatch", details: { project: "p", agent: "a", template: "triage", mode: "ad-hoc" } };
  assert.deepEqual(approvalRows(disp)[0], { label: "Template", value: "triage", detail: "", flag: "" });
}
assert.equal(approvalConfirm({ kind: "template", command: "c", details: {} }, "allow").title, "Approve this template change?");
console.log("ok: template approvals");

// Double tap on the reflow pane.
assert.equal(isTap({ t: 0, x: 5, y: 5 }, { t: 120, x: 8, y: 7 }), true);
assert.equal(isTap({ t: 0, x: 5, y: 5 }, { t: 450, x: 5, y: 5 }), false, "long press");
assert.equal(isTap({ t: 0, x: 5, y: 5 }, { t: 100, x: 5, y: 40 }), false, "scroll");
assert.equal(isDoubleTap(null, { t: 100, x: 0, y: 0 }), false);
assert.equal(isDoubleTap({ t: 0, x: 10, y: 10 }, { t: 200, x: 15, y: 12 }), true);
assert.equal(isDoubleTap({ t: 0, x: 10, y: 10 }, { t: 300, x: 10, y: 10 }), true);
assert.equal(isDoubleTap({ t: 0, x: 10, y: 10 }, { t: 301, x: 10, y: 10 }), false, "too slow");
assert.equal(isDoubleTap({ t: 0, x: 10, y: 10 }, { t: 100, x: 80, y: 10 }), false, "too far");
assert.equal(isDoubleTap({ t: 100, x: 10, y: 10 }, { t: 50, x: 10, y: 10 }), false, "out of order");
console.log("ok: double tap");

// Nothing earlier to load.
assert.equal(paneLineCount(""), 0);
assert.equal(paneLineCount("a\nb\n"), 2);
assert.equal(canLoadEarlier("a\nb\n"), false, "short pane has no scrollback");
assert.equal(canLoadEarlier(Array(199).fill("x").join("\n")), false);
assert.equal(canLoadEarlier(Array(200).fill("x").join("\n")), true);
assert.equal(historyAddsLines("a\nb", "a\nb"), false, "history no longer than the tail");
assert.equal(historyAddsLines("b\nc", "a\nb\nc"), true);
console.log("ok: load earlier gate");
// Artifacts: routes, grouping, filter and token age.
{
  assert.deepEqual(parseRoute("#/artifacts"), { name: "artifacts", view: "list", filter: null });
  assert.deepEqual(parseRoute("#/artifacts?window=%40523&session=agentctl-x"), { name: "artifacts", view: "list", filter: { session: "agentctl-x", windowId: "@523" } });
  assert.deepEqual(parseRoute("#/artifacts?window=%40523"), { name: "artifacts", view: "list", filter: null });
  assert.deepEqual(parseRoute("#/artifacts/abc"), { name: "artifacts", view: "viewer", id: "abc", rev: null });
  assert.deepEqual(parseRoute("#/artifacts/abc/3"), { name: "artifacts", view: "viewer", id: "abc", rev: 3 });
  assert.deepEqual(parseRoute("#/artifacts/abc/latest"), { name: "artifacts", view: "viewer", id: "abc", rev: null });
  assert.equal(artifactHash("abc"), "#/artifacts/abc");
  assert.equal(artifactHash("abc", 2), "#/artifacts/abc/2");
  const f = { session: "agentctl-x", windowId: "@523" };
  assert.deepEqual(parseRoute(artifactsHash(f)).filter, f);
  assert.equal(artifactsHash(), "#/artifacts");

  const A = (id, dir, project, extra = {}) => ({ id, project_dir: dir, project, updated: "2026-01-01T00:00:00Z", rev_count: 1, ...extra });
  const list = [A("1", "/z", "zed"), A("2", "/b", "beta"), A("3", "/a", "alpha"), A("4", "/b", "beta"), A("5", "", "")];
  const fleet = [{ key: "/b", dir: "/b" }, { key: "k2", dir: "/z" }];
  const g = groupArtifacts(list, fleet);
  assert.deepEqual(g.map((x) => x.name), ["beta", "zed", "alpha", "Other"]);
  assert.deepEqual(g[0].items.map((x) => x.id), ["2", "4"]); // list order kept inside a group
  assert.deepEqual(groupArtifacts(list, []).map((x) => x.name), ["alpha", "beta", "zed", "Other"]);
  assert.deepEqual(groupArtifacts([], fleet), []);

  const w = { session: "s", window_id: "@1" };
  assert.deepEqual(artifactsForWindow([{ id: "a", ...w }, { id: "b", session: "t", window_id: "@1" }, { id: "c", session: "s", window_id: "@2" }], "s", "@1").map((x) => x.id), ["a"]);
  assert.equal(artifactChipText(1), "\u25C6 1 artifact");
  assert.equal(artifactChipText(3), "\u25C6 3 artifacts");
  assert.equal(artifactWindowLive({ ...w }, [{ session: "s", windows: [{ window_id: "@1" }] }])?.window_id, "@1");
  assert.equal(artifactWindowLive({ ...w }, [{ session: "s", windows: [] }]), null);
  assert.equal(artifactWindowLive({ session: "s" }, []), null);

  const now = Date.parse("2026-01-01T12:00:00Z");
  assert.equal(artifactTokenStale(now - 59 * 60000, now), false);
  assert.equal(artifactTokenStale(now - ARTIFACT_TOKEN_MAX_AGE_MS, now), true);
  assert.equal(artifactTokenStale(undefined, now), true);
  assert.equal(artifactIcon("html") !== artifactIcon("image"), true);
  assert.equal(artifactIcon("weird"), "\u25C6");
  assert.equal(artifactRowMeta({ rev_count: 3, updated: "2026-01-01T11:55:00Z" }, now), "3 revs \u00B7 5m ago");
  assert.equal(artifactRowMeta({ rev_count: 1, updated: "2026-01-01T11:55:00Z" }, now), "5m ago");
  assert.equal(artifactRowMeta({ pinned: true, rev_count: 2, updated: "2026-01-01T11:55:00Z" }, now), "pin \u00B7 2 revs \u00B7 5m ago");
  assert.equal(artifactRevLabel({ n: 2, created: "2026-01-01T11:55:00Z", note: "fix" }, now), "v2 \u00B7 5m ago \u00B7 fix");
  assert.equal(artifactRevLabel({ n: 1, created: "2026-01-01T09:00:00Z" }, now), "v1 \u00B7 3h ago");
  assert.equal(artifactRevPill(2, 2, 2), "v2 \u25BE");
  assert.equal(artifactRevPill(1, 2, 2), "v1 of 2 \u25BE");
  assert.equal(artifactOrigin({ agent: "designer", project: "shop", window_id: "@17" }), "shop \u203A designer");
  assert.equal(artifactOrigin({ agent: "designer", project: "shop", window_id: "@17" }, { handle: "a3f9" }), "shop \u203A designer \u00B7 a3f9");
  assert.equal(artifactOrigin({ project: "shop" }), "shop");
  // Group items keep the served (saved) order: pinned rows no longer float.
  const pg = groupArtifacts([A("p1", "/b", "beta"), A("p2", "/b", "beta", { pinned: true }), A("p3", "/b", "beta")], []);
  assert.deepEqual(pg[0].items.map((x) => x.id), ["p1", "p2", "p3"]);
  // Other is always last, even when the fleet knows no project and unknown projects follow by name.
  assert.deepEqual(groupArtifacts([A("o", "", ""), A("a", "/a", "alpha")], []).map((x) => x.name), ["alpha", ARTIFACT_OTHER]);
  assert.equal(artifactGroupKey(A("x", "/d", "n")), "/d");
  assert.equal(artifactGroupKey(A("x", "", "n")), "n");
  assert.equal(artifactGroupKey(A("x", "", "")), "");
  // fleetKey is the fleet's key for a listed project, empty otherwise.
  assert.deepEqual(g.map((x) => x.fleetKey), ["/b", "k2", "", ""]);
  assert.equal(groupArtifacts([A("a", "/b", "b")], [{ key: "k9", dir: "/b" }])[0].fleetKey, "k9");
  // A project with no dir is named by its name; a dir with no name by its basename.
  assert.equal(groupArtifacts([A("a", "/x/y", "")], [])[0].name, "y");

  // The optimistic order matches the server's merge: new ones first, saved after, unknown ids dropped.
  const items = ["n1", "a", "b", "c"].map((id) => ({ id }));
  assert.deepEqual(mergeArtifactOrder(items, ["c", "gone", "a", "b"]).map((x) => x.id), ["n1", "c", "a", "b"]);
  assert.deepEqual(mergeArtifactOrder(items, []).map((x) => x.id), ["n1", "a", "b", "c"]);
  assert.deepEqual(mergeArtifactOrder(items, ["a", "a"]).map((x) => x.id), ["n1", "b", "c", "a"]);
  assert.deepEqual(buildArtifactOrderRequest("/p", ["a", "b"]), { method: "POST", path: "/v1/artifacts/order", body: { project: "/p", ids: ["a", "b"] } });

  // Dragging a project group saves the fleet keys of the listed groups; Other stays last and unlisted groups cannot move.
  const gs = [
    { key: "/a", fleetKey: "ka" }, { key: "/b", fleetKey: "kb" }, { key: "/u", fleetKey: "" }, { key: "", fleetKey: "" },
  ];
  assert.deepEqual(artifactGroupMove(gs, "/a", 1), ["kb", "ka"]);
  assert.equal(artifactGroupMove(gs, "/a", 0), null, "no move");
  assert.deepEqual(artifactGroupMove(gs, "/b", 0), ["kb", "ka"]);
  assert.deepEqual(artifactGroupMove(gs, "/a", 9), ["kb", "ka"], "past Other clamps before it");
  assert.equal(artifactGroupMove(gs, "/u", 0), null, "unlisted group");
  assert.equal(artifactGroupMove(gs, "", 0), null, "Other");

  assert.equal(artifactSubline({ description: " Weekly numbers ", entry: "r.html" }), "Weekly numbers");
  assert.equal(artifactSubline({ entry: "r.html" }), "r.html");
  assert.equal(artifactSubline({}), "");
  assert.equal(artifactRevBadge({ latest: 3 }), "rev 3");
  assert.equal(artifactRevBadge({ rev_count: 1 }), "");
  assert.equal(artifactRevBadge({ latest: 4, rev_count: 4 }), "rev 4");

  // Folded groups persist under their own key and survive junk.
  const mem = () => { const m = new Map(); return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => m.set(k, v) }; };
  const st = mem();
  saveCollapsedSet(ARTIFACT_COLLAPSE_KEY, new Set(["/a", "~other"]), st);
  assert.deepEqual([...loadCollapsedSet(ARTIFACT_COLLAPSE_KEY, st)], ["/a", "~other"]);
  assert.equal(ARTIFACT_COLLAPSE_KEY, "agentctl.artifacts.collapsed");
  st.setItem("k", "{nope");
  assert.equal(loadCollapsedSet("k", st).size, 0);
  st.setItem("k", JSON.stringify(["a", 3, null]));
  assert.deepEqual([...loadCollapsedSet("k", st)], ["a"]);
  assert.equal(loadCollapsedSet("k", { getItem() { throw new Error("denied"); } }).size, 0);
  assert.doesNotThrow(() => saveCollapsedSet("k", new Set(["a"]), { setItem() { throw new Error("full"); } }));
  console.log("ok: artifacts logic");
}

{
  // The delete queue: the DELETE goes out only when the undo window ends.
  const timers = new Map();
  let nextTimer = 1;
  const setTimer = (fn, ms) => { timers.set(nextTimer, { fn, ms }); return nextTimer++; };
  const clearTimer = (t) => timers.delete(t);
  const fire = () => { const [id, t] = [...timers][0]; timers.delete(id); t.fn(); };
  const sent = [];
  const settled = [];
  let fail = false;
  const q = createDeleteQueue({
    commit: async (item) => { sent.push(item.id); if (fail) throw new Error("boom"); },
    onSettle: (id, item, err) => settled.push([id, err ? err.message : null]),
    setTimer, clearTimer,
  });
  assert.equal(UNDO_MS, 5000);
  q.add("a", { id: "a" });
  assert.equal(timers.get(1).ms, UNDO_MS);
  assert.equal(q.has("a"), true);
  assert.deepEqual(sent, [], "nothing sent while the toast is up");
  q.add("a", { id: "a" }); // a second swipe on the same row is ignored
  assert.equal(timers.size, 1);

  // Undo cancels: nothing is ever sent, the row is back.
  q.add("b", { id: "b" });
  assert.equal(q.undo("b"), true);
  assert.equal(q.has("b"), false);
  assert.equal(q.undo("b"), false, "already undone");
  assert.equal(timers.size, 1);

  // Expiry sends; the id stays hidden until the request settles.
  fire();
  assert.deepEqual(sent, ["a"]);
  assert.equal(q.has("a"), true, "in flight: an SSE refetch must not resurrect it");
  await Promise.resolve(); await Promise.resolve();
  assert.equal(q.has("a"), false);
  assert.deepEqual(settled, [["a", null]]);
  assert.equal(q.undo("a"), false, "too late to undo");

  // A failed delete settles with the error so the row can be restored.
  fail = true;
  q.add("c", { id: "c" });
  fire();
  await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(settled.at(-1), ["c", "boom"]);
  assert.equal(q.has("c"), false);

  // Leaving the page flushes everything waiting.
  fail = false;
  q.add("d", { id: "d" });
  q.add("e", { id: "e" });
  assert.deepEqual(q.waiting(), ["d", "e"]);
  await q.flush();
  assert.deepEqual(sent.slice(-2), ["d", "e"]);
  assert.equal(timers.size, 0);
  assert.deepEqual(q.waiting(), []);
  // A refetch racing the DELETE must not resurrect the row; old entries expire.
  const settledIds = new Map([["x", 15000], ["old", 0]]);
  const rows = [{ id: "x" }, { id: "y" }, { id: "old" }];
  assert.deepEqual(dropSettled(rows, settledIds, 16000).map((r) => r.id), ["y", "old"]);
  assert.deepEqual(dropSettled(rows, settledIds, 15000 + SETTLED_TTL_MS + 1).map((r) => r.id), ["x", "y", "old"]);
  assert.equal(settledIds.size, 0);
  console.log("ok: delete queue");
}

{
  // The postMessage bridge: only the iframe's own window, only the exact type.
  const frame = { id: "frame" };
  const ev = (over = {}) => ({ source: frame, data: { type: "agentctl:feedback", text: "pick B" }, ...over });
  assert.equal(artifactFeedbackFrom(ev(), frame), "pick B");
  assert.equal(artifactFeedbackFrom(ev({ source: {} }), frame), null, "other window");
  assert.equal(artifactFeedbackFrom(ev({ source: null }), frame), null);
  assert.equal(artifactFeedbackFrom(ev(), null), null, "no frame window");
  assert.equal(artifactFeedbackFrom(ev(), undefined), null);
  assert.equal(artifactFeedbackFrom(ev({ data: { type: "other", text: "x" } }), frame), null, "wrong type");
  assert.equal(artifactFeedbackFrom(ev({ data: { type: "agentctl:feedback", text: 5 } }), frame), null, "non-string");
  assert.equal(artifactFeedbackFrom(ev({ data: { type: "agentctl:feedback" } }), frame), null);
  assert.equal(artifactFeedbackFrom(ev({ data: { type: "agentctl:feedback", text: "  \n " } }), frame), null, "blank");
  assert.equal(artifactFeedbackFrom(ev({ data: "agentctl:feedback" }), frame), null, "string payload");
  assert.equal(artifactFeedbackFrom(ev({ data: null }), frame), null);
  assert.equal(artifactFeedbackFrom(ev({ data: { type: "agentctl:feedback", text: "  hi  " } }), frame), "hi");
  const long = artifactFeedbackFrom(ev({ data: { type: "agentctl:feedback", text: "\u00e9".repeat(ARTIFACT_FEEDBACK_MAX + 50) } }), frame);
  assert.equal([...long].length, ARTIFACT_FEEDBACK_MAX);
  assert.equal(artifactCommentToast({ delivery: "delivered" })[0], "Sent to the agent");
  assert.match(artifactCommentToast({ delivery: "queued" })[0], /^Queued/);
  assert.equal(artifactCommentToast({ delivery: "saved", reason: "the agent's window is closed" })[0], "Saved, not delivered: the agent's window is closed");
  assert.equal(artifactCommentToast({ delivery: "saved" })[0], "Saved, not delivered");
  assert.equal(artifactCommentStatus({ status: "delivered" }), "sent");
  assert.equal(artifactCommentStatus({ status: "queued" }), "queued");
  assert.equal(artifactCommentStatus({ status: "undelivered" }), "not delivered");
  console.log("ok: artifact feedback bridge");
}

// ---- handles ----
assert.equal(handleHash("a3f9"), "#/s/a3f9");
assert.equal(handleApiPath("a3f9"), "/v1/handles/a3f9");
assert.deepEqual(parseRoute("#/s/a3f9"), { name: "handle", handle: "a3f9" });
assert.deepEqual(parseRoute("#/w/agentctl-p/%4012"), { name: "window", session: "agentctl-p", windowId: "@12" });
assert.equal(windowWho({ label: "esc fix", handle: "a3f9" }), "esc fix (a3f9)");
assert.equal(windowWho({ handle: "a3f9" }), "a3f9");
assert.equal(windowWho(null), "");
assert.deepEqual(handleOutcome("a3f9", { live: true, session: "agentctl-p", window_id: "@7", handle: "a3f9" }), { go: "#/w/agentctl-p/%407" });
assert.match(handleOutcome("bbbb", { live: false, closed: true, handle: "bbbb", label: "old work", closed_at: "2026-11-20T09:00:00Z" }).message, /old work \(bbbb\) is closed .*agentctl resume bbbb/);
assert.match(handleOutcome("cccc", { live: false, closed: false, handle: "cccc", label: "stale" }).message, /not running in any window/);
assert.doesNotMatch(handleOutcome("cccc", { live: false, closed: false, handle: "cccc" }).message, /resume/);
assert.match(handleOutcome("a3f9", null, { status: 409, message: "ambiguous: a3f9ab, a3f9cd" }).message, /ambiguous/);
assert.match(handleOutcome("dead", null, { status: 404 }).message, /No session dead/);
assert.match(handleOutcome("dead", null, new Error("x")).message, /Could not look up/);
assert.equal(artifactOrigin({ project: "shop", agent: "designer", window_id: "@17" }, { handle: "a3f9" }).includes("@17"), false);
assert.equal(rowHeadline({ agent: "go-engineer:2", label: "go-engineer · started 09:05" }), "go-engineer · started 09:05");
assert.equal(agentChip({ agent: "go-engineer:2", label: "go-engineer · started 09:05" }), "");
assert.ok(rowChips({ agent: "a:1", handle: "a3f9" }).some((c) => c.kind === "handle-chip" && c.text === "a3f9"));
console.log("ok: handles");
// Folder-trust dialog: answered with trust/exit only, never Esc or a number.
{
  const trust = {
    kind: "trust", folder: "/repo/.worktrees/x", hash: "h".repeat(64),
    options: [{ key: "exit", label: "No, exit" }, { key: "trust", label: "Yes, I trust this folder" }],
  };
  assert.equal(isTrustDialog(trust), true);
  assert.equal(isTrustDialog({ hash: "x", options: [] }), false);
  assert.deepEqual(buildAnswerRequest("s", "@1", trust, "trust").body, { key: "trust", dialog_hash: trust.hash });
  assert.equal(buildAnswerRequest("s", "@1", trust, "exit").body.key, "exit");
  assert.throws(() => buildAnswerRequest("s", "@1", trust, "esc"), /folder-trust/);
  assert.throws(() => buildAnswerRequest("s", "@1", trust, "1"), /folder-trust/);
  assert.throws(() => buildAnswerRequest("s", "@1", { ...trust, options: [trust.options[0]] }, "trust"), /folder-trust/);
  // Trust keys are not options of an ordinary dialog.
  assert.throws(() => buildAnswerRequest("s", "@1", { hash: "x", options: [{ key: "1", label: "Yes" }] }, "trust"), /not an option/);

  assert.equal(isTrustBlocked({ state: "blocked", pending_tool: "trust" }), true);
  assert.equal(isTrustBlocked({ state: "blocked", pending_tool: "Bash" }), false);
  assert.equal(isTrustBlocked({ state: "idle", pending_tool: "trust" }), false);
  assert.equal(trustFolder(trust), "/repo/.worktrees/x");
  assert.equal(trustFolder({ pending_tool_detail: " /a/b " }), "/a/b");
  assert.equal(trustFolder(null), "");

  const pane = { verdict: "awaiting_input", dialog: trust };
  assert.equal(trustDialogOf(pane, "/repo/.worktrees/x"), trust);
  assert.equal(trustDialogOf(pane), trust);
  assert.throws(() => trustDialogOf(pane, "/other"), /different folder/);
  assert.throws(() => trustDialogOf({ verdict: "idle", dialog: null }), /no longer showing/);
  assert.throws(() => trustDialogOf({ verdict: "awaiting_input", dialog: { hash: "x", options: [] } }), /no longer showing/);

  const c = trustExitConfirm("/repo/.worktrees/x");
  assert.equal(c.danger, true);
  assert.equal(c.confirmLabel, "Exit session");
  assert.match(c.lines[0], /\/repo\/\.worktrees\/x/);
  assert.equal(TRUST_TITLE, "Trust this folder?");
  assert.match(TRUST_EXPLAIN, /read, edit and run files here/);
}
console.log("ok: folder trust");

// Artifact PDF export.
{
  assert.equal(artifactPdfPath("abc", 2), "/v1/artifacts/abc/pdf?rev=2");
  assert.equal(artifactPdfPath("a/b", 1), "/v1/artifacts/a%2Fb/pdf?rev=1");
  assert.equal(artifactPdfLabel(3), "Save as PDF (v3)");
  assert.equal(pdfFilename('attachment; filename="weekly-report-r2.pdf"'), "weekly-report-r2.pdf");
  assert.equal(pdfFilename("attachment; filename=\"abcdef-r1.pdf\"; filename*=UTF-8''%E6%97%A5%E6%9C%AC-r1.pdf"), "\u65E5\u672C-r1.pdf");
  assert.equal(pdfFilename("attachment; filename=\"ok.pdf\"; filename*=UTF-8''%E0%A4"), "ok.pdf"); // undecodable filename*
  assert.equal(pdfFilename("attachment; filename=plain.pdf"), "plain.pdf");
  assert.equal(pdfFilename('attachment; filename="../x/y.pdf"'), "..-x-y.pdf");
  assert.equal(pdfFilename("", "fallback-r1.pdf"), "fallback-r1.pdf");
  assert.equal(pdfFilename(null), "artifact.pdf");
  const file = { name: "a.pdf" };
  const share = { share() {}, canShare: (d) => d.files.length === 1 };
  assert.equal(pdfDelivery(share, file), "share");
  assert.equal(pdfDelivery({ share() {}, canShare: () => false }, file), "download");
  assert.equal(pdfDelivery({ canShare: () => true }, file), "download"); // no share()
  assert.equal(pdfDelivery({ share() {} }, file), "download"); // no canShare (desktop Firefox)
  assert.equal(pdfDelivery({ share() {}, canShare: () => { throw new TypeError("x"); } }, file), "download");
  assert.equal(pdfDelivery(undefined, file), "download");
}
console.log("ok: artifact pdf");
