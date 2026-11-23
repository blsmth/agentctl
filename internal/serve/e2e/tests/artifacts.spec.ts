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
  await expect(mock).toContainText("2 revs");
  await expect(mock.locator(".artifact-pin")).toBeVisible();
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
  await expect(page.getByRole("link", { name: /Open in new tab/ })).toHaveAttribute("href", /^\/a\/t\//);
  await expect(page.getByRole("link", { name: /Open in new tab/ })).toHaveAttribute("target", "_blank");
  // Origin chips; the window is live in the fixture.
  await expect(page.locator(".artifact-tools .chip", { hasText: "designer" })).toBeVisible();
  await expect(page.locator(".artifact-tools a.chip")).toHaveAttribute("href", "#/w/agentctl-shop/%4017");
});

test("the revision picker switches content", async ({ page, control }) => {
  const [id] = await control.seedArtifacts(seed());
  await page.goto(`/#/artifacts/${id}`);
  await expect(frame(page).locator("#h")).toHaveText("Rev two");
  await page.getByLabel("Revision").selectOption("1");
  await expect(page).toHaveURL(new RegExp(`#/artifacts/${id}/1$`));
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
  await page.getByRole("button", { name: "Pin", exact: true }).click();
  await expect(page.getByRole("button", { name: "Unpin" })).toBeVisible();
  await page.getByRole("button", { name: "Delete" }).click();
  await expect(page.locator(".overlay .sheet")).toContainText("Delete Cart numbers?");
  await page.locator(".overlay .sheet").getByRole("button", { name: "Cancel" }).click();
  expect((await page.request.get(`/v1/artifacts/${ids[1]}`)).status()).toBe(200);
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
  await page.getByRole("link", { name: "Back to artifacts" }).click();
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
