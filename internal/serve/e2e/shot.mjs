// Drives the phone web app for `just demo-phone-shot` (internal/demo): plays a
// scene's phone steps against the dev server and writes PNGs.
//
//	node shot.mjs <base-url> <plan.json>
//
// The plan is internal/demo.PhonePlan. Uses the same Playwright install as
// `just web-test` (WebKit, iPhone profile) with the scene's viewport and theme.
import { readFileSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { devices, webkit } from "@playwright/test";

const [base, planFile] = process.argv.slice(2);
if (!base || !planFile) {
  console.error("usage: node shot.mjs <base-url> <plan.json>");
  process.exit(2);
}
const plan = JSON.parse(readFileSync(planFile, "utf8"));

const scene = await (await fetch(`${base}/__scene`)).json();
const byKey = new Map(scene.windows.map((w) => [w.key, w]));
const win = (key) => {
  const w = byKey.get(key);
  if (!w) throw new Error(`scene has no window ${key}`);
  return w;
};

/** route maps an open target to the app's hash route. */
function route(target) {
  const i = target.indexOf(":");
  const kind = i < 0 ? target : target.slice(0, i);
  const key = i < 0 ? "" : target.slice(i + 1);
  switch (kind) {
    case "fleet":
      return "/#/";
    case "window": {
      const w = win(key);
      return `/#/w/${encodeURIComponent(w.session)}/${encodeURIComponent(w.window_id)}`;
    }
    case "terminal": {
      const w = win(key);
      return `/#/t/${encodeURIComponent(w.session)}/${encodeURIComponent(w.window_id)}`;
    }
    case "approval": {
      const id = scene.approvals[key];
      if (!id) throw new Error(`window ${key} has no open approval`);
      return `/#/approvals/${encodeURIComponent(id)}`;
    }
  }
  throw new Error(`unknown open target ${target}`);
}

/** locate finds an element by "css:<selector>" or visible text. */
function locate(page, arg) {
  return arg.startsWith("css:") ? page.locator(arg.slice(4)).first() : page.getByText(arg).first();
}

const browser = await webkit.launch();
try {
  const context = await browser.newContext({
    ...devices["iPhone 15"],
    viewport: { width: plan.viewport.width, height: plan.viewport.height },
    screen: { width: plan.viewport.width, height: plan.viewport.height },
    deviceScaleFactor: plan.viewport.dpr,
    colorScheme: plan.theme,
  });
  const page = await context.newPage();
  page.on("pageerror", (e) => console.error("page error:", e.message));
  await page.goto(`${base}/__login`); // device cookie, then the app
  await page.waitForLoadState("networkidle");
  for (const step of plan.steps) {
    switch (step.kind) {
      case "open":
        await page.goto(base + route(step.arg));
        await page.waitForLoadState("networkidle");
        break;
      case "tap":
        await locate(page, step.arg).tap();
        break;
      case "wait":
        await locate(page, step.arg).waitFor({ state: "visible", timeout: 10_000 });
        break;
      case "screenshot":
        await page.evaluate(() => document.fonts.ready);
        await page.waitForTimeout(400); // let transitions and live refreshes settle
        mkdirSync(dirname(step.path), { recursive: true });
        await page.screenshot({ path: step.path });
        break;
    }
  }
} finally {
  await browser.close();
}
