// The Host screen opens from Settings, renders the connection and host
// sections, and stops polling /v1/host once the user leaves it.
import { expect, openFleet, test } from "./fixtures";

test("host screen opens from settings and stops polling on leave", async ({ page, guard }) => {
  let calls = 0;
  page.on("request", (r) => {
    if (new URL(r.url()).pathname === "/v1/host") calls++;
  });
  await openFleet(page);
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page.getByRole("link", { name: /Host status/ }).click();
  await expect(page.locator("header.bar h1")).toHaveText("Host");
  await expect(page.locator(".hsec h2", { hasText: "Connection" })).toBeVisible();
  await expect(page.locator(".hkv", { hasText: "Round trip" })).toContainText("ms");
  await expect(page.locator(".htable").first()).toBeVisible();
  expect(guard.unexpected()).toEqual([]);

  await page.getByRole("link", { name: /Settings/ }).first().click();
  const seen = calls;
  await page.waitForTimeout(100);
  expect(calls).toBe(seen);
});
