// Run with: node internal/serve/web/logic.test.mjs  (no dependencies; CI has no node).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { ansiColor, normalizePane, parseAnsi } from "./logic.js";

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
