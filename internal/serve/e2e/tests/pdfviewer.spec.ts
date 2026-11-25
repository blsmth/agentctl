// The artifact PDF viewer (pdfviewer.js): "View PDF" in the ⋯ sheet opens a
// full-screen overlay that draws the pages with the bundled PDF.js. The
// servetest renderer answers with a real 12-page PDF whose pages have a
// red, green or blue left half (repeating), after 300ms.
import type { Page } from "@playwright/test";
import { expect, test, type ArtifactSpec } from "./fixtures";

const shop = { project: "shop", project_dir: "/fixture/shop", session: "agentctl-shop" };

const seed = (): ArtifactSpec[] => [
  {
    title: "Checkout mockup", agent: "designer", ...shop, window_id: "@17",
    revisions: [
      { files: { "index.html": "<h1>one</h1>" }, entry: "index.html" },
      { files: { "index.html": "<h1>two</h1>" }, entry: "index.html" },
    ],
  },
];

const viewer = (page: Page) => page.locator(".pdfv");
const canvases = (page: Page) => page.locator(".pdfv-canvas");

/** open goes to the artifact and opens the viewer from the ⋯ sheet. */
async function open(page: Page, id: string, rev = "") {
  await page.goto(`/#/artifacts/${id}${rev}`);
  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("button", { name: /^View PDF/ }).click();
}

/** pixel reads the RGB at the fraction (fx, fy) of the nth canvas. */
const pixel = (page: Page, n: number, fx: number, fy: number) =>
  canvases(page).nth(n).evaluate((c, [x, y]) => {
    const cv = c as HTMLCanvasElement;
    const d = cv.getContext("2d")!.getImageData(Math.floor(cv.width * x!), Math.floor(cv.height * y!), 1, 1).data;
    return [d[0], d[1], d[2]];
  }, [fx, fy]);

test("View PDF replaces Save as PDF and Open PDF in the sheet", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}/1`);
  await page.getByRole("button", { name: "More actions" }).click();
  const btn = page.getByRole("button", { name: "View PDF (v1)" });
  const box = (await btn.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  expect(box.height).toBeGreaterThanOrEqual(44);
  await expect(page.getByRole("button", { name: /Save as PDF|Open PDF/ })).toHaveCount(0);
});

test("opening the viewer shows progress, then draws the pages", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await open(page, id);
  await expect(viewer(page)).toBeVisible();
  await expect(page.locator(".artifact-sheet")).toHaveCount(0);
  await expect(page.locator(".pdfv-title")).toHaveText("Checkout mockup");
  await expect(page.locator(".pdfv-status")).toContainText("Rendering the PDF");
  await expect(page.locator(".pdfv-count")).toHaveText("12 pages");
  await expect(page.locator(".pdfv-status")).toBeHidden();
  // Pages on screen are drawn into canvases: page 1 red, page 2 green on the left half, white on the right.
  await expect.poll(() => canvases(page).count()).toBeGreaterThanOrEqual(2);
  expect(await pixel(page, 0, 0.25, 0.5)).toEqual([255, 0, 0]);
  expect(await pixel(page, 0, 0.75, 0.5)).toEqual([255, 255, 255]);
  expect(await pixel(page, 1, 0.25, 0.5)).toEqual([0, 255, 0]);
  // Fitted to the width: no sideways scroll, and the canvas is drawn at the device pixel ratio.
  const m = await page.evaluate(() => {
    const s = document.querySelector(".pdfv-scroll")!;
    const c = document.querySelector<HTMLCanvasElement>(".pdfv-canvas")!;
    return { overflow: s.scrollWidth - s.clientWidth, ratio: c.width / c.getBoundingClientRect().width, dpr: devicePixelRatio };
  });
  expect(m.overflow).toBeLessThanOrEqual(0);
  expect(m.ratio).toBeGreaterThan(m.dpr - 0.1);
});

test("a long PDF draws only the pages near the screen", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await open(page, id);
  await expect(page.locator(".pdfv-count")).toHaveText("12 pages");
  await expect.poll(() => canvases(page).count()).toBeGreaterThan(0);
  await page.locator(".pdfv-scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
  // The last page (index 11, blue) is drawn and the first is freed.
  await expect(page.locator('.pdfv-page[data-page="12"] canvas')).toHaveCount(1);
  await expect(page.locator('.pdfv-page[data-page="1"] canvas')).toHaveCount(0);
  expect(await canvases(page).count()).toBeLessThan(8);
});

test("Close, Escape and the back gesture each close the viewer and leave the artifact open", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await open(page, id);
  await expect(page.locator(".pdfv-count")).toHaveText("12 pages");
  await page.getByRole("button", { name: "Close PDF" }).click();
  await expect(viewer(page)).toHaveCount(0);
  await expect(page.locator("header.bar h1")).toHaveText("Checkout mockup");

  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("button", { name: /^View PDF/ }).click();
  await expect(viewer(page)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(viewer(page)).toHaveCount(0);

  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("button", { name: /^View PDF/ }).click();
  await expect(viewer(page)).toBeVisible();
  await page.goBack();
  await expect(viewer(page)).toHaveCount(0);
  await expect(page).toHaveURL(new RegExp(`#/artifacts/${id}`));
});

