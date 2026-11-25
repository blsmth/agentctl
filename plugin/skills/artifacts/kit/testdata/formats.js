// Table-driven check of kit.js's percent/duration formatters. Prints a JSON
// list of failures ([] when all pass). Usage: node formats.js <kit.js>
"use strict";
const fs = require("fs");
const vm = require("vm");

const sandbox = { window: { document: { getElementById: () => null } } };
vm.createContext(sandbox);
vm.runInContext(fs.readFileSync(process.argv[2], "utf8"), sandbox);
const fmt = sandbox.window.Kit.fmt;

const percent = [
  [34.5, "34.5%"], [12, "12%"], [0, "0%"], [100, "100%"],
  [99.97, "99.97%"], [99.95, "99.95%"], [99.999, "99.999%"], [99.9996, ">99.999%"],
  [0.04, "0.04%"], [0.0004, "<0.001%"], [89.96, "89.96%"], [94.99, "94.99%"],
  [99.94, "99.9%"], [99.949, "99.9%"], [100.04, "100.04%"], [100.0004, ">100%"],
  [-0.04, "-0.04%"], [-0.0004, ">-0.001%"], [-99.97, "-99.97%"], [-99.9996, "<-99.999%"],
  [-100, "-100%"], [12.04, "12%"], [1234.5, "1,234.5%"],
];
const signed = [[0.04, "+0.04%"], [99.97, "+99.97%"], [-0.04, "-0.04%"], [0.0004, "<+0.001%"], [100.0004, ">+100%"]];
const duration = [[59.97, "59.9 s"], [3.24, "3.2 s"], [0.9996, "999 ms"], [0.21, "210 ms"], [60, "1m 00s"]];

const bad = [];
for (const [v, want] of percent) { const got = fmt.percent(v); if (got !== want) bad.push(["percent", v, want, got]); }
for (const [v, want] of signed) { const got = fmt.signed("percent", v); if (got !== want) bad.push(["signed", v, want, got]); }
for (const [v, want] of duration) { const got = fmt.duration(v); if (got !== want) bad.push(["duration", v, want, got]); }
process.stdout.write(JSON.stringify(bad));
