// Run with: node internal/serve/web/logic.test.mjs  (no dependencies; CI has no node).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  ansiColor, buildDispatchRequest, choiceLabel, confirmLines, dispatchHash, launchOptionsPath, parseRoute, modelPicker, normalizePane, parseAnsi,
} from "./logic.js";

const fixtures = JSON.parse(readFileSync(new URL("./logic.fixtures.json", import.meta.url), "utf8"));

// Compact view of a normalised block: a plain string, or {rule: label}.
const view = (b) => (b.rule !== undefined ? { rule: b.rule } : b.runs.map((r) => r.t).join(""));

let n = 0;
for (const f of fixtures.normalize) {
  assert.deepEqual(normalizePane(f.input).map(view), f.expect, f.name);
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
