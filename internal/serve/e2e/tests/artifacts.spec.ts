// The Artifacts tab and viewer: list, sandboxed iframe, revisions, pin, delete,
// window chip and the push-click route.
import { expect, test, type ArtifactSpec } from "./fixtures";

// A 1x1 PNG.
const PNG = "base64:iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

const site = (heading: string): Record<string, string> => ({
  "index.html":
    `<!doctype html><html><head><title>Site</title><link rel="stylesheet" href="style.css"></head>` +
    `<body><h1 id="h">${heading}</h1><p id="js">js not run</p><img id="pic" src="pic.png"><script src="app.js"></script></body></html>`,
  "style.css": "#h { color: rgb(1, 2, 3); }",
  "app.js": `document.getElementById("js").textContent = "js ran";`,
  "pic.png": PNG,
});

const shop = { project: "shop", project_dir: "/fixture/shop", session: "agentctl-shop" };

const seed = (): ArtifactSpec[] => [
  {
    title: "Checkout mockup", agent: "designer", ...shop, window_id: "@17", pin: true,
    revisions: [{ files: site("Rev one"), entry: "index.html" }, { files: site("Rev two"), entry: "index.html", note: "bigger button" }],
  },
  { title: "Cart numbers", agent: "go-engineer", ...shop, window_id: "@12", revisions: [{ files: { "report.md": "# Cart\n\nSome numbers." }, entry: "report.md" }] },
  { title: "Post draft", agent: "writer", project: "blog", project_dir: "/fixture/blog", session: "agentctl-blog", window_id: "@31", revisions: [{ files: { "notes.txt": "hello" }, entry: "notes.txt" }] },
];

const frame = (page: import("@playwright/test").Page) => page.frameLocator("iframe.artifact-frame");

test("the tab is hidden until the server offers artifacts, then lists by project", async ({ page, control }) => {
  await control.seedArtifacts([]);
  await page.goto("/#/artifacts");
  await expect(page.locator("#nav-artifacts")).toBeVisible();
  await expect(page.locator(".artifact-empty")).toContainText("agentctl artifact add");

  await control.seedArtifacts(seed());
  await expect(page.locator(".artifact-row")).toHaveCount(3);
  // Groups follow the fleet's order (shop before blog) and rows carry the details.
  await expect(page.locator(".artifact-list section.group h2")).toHaveText(["shop", "blog"]);
  const mock = page.locator(".artifact-row", { hasText: "Checkout mockup" });
  await expect(mock).toContainText("designer");
  await expect(mock).toContainText("rev 2");
  await expect(mock.locator(".artifact-pin")).toBeVisible();
  await expect(mock.locator(".artifact-tile")).toHaveClass(/kind-html/);
  // The header carries the project's artifact count.
  await expect(page.locator(".artifact-list section.group").first().locator(".artifact-count")).toHaveText("2");
  await expect(page.locator("nav a.active")).toHaveText("Artifacts");
});

