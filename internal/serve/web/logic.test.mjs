// Run with: node internal/serve/web/logic.test.mjs  (no dependencies; CI has no node).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  canSwipeClose, swipeAxis, swipeCommit, swipeOffset,
  ansiColor, approvalBannerText, approvalConfirm, approvalHash, approvalsForWindow, approvalSummary, buildApprovalRequest,
  buildDispatchRequest, choiceLabel, confirmLines, dispatchHash, formatCountdown, launchOptionsPath, modelPicker, normalizePane,
  parseAnsi, parseRoute, removeApproval, resolvedText, secondsLeft, splitPane, upsertApproval, withReceived,
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
  console.log("ok: swipe close");
}
