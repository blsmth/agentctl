// Run with: node internal/serve/web/logic.test.mjs  (no dependencies; also run by `just web-test` and the CI web job).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  SWIPE_REVEAL, canSwipeClose, swipeAxis, swipeCommit, swipeOffset,
  ansiColor, approvalBannerText, approvalConfirm, approvalHash, approvalsForWindow, approvalSummary, buildApprovalRequest,
  DISPATCH_UPLOAD_PATH, buildDispatchRequest, choiceLabel, confirmLines, dispatchHash, formatCountdown, launchOptionsPath, modelPicker, normalizePane,
  parseAnsi, parseRoute, removeApproval, resolvedText, secondsLeft, splitPane, upsertApproval, withReceived,
  bucketLabel, kindLabel, rowTags, rowTitle, rowHeadline, agentChip, rowDetailLine, treeRows,
  isScheduled, scheduledChip, scheduledLine, treeLines, collapseKey, groupCounts, groupKey,
  actionsWithQueue, buildAnswerRequest, checkboxChoices, buildQueueRequest, buildReplyRequest, buildUploadPath, checkAttachment, MAX_IMAGES, dialogQuestion, exactRows, focusedOption, formatCost, hangingIndent, headerChips,
  sortGroups, groupKeys, moveKey, applyGroupOrder, sameOrder, dropIndex, autoscrollDelta, buildGroupOrderRequest,
  defaultAgentFor, liveness, paneLinesForExact, safeHttpsUrl, shortModel, stateLabel, taskLine, headerLines,
  buildSweepRequest, canSweep, sweepConfirm, sweepCounts, nothingToSweep, sweptMessage, runSweep,
} from "./logic.js";

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
const conf = approvalConfirm(ap, "allow");
assert.equal(conf.confirmLabel, "Approve");
assert.equal(conf.danger, false);
assert.ok(conf.lines.includes("do it") && conf.lines.includes("agentctl dispatch -k k"));
assert.equal(approvalConfirm({ ...ap, kind: "bash", prompt: "" }, "deny").danger, true);
const cronConf = approvalConfirm({ ...ap, kind: "cron", prompt: "Job: nightly" }, "allow");
assert.equal(cronConf.title, "Approve this schedule?");
assert.ok(cronConf.lines[0].includes("scheduled agent: go-engineer") && cronConf.lines.includes("Job: nightly"));
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
  const act = { agent: "brandon:55", recap: "go test ./...", recap_source: "activity", activity: "go test ./..." };
  assert.equal(rowHeadline(act), "brandon:55");
  assert.equal(agentChip(act), "");
  assert.equal(rowDetailLine(act), "go test ./...");
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
  // A title without a showable recap is ignored.
  assert.equal(rowHeadline({ agent: "a:1", recap: "go test", recap_source: "activity", title: "Stale" }), "a:1");
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

  const now = Date.parse("2026-11-17T10:03:00Z");
  assert.equal(stateLabel({ state: "working", state_since: "2026-11-17T10:00:00Z" }, now), "working for 3m");
  assert.equal(stateLabel({ state: "blocked", blocked_since: "2026-11-17T10:02:30Z", state_since: "x" }, now), "blocked for 30s");
  assert.equal(stateLabel({ state: "idle", done: true }, now), "done");

  const chips = headerChips({
    pr_url: "https://github.com/o/r/pull/8", context_pct: 41.6, window_id: "@12", mode: "dispatch",
    detail: { cost_usd: 1.84, model: "claude-opus-4-1" },
  }, safeHttpsUrl);
  assert.deepEqual(chips.map((c) => c.text), ["PR #8", "42% ctx", "$1.84", "opus-4-1", "@12 · dispatch"]);
  assert.equal(chips[0].href, "https://github.com/o/r/pull/8");
  assert.deepEqual(headerChips({ pr_url: "javascript:alert(1)", pr_number: 3, window_id: "@1" }, safeHttpsUrl).map((c) => c.text), ["@1"]);
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
  });
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
  console.log("ok: sweep helpers and confirm flow");
}
for (const f of fixtures.defaultAgent) {
  assert.equal(defaultAgentFor(f.projects, f.dir, f.agents, f.userPicked), f.expect, f.name);
}
console.log("ok: dispatch default agent");