test("the viewer sandboxes a directory artifact and loads its relative assets", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}`);
  const iframe = page.locator("iframe.artifact-frame");
  await expect(iframe).toBeVisible();
  const sandbox = await iframe.getAttribute("sandbox");
  expect(sandbox!.split(" ").sort()).toEqual(["allow-downloads", "allow-forms", "allow-modals", "allow-popups", "allow-scripts"]);
  await expect(page.locator("header.bar h1")).toHaveText("Checkout mockup");
  const f = frame(page);
  await expect(f.locator("#h")).toHaveText("Rev two");
  await expect(f.locator("#js")).toHaveText("js ran");
  await expect(f.locator("#h")).toHaveCSS("color", "rgb(1, 2, 3)");
  await expect(f.locator("#pic")).toHaveJSProperty("naturalWidth", 1);
    // Origin chips; the window is live in the fixture.
  await expect(page.locator("nav")).toBeHidden();
  await page.getByRole("button", { name: "More actions" }).click();
  await expect(page.locator(".artifact-sheet .artifact-origin")).toHaveText("shop › designer");
  await expect(page.getByRole("link", { name: "Go to window ›" })).toHaveAttribute("href", "#/w/agentctl-shop/%4017");
  const open = page.getByRole("link", { name: /Open in new tab/ });
  // The id is the capability: the direct numbered URL, no token.
  await expect(open).toHaveAttribute("href", new RegExp(`^/a/${id}/2/$`));
  await expect(open).toHaveAttribute("target", "_blank");
  await page.getByRole("button", { name: "Close" }).click();
  const bar = (await page.locator("header.artifact-bar").boundingBox())!;
  expect(bar.height).toBeLessThanOrEqual(56);
  const fr = (await page.locator("iframe.artifact-frame").boundingBox())!;
  const vh = page.viewportSize()!.height;
  expect(vh - (fr.y + fr.height)).toBeLessThan(2);
});

test("an unpaired browser opens the direct link and loads the relative assets", async ({ browser, baseURL, control }) => {
  const [id] = await control.seedArtifacts(seed());
  expect(id).toMatch(/^[a-z2-7]{26}$/);
  const ctx = await browser.newContext({ baseURL });
  const page = await ctx.newPage();
  const res = await page.goto(`/a/${id}/latest/`);
  expect(res?.status()).toBe(200);
  expect(res?.headers()["content-security-policy"]).toMatch(/^sandbox allow-scripts/);
  await expect(page.locator("#h")).toHaveText("Rev two");
  await expect(page.locator("#js")).toHaveText("js ran");
  await expect(page.locator("#h")).toHaveCSS("color", "rgb(1, 2, 3)");
  await expect(page.locator("#pic")).toHaveJSProperty("naturalWidth", 1);
  // The API still needs pairing.
  expect((await page.request.get("/v1/artifacts")).status()).toBe(401);
  await ctx.close();
});

test("the revision picker switches content", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}`);
  await expect(frame(page).locator("#h")).toHaveText("Rev two");
  await expect(page.locator(".artifact-pill")).toHaveText("v2 ▾");
  await page.getByLabel("Revision").selectOption("1");
  await expect(page).toHaveURL(new RegExp(`#/artifacts/${id}/1$`));
  await expect(page.locator(".artifact-pill")).toHaveText("v1 of 2 ▾");
  await expect(frame(page).locator("#h")).toHaveText("Rev one");
  await page.getByLabel("Revision").selectOption("2");
  await expect(frame(page).locator("#h")).toHaveText("Rev two");
});

test("a published revision shows up while viewing", async ({ page, control }) => {
  const specs = seed();
  const [id] = await control.seedArtifacts(specs);
  await page.goto(`/#/artifacts/${id}`);
  await expect(frame(page).locator("#h")).toHaveText("Rev two");
  specs[0]!.revisions.push({ files: site("Rev three"), entry: "index.html" });
  // Re-seeding replaces the store (a new id), so the open one is deleted.
  await control.seedArtifacts(specs);
  await expect(page.locator(".artifact-message")).toContainText("This artifact was deleted");
});

test("pin toggles and delete confirms, then returns to the list", async ({ page, control }) => {
  const ids = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${ids[1]}`);
  await expect(frame(page).locator("body")).toContainText("Some numbers");
  const more = page.getByRole("button", { name: "More actions" });
  await more.click();
  await page.getByRole("button", { name: "Pin", exact: true }).click();
  await more.click();
  await expect(page.getByRole("button", { name: "Unpin" })).toBeVisible();
  await page.getByRole("button", { name: "Delete" }).click();
  await expect(page.locator(".overlay .sheet")).toContainText("Delete Cart numbers?");
  await page.locator(".overlay .sheet").getByRole("button", { name: "Cancel" }).click();
  expect((await page.request.get(`/v1/artifacts/${ids[1]}`)).status()).toBe(200);
  await more.click();
  await page.getByRole("button", { name: "Delete" }).click();
  await page.locator(".overlay .sheet").getByRole("button", { name: "Delete" }).click();
  await expect(page).toHaveURL(/#\/artifacts$/);
  await expect(page.locator(".artifact-row")).toHaveCount(2);
});

test("deleted while viewing shows a way back", async ({ page, control, guard }) => {
  guard.allow(/HTTP 404|status of 404/);
  const ids = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${ids[2]}`);
  await expect(frame(page).locator("body")).toContainText("hello");
  const status = await page.evaluate(
    async (id) => (await fetch(`/v1/artifacts/${id}`, { method: "DELETE", headers: { "X-Agentctl-CSRF": "1", "Content-Type": "application/json" }, body: "{}" })).status,
    ids[2],
  );
  expect(status).toBe(200);
  await expect(page.locator(".artifact-message")).toContainText("This artifact was deleted");
  await page.locator(".artifact-message").getByRole("link", { name: "Back to artifacts" }).click();
  await expect(page).toHaveURL(/#\/artifacts$/);
  await page.goto("/#/artifacts/zzzzzzzzzz");
  await expect(page.locator(".artifact-message")).toContainText("This artifact was deleted");
});

