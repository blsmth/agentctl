// Runs kit.js's validators over the JSON files in the given directories and
// prints {"<dir>/<name>": ["<error path>", ...]} as JSON. Used by the Go
// fixture test (internal/artifactkit) so both validators share one set of
// fixtures. Usage: node check.js <kit.js> <dir>...
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const [, , kitPath, ...dirs] = process.argv;
const sandbox = { window: { document: { getElementById: () => null } } };
vm.createContext(sandbox);
vm.runInContext(fs.readFileSync(kitPath, "utf8"), sandbox);
const Kit = sandbox.window.Kit;

function paths(spec) {
  const out = Kit.validate(spec).map((e) => e[0]);
  if (Array.isArray(spec.blocks)) {
    spec.blocks.forEach((b, i) => {
      Kit.validateBlock(b).forEach((e) => out.push("blocks[" + i + "]." + e[0]));
    });
  }
  return out;
}

const result = {};
for (const dir of dirs) {
  for (const f of fs.readdirSync(dir).filter((n) => n.endsWith(".json") && n !== "expect.json").sort()) {
    const spec = JSON.parse(fs.readFileSync(path.join(dir, f), "utf8"));
    result[path.basename(dir) + "/" + f.replace(/\.json$/, "")] = paths(spec);
  }
}
process.stdout.write(JSON.stringify(result));