test("Share hands over only the file when the browser can share files", async ({ page, control }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { shared: { name: string; type: string; size: number; keys: string[] }[] };
    w.shared = [];
    Object.defineProperty(navigator, "canShare", { configurable: true, value: (d: { files?: File[] }) => !!d?.files?.length });
    Object.defineProperty(navigator, "share", {
      configurable: true,
      value: async (d: { files: File[] }) => {
        const f = d.files[0]!;
        w.shared.push({ name: f.name, type: f.type, size: f.size, keys: Object.keys(d) });
      },
    });
  });
  const [id] = await control.seedArtifacts(seed());
  await open(page, id, "/2");
  const share = page.getByRole("button", { name: "Share", exact: true });
  await expect(share).toBeVisible();
  await share.click();
  await expect.poll(() => page.evaluate(() => (window as unknown as { shared: unknown[] }).shared.length)).toBe(1);
  const got = await page.evaluate(() => (window as unknown as { shared: unknown[] }).shared[0]);
  // Files only: with a title or text, iOS shares text and drops Save to Files and Print.
  expect(got).toMatchObject({ name: "checkout-mockup-r2.pdf", type: "application/pdf", keys: ["files"] });
  expect((got as { size: number }).size).toBeGreaterThan(0);
  await expect(viewer(page)).toBeVisible();
});

test("the button is Download when files cannot be shared, and saves the PDF", async ({ page, control }) => {
  await page.addInitScript(() => Object.defineProperty(navigator, "canShare", { configurable: true, value: () => false }));
  const [id] = await control.seedArtifacts(seed());
  await open(page, id, "/1");
  const dl = page.getByRole("button", { name: "Download", exact: true });
  await expect(dl).toBeVisible();
  await expect(page.getByRole("button", { name: "Share", exact: true })).toHaveCount(0);
  const download = page.waitForEvent("download");
  await dl.click();
  expect((await download).suggestedFilename()).toBe("checkout-mockup-r1.pdf");
});

test("a failed render shows the server's message and Retry asks again", async ({ page, control, guard }) => {
  guard.allow(/503|Service Unavailable/);
  await page.addInitScript(() => {
    // The first PDF request is answered 503 (another render in progress). The
    // page's fetch is wrapped: page.route does not see requests once the
    // service worker controls the page.
    let calls = 0;
    const real = window.fetch.bind(window);
    window.fetch = (input: RequestInfo | URL, init?: RequestInit) =>
      /\/v1\/artifacts\/[^/]+\/pdf\?/.test(String(input)) && calls++ === 0
        ? Promise.resolve(new Response(JSON.stringify({ error: "another PDF is rendering; try again" }), { status: 503, headers: { "Content-Type": "application/json" } }))
        : real(input, init);
  });
  const [id] = await control.seedArtifacts(seed());
  await open(page, id);
  await expect(page.locator(".pdfv-error")).toContainText("another PDF is rendering");
  await expect(page.locator(".pdfv-share")).toBeHidden();
  const retry = page.getByRole("button", { name: "Retry" });
  const box = (await retry.boundingBox())!;
  expect(box.height).toBeGreaterThanOrEqual(44);
  await retry.click();
  await expect(page.locator(".pdfv-count")).toHaveText("12 pages");
  await expect(page.locator(".pdfv-error")).toHaveCount(0);
});

test("no browser installed: the error names the setting and Retry stays", async ({ page, control, guard }) => {
  guard.allow(/501|Not Implemented/);
  const [id] = await control.seedArtifacts([{ title: "Report [no browser]", revisions: [{ files: { "r.md": "# R" }, entry: "r.md" }] }]);
  await open(page, id);
  await expect(page.locator(".pdfv-error")).toContainText("artifacts.pdf_browser");
  await page.getByRole("button", { name: "Retry" }).click();
  await expect(page.locator(".pdfv-error")).toContainText("artifacts.pdf_browser");
  await page.getByRole("button", { name: "Close PDF" }).click();
  await expect(viewer(page)).toHaveCount(0);
});