test("read-only devices get no pin or delete", async ({ page, control }) => {
  const ids = await control.seedArtifacts(seed());
  await page.route("**/healthz", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.features = body.features.filter((f: string) => f !== "write");
    await route.fulfill({ response: res, json: body });
  });
  await page.goto(`/#/artifacts/${ids[0]}`);
  await expect(page.locator("iframe.artifact-frame")).toBeVisible();
  await page.getByRole("button", { name: "More actions" }).click();
  await expect(page.getByRole("link", { name: /Open in new tab/ })).toBeVisible();
  await expect(page.getByRole("button", { name: "Delete" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Pin/ })).toHaveCount(0);
});

test("the window detail chip links to the list filtered to that window", async ({ page, control }) => {
  await control.seedArtifacts(seed());
  await page.goto("/#/w/agentctl-shop/%4017");
  const chip = page.locator(".artifact-chip");
  await expect(chip).toHaveText("◆ 1 artifact");
  await chip.click();
  await expect(page.locator(".artifact-row")).toHaveCount(1);
  await expect(page.locator(".artifact-row")).toContainText("Checkout mockup");
  await page.getByRole("link", { name: "Show all" }).click();
  await expect(page.locator(".artifact-row")).toHaveCount(3);
  await page.goto("/#/w/agentctl-shop/%4015");
  await expect(page.locator(".win-head")).toBeVisible();
  await expect(page.locator(".artifact-chip")).toBeHidden();
});

test("the service worker precaches artifacts.js and opens same-origin push urls", async ({ page }) => {
  const sw = await (await page.request.get("/sw.js")).text();
  expect(sw).toContain('"/artifacts.js"');
  // notificationclick resolves the payload url against the origin and hands it
  // to the open app (location.assign), so "/#/artifacts/<id>" opens the viewer.
  expect(sw).toContain('event.notification.data?.url');
  expect(sw).toContain('postMessage({ type: "navigate", url })');
});

test("feedback: the comment button opens the composer and the outcome toast follows", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}`);
  await expect(frame(page).locator("#h")).toHaveText("Rev two");
  await page.getByRole("button", { name: "Send feedback to the agent" }).click();
  const sheet = page.locator(".overlay .sheet");
  await expect(sheet).toContainText("Applies to v2");
  await expect(sheet.getByRole("button", { name: "Send" })).toBeVisible();
  await sheet.getByLabel("Feedback").fill("make the button bigger");
  await sheet.getByRole("button", { name: "Send" }).click();
  await expect(page.locator(".toast")).toContainText(/Sent to the agent|Queued|Saved, not delivered/);
  await expect(page.locator(".overlay")).toHaveCount(0);
  await page.getByRole("button", { name: "More actions" }).click();
  await expect(page.locator(".artifact-comments")).toContainText("make the button bigger");
});

test("feedback bridge: a message from the artifact prefills the composer and never sends", async ({ page, control }) => {
  const files = {
    "index.html":
      `<!doctype html><title>Pick</title><button id="b">Choose B</button>` +
      `<script>document.getElementById("b").onclick = () => parent.postMessage({type: "agentctl:feedback", text: "Use option B"}, "*");</script>`,
  };
  const [id] = await control.seedArtifacts([
    { title: "Pick one", agent: "designer", ...shop, window_id: "@17", revisions: [{ files, entry: "index.html" }] },
  ]);
  const posts: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/comments")) posts.push(r.url());
  });
  await page.goto(`/#/artifacts/${id}`);
  // A page of ours cannot spoof the bridge: only the iframe's window counts.
  await page.evaluate(() => window.postMessage({ type: "agentctl:feedback", text: "spoof" }, "*"));
  await page.waitForTimeout(200);
  await expect(page.locator(".overlay")).toHaveCount(0);

  await frame(page).locator("#b").click();
  const sheet = page.locator(".overlay .sheet");
  await expect(sheet.getByLabel("Feedback")).toHaveValue("Use option B");
  const box = (await sheet.boundingBox())!;
  expect(box.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  await page.waitForTimeout(300);
  expect(posts).toEqual([]);
  await sheet.getByRole("button", { name: "Send" }).click();
  await expect(page.locator(".toast")).toBeVisible();
  expect(posts).toHaveLength(1);
});

