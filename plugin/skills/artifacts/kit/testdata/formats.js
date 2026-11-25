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
  [34.5, "34.5%"], [34.4999999999, "34.5%"], [34.56, "34.5%"], [-34.56, "-34.5%"], [12, "12%"], [0, "0%"], [100, "100%"],
  [0.3, "0.3%"], [0.29999999999, "0.3%"], [99.86, "99.8%"], [99.97, "99.97%"], [99.95, "99.95%"], [99.999, "99.999%"],
  [99.9996, ">99.999%"], [99.9999, ">99.999%"], [0.04, "0.04%"], [0.0004, "<0.001%"], [89.96, "89.9%"], [94.99, "94.9%"],
  [99.94, "99.94%"], [100.04, "100.04%"], [100.0004, ">100%"], [-0.04, "-0.04%"], [-0.06, "-0.06%"],
  [-0.0004, ">-0.001%"], [-99.97, "-99.97%"], [-99.9996, "<-99.999%"], [-100, "-100%"], [12.04, "12%"], [1234.5, "1,234.5%"],
];
const signed = [[0.04, "+0.04%"], [99.97, "+99.97%"], [-0.04, "-0.04%"], [0.0004, "<+0.001%"], [100.0004, ">+100%"]];
const duration = [[59.97, "59.9 s"], [3.24, "3.2 s"], [0.9996, "999 ms"], [0.21, "210 ms"], [60, "1m 00s"], [3599.6, "59m 59s"], [86399.6, "23h 59m"], [-3599.6, "-59m 59s"]];

const bad = [];
// chart fallback table cells and tooltips go through fmt.by / fmt.percent for "%" charts
const chart = [["percent", 99.997, "99.997%"], ["percent", 99.9999, ">99.999%"], ["number", 99.997, "100"]];
for (const [t, v, want] of chart) { const got = fmt.by(t, v); if (got !== want) bad.push(["chart " + t, v, want, got]); }
const units = [[99.86, "%", "99.8%"], [99.86, "% uptime", "99.8% uptime"], [12.5, "%  of runs", "12.5%  of runs"], [99.86, "ms", "99.86 ms"], [99.86, "", "99.86"]];
for (const [v, u, want] of units) { const got = fmt.withUnit(v, u); if (got !== want) bad.push(["withUnit " + u, v, want, got]); }
for (const [v, want] of percent) { const got = fmt.percent(v); if (got !== want) bad.push(["percent", v, want, got]); }
for (const [v, want] of signed) { const got = fmt.signed("percent", v); if (got !== want) bad.push(["signed", v, want, got]); }
for (const [v, want] of duration) { const got = fmt.duration(v); if (got !== want) bad.push(["duration", v, want, got]); }
process.stdout.write(JSON.stringify(bad));