test("an answer that is not a PDF is refused", async ({ page, control }) => {
  await page.addInitScript(() => {
    const real = window.fetch.bind(window);
    window.fetch = (input: RequestInfo | URL, init?: RequestInit) =>
      /\/v1\/artifacts\/[^/]+\/pdf\?/.test(String(input))
        ? Promise.resolve(new Response("<!doctype html><p>hi</p>", { status: 200, headers: { "Content-Type": "text/html" } }))
        : real(input, init);
  });
  const [id] = await control.seedArtifacts(seed());
  await open(page, id);
  await expect(page.locator(".pdfv-error")).toContainText("did not send a PDF");
});

test("the viewer fits a 390px phone in light and dark, with white pages on dark chrome", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await open(page, id);
  await expect(page.locator(".pdfv-count")).toHaveText("12 pages");
  const width = page.viewportSize()!.width;
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    // The button reads Share or Download depending on the engine's file sharing.
    for (const btn of [page.locator(".pdfv-share"), page.getByRole("button", { name: "Close PDF" })]) {
      const box = (await btn.boundingBox())!;
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(box.height).toBeGreaterThanOrEqual(44);
    }
    const colors = await page.evaluate(() => ({
      chrome: getComputedStyle(document.querySelector(".pdfv")!).backgroundColor,
      pageBg: getComputedStyle(document.querySelector(".pdfv-page")!).backgroundColor,
    }));
    expect(colors.pageBg).toBe("rgb(255, 255, 255)");
    expect(colors.chrome).toBe(scheme === "dark" ? "rgb(28, 28, 30)" : "rgb(245, 245, 247)");
  }
});

test("rotating to landscape re-fits the pages", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await open(page, id);
  await expect(page.locator(".pdfv-count")).toHaveText("12 pages");
  const w = () => page.locator(".pdfv-page").first().evaluate((e) => e.getBoundingClientRect().width);
  const portrait = await w();
  const vp = page.viewportSize()!;
  await page.setViewportSize({ width: vp.height, height: vp.width });
  await expect.poll(w).toBeGreaterThan(portrait + 100);
  await expect.poll(() => canvases(page).count()).toBeGreaterThan(0);
  const m = await page.locator(".pdfv-canvas").first().evaluate((c) => {
    const cv = c as HTMLCanvasElement;
    return cv.width / cv.getBoundingClientRect().width;
  });
  expect(m).toBeGreaterThan(1.5); // redrawn for the new width, not stretched
});

test("closing while the server is still rendering cancels the request", async ({ page, control, guard }) => {
  guard.allow(/cancelled/);
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}`);
  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("button", { name: /^View PDF/ }).click();
  await expect(page.locator(".pdfv-status")).toContainText("Rendering the PDF");
  await page.getByRole("button", { name: "Close PDF" }).click();
  await expect(viewer(page)).toHaveCount(0);
  // Nothing is left behind to show up later.
  await page.waitForTimeout(600);
  await expect(viewer(page)).toHaveCount(0);
  await expect(page.locator("header.bar h1")).toHaveText("Checkout mockup");
});

test("zooming twice quickly leaves every visible page drawn sharp, at the new size", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await open(page, id);
  await expect(page.locator(".pdfv-count")).toHaveText("12 pages");
  await expect.poll(() => canvases(page).count()).toBeGreaterThan(0);
  const wheel = (deltaY: number) =>
    page.locator(".pdfv-scroll").evaluate((el, dy) => {
      el.dispatchEvent(new WheelEvent("wheel", { ctrlKey: true, deltaY: dy, clientX: 200, clientY: 300, bubbles: true, cancelable: true }));
    }, deltaY);
  // Each wheel burst settles after 150ms and re-renders; the second lands while pages from the first are still drawing.
  await wheel(-60);
  await page.waitForTimeout(170);
  await wheel(-40);
  await page.waitForTimeout(170);
  const sharp = () =>
    page.evaluate(() => {
      const box = document.querySelector(".pdfv-scroll")!.getBoundingClientRect();
      const out: { drawn: boolean; ratio: number }[] = [];
      for (const el of document.querySelectorAll(".pdfv-page")) {
        const r = el.getBoundingClientRect();
        if (r.bottom < box.top || r.top > box.bottom) continue;
        const c = el.querySelector("canvas");
        out.push({ drawn: !!c, ratio: c ? c.width / r.width : 0 });
      }
      return out;
    });
  // Zoomed ~2.7x: a canvas still at the old scale would be about 1.1 pixels per CSS pixel, not the device ratio.
  await expect.poll(async () => {
    const v = await sharp();
    return v.length > 0 && v.every((p) => p.drawn && p.ratio > 2);
  }).toBe(true);
});