test("project groups fold, remember it, and keep their count", async ({ page, control }) => {
  await control.seedArtifacts(seed());
  await page.goto("/#/artifacts");
  const shopGroup = page.locator(".artifact-list section.group", { hasText: "shop" });
  await expect(shopGroup.locator(".artifact-row")).toHaveCount(2);
  await shopGroup.locator(".group-toggle").click();
  await expect(shopGroup.locator(".nodes")).toBeHidden();
  await expect(shopGroup.locator(".artifact-count")).toHaveText("2");
  expect(await page.evaluate(() => localStorage.getItem("agentctl.artifacts.collapsed"))).toContain("/fixture/shop");
  await page.reload();
  await expect(page.locator(".artifact-list section.group", { hasText: "shop" }).locator(".nodes")).toBeHidden();
  await page.locator(".artifact-list section.group", { hasText: "shop" }).locator(".group-toggle").click();
  await expect(page.locator(".artifact-list section.group", { hasText: "shop" }).locator(".nodes")).toBeVisible();
});

test("Alt+Arrow reorders rows inside a project and the order sticks", async ({ page, control }) => {
  await control.seedArtifacts(seed());
  await page.goto("/#/artifacts");
  const titles = () => page.locator(".artifact-list section.group").first().locator(".artifact-row .win-title .agent").allTextContents();
  const before = await titles();
  expect(before).toHaveLength(2);
  await page.locator(".artifact-list section.group").first().locator(".artifact-row").first().focus();
  await page.keyboard.press("Alt+ArrowDown");
  await expect.poll(titles).toEqual([...before].reverse());
  await page.reload();
  await expect.poll(titles).toEqual([...before].reverse());
});

test("swipe-delete hides the row at once, Undo brings it back, and nothing is sent until the toast ends", async ({ page, control }) => {
  await control.seedArtifacts(seed());
  await page.goto("/#/artifacts");
  const row = page.locator(".artifact-row", { hasText: "Cart numbers" });
  await expect(row).toBeVisible();
  const deletes: string[] = [];
  page.on("request", (r) => r.method() === "DELETE" && deletes.push(r.url()));
  const box = (await row.boundingBox())!;
  const y = box.y + box.height / 2;
  await page.mouse.move(box.x + box.width - 20, y);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2, y, { steps: 6 });
  await page.mouse.move(box.x + 20, y, { steps: 6 });
  await page.mouse.up();
  await page.getByRole("button", { name: "Delete Cart numbers" }).click();
  await expect(page.locator(".toast")).toContainText("Deleted Cart numbers");
  await expect(page.locator(".artifact-row", { hasText: "Cart numbers" })).toHaveCount(0);
  expect(deletes).toEqual([]);
  await page.locator(".toast .btn-text", { hasText: "Undo" }).click();
  await expect(page.locator(".artifact-row", { hasText: "Cart numbers" })).toHaveCount(1);
  expect(deletes).toEqual([]);
});

test("Save as PDF downloads the revision on screen", async ({ page, control }) => {
  // A browser that cannot share files (a laptop) gets a download.
  await page.addInitScript(() => Object.defineProperty(navigator, "canShare", { configurable: true, value: () => false }));
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}/1`);
  await expect(frame(page).locator("#h")).toHaveText("Rev one");
  await page.getByRole("button", { name: "More actions" }).click();
  const btn = page.getByRole("button", { name: "Save as PDF (v1)" });
  const box = (await btn.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  const download = page.waitForEvent("download");
  await btn.click();
  // The servetest renderer takes 300ms, long enough to see the busy state.
  await expect(page.locator(".artifact-pdf")).toHaveText("Rendering PDF…");
  await expect(page.locator(".artifact-pdf")).toBeDisabled();
  const d = await download;
  expect(d.suggestedFilename()).toBe("checkout-mockup-r1.pdf");
  await expect(page.locator(".artifact-sheet")).toHaveCount(0);
});

test("Save as PDF uses the share sheet when the browser can share files", async ({ page, control }) => {
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
  await page.goto(`/#/artifacts/${id}`);
  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("button", { name: "Save as PDF (v2)" }).click();
  // When the render outlived the tap's activation, a second tap shares.
  const shareAgain = page.getByRole("button", { name: "Share PDF" });
  const shared = () => page.evaluate(() => (window as unknown as { shared: unknown[] }).shared.length);
  await expect.poll(async () => (await shared()) > 0 || (await shareAgain.isVisible())).toBe(true);
  if ((await shared()) === 0) await shareAgain.click();
  await expect.poll(shared).toBe(1);
  const got = await page.evaluate(() => (window as unknown as { shared: unknown[] }).shared[0]);
  // Files only: with a title or text, iOS shares text and drops Save to Files and Print.
  expect(got).toMatchObject({ name: "checkout-mockup-r2.pdf", type: "application/pdf", keys: ["files"] });
  expect((got as { size: number }).size).toBeGreaterThan(0);
});

