// Checks kit.js's palette choice (Kit.isDark) against stub documents and media
// queries. Usage: node isdark.js <kit.js>. Exits non-zero on a mismatch.
"use strict";
const fs = require("fs");
const vm = require("vm");

const [, , kitPath] = process.argv;
const src = fs.readFileSync(kitPath, "utf8");

function isDark(attrs, { prefersDark, print }) {
  const window = {
    document: {
      getElementById: () => null,
      documentElement: { hasAttribute: (a) => attrs.includes(a) },
    },
    matchMedia: (q) => ({ matches: q === "print" ? print : q.includes("prefers-color-scheme: dark") ? prefersDark : false }),
    location: { search: "" },
  };
  const sandbox = { window };
  vm.createContext(sandbox);
  vm.runInContext(src, sandbox);
  return window.Kit.isDark();
}

const cases = [
  // [attrs, device dark, print, want]
  [["data-dark"], false, false, true], // a dark brand is dark on a light device
  [["data-dark"], true, false, true],
  [["data-dark"], false, true, true], // and stays dark in print
  [["data-light"], true, false, false], // a light brand ignores a dark device
  [["data-light"], true, true, false],
  [[], true, false, true], // unbranded follows the device
  [[], false, false, false],
  [[], true, true, false], // unbranded repaints light in print
];
let failed = 0;
for (const [attrs, prefersDark, print, want] of cases) {
  const got = isDark(attrs, { prefersDark, print });
  if (got !== want) {
    failed++;
    console.error(`attrs=${attrs} prefersDark=${prefersDark} print=${print}: got ${got}, want ${want}`);
  }
}
process.exit(failed ? 1 : 0);