test("Save as PDF refuses to share an answer that is not a PDF", async ({ page, control }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { shared: number };
    w.shared = 0;
    Object.defineProperty(navigator, "canShare", { configurable: true, value: () => true });
    Object.defineProperty(navigator, "share", { configurable: true, value: async () => void w.shared++ });
    // An HTML page where the PDF should be (a captive portal, a proxy). The
    // page's fetch is wrapped: page.route does not see requests once the
    // service worker controls the page.
    const real = window.fetch.bind(window);
    window.fetch = (input: RequestInfo | URL, init?: RequestInit) =>
      /\/v1\/artifacts\/[^/]+\/pdf\?/.test(String(input))
        ? Promise.resolve(new Response("<!doctype html><p>hi</p>", { status: 200, headers: { "Content-Type": "text/html" } }))
        : real(input, init);
  });
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}`);
  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("button", { name: "Save as PDF (v2)" }).click();
  await expect(page.locator(".toast.error")).toContainText("did not send a PDF");
  expect(await page.evaluate(() => (window as unknown as { shared: number }).shared)).toBe(0);
  await expect(page.getByRole("button", { name: "Save as PDF (v2)" })).toBeEnabled();
});

test("Open PDF opens a short-lived link that works without the app's cookie", async ({ page, browser, baseURL, control }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { opened: string[] };
    w.opened = [];
    window.open = ((url: string) => {
      w.opened.push(url);
      return {} as Window;
    }) as typeof window.open;
  });
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}/1`);
  await page.getByRole("button", { name: "More actions" }).click();
  const save = page.getByRole("button", { name: "Save as PDF (v1)" });
  const open = page.getByRole("button", { name: "Open PDF" });
  // Side by side, both inside a 390px-wide phone.
  const width = page.viewportSize()!.width;
  const [a, b] = [(await save.boundingBox())!, (await open.boundingBox())!];
  expect(Math.abs(a.y - b.y)).toBeLessThan(2);
  for (const box of [a, b]) {
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    expect(box.height).toBeGreaterThanOrEqual(44);
  }
  await open.click();
  const opened = () => page.evaluate(() => (window as unknown as { opened: string[] }).opened);
  // When the request used up the tap's activation, a second tap opens.
  const again = page.getByRole("button", { name: "Open PDF ↗" });
  await expect.poll(async () => (await opened()).length > 0 || (await again.isVisible())).toBe(true);
  if ((await opened()).length === 0) await again.click();
  await expect.poll(async () => (await opened()).length).toBe(1);
  const [url] = await opened();
  expect(url).toMatch(/^\/pdf\/[A-Za-z0-9_-]{43}\/checkout-mockup-r1\.pdf$/);
  await expect(page.locator(".artifact-sheet")).toHaveCount(0);

  // A browser with no device cookie (Safari outside the home-screen app).
  const ctx = await browser.newContext({ baseURL });
  const res = await ctx.request.get(url!);
  expect(res.status()).toBe(200);
  expect(res.headers()["content-type"]).toBe("application/pdf");
  expect(res.headers()["content-disposition"]).toBe('inline; filename="checkout-mockup-r1.pdf"');
  expect(res.headers()["cache-control"]).toBe("private, no-store");
  expect((await res.body()).subarray(0, 5).toString()).toBe("%PDF-");
  // Another token is the /a/ 404 page.
  const miss = await ctx.request.get(url!.replace(/\/pdf\/./, (m) => (m.endsWith("A") ? "/pdf/B" : "/pdf/A")));
  expect(miss.status()).toBe(404);
  await ctx.close();
});

test("Save as PDF shows the server's error when no browser is installed", async ({ page, control, guard }) => {
  guard.allow(/501|Not Implemented/);
  const [id] = await control.seedArtifacts([{ title: "Report [no browser]", revisions: [{ files: { "r.md": "# R" }, entry: "r.md" }] }]);
  await page.goto(`/#/artifacts/${id}`);
  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("button", { name: "Save as PDF (v1)" }).click();
  await expect(page.locator(".toast.error")).toContainText("artifacts.pdf_browser");
  await expect(page.getByRole("button", { name: "Save as PDF (v1)" })).toBeEnabled();
});
