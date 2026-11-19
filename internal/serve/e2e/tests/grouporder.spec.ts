// Reordering project groups by long-press and drag (fleet.js attachGroupDrag),
// driven by touch pointers on the group header.
import type { Page } from "@playwright/test";
import { expect, openFleet, test } from "./fixtures";

const names = (page: Page) => page.locator(".fleet section.group h2");

/** hold presses on the header of `name`, waits out the long press, moves to the header of `to`, and optionally lifts. */
async function holdAndDrag(page: Page, name: string, to: string, opts: { hold?: number; lift?: boolean } = {}) {
  await page.evaluate(
    async ({ name, to, hold, lift }) => {
      const head = (n: string) =>
        [...document.querySelectorAll<HTMLElement>(".fleet section.group")].find((s) => s.querySelector("h2")?.textContent === n)!.querySelector<HTMLElement>(".group-head")!;
      const from = head(name);
      const r = from.getBoundingClientRect();
      const x = r.left + 40;
      const y = r.top + r.height / 2;
      const fire = (el: Element, type: string, cy: number) =>
        el.dispatchEvent(
          new PointerEvent(type, {
            pointerId: 1, pointerType: "touch", isPrimary: true, bubbles: true, cancelable: true, composed: true,
            clientX: x, clientY: cy, button: 0, buttons: type === "pointerup" ? 0 : 1,
          }),
        );
      const sleep = (ms: number) => new Promise((res) => setTimeout(res, ms));
      fire(from, "pointerdown", y);
      await sleep(hold);
      // Target the lower half of the destination header's group.
      const t = head(to).getBoundingClientRect();
      const ty = t.top + t.height + 4;
      for (let i = 1; i <= 6; i++) {
        await sleep(16);
        fire(from, "pointermove", y + ((ty - y) * i) / 6);
      }
      if (lift) fire(from, "pointerup", ty);
    },
    { name, to, hold: opts.hold ?? 500, lift: opts.lift ?? true },
  );
}

test.beforeEach(async ({ page }) => {
  await openFleet(page);
});

test("long-press and drag moves a group, and the order survives a reload", async ({ page }) => {
  await expect(names(page)).toHaveText(["shop", "blog", "hub"]);
  await holdAndDrag(page, "shop", "hub");
  await expect(names(page)).toHaveText(["blog", "hub", "shop"]);
  await page.reload();
  await expect(names(page)).toHaveText(["blog", "hub", "shop"]);
});

test("a short press and drag scrolls instead of reordering", async ({ page }) => {
  await holdAndDrag(page, "shop", "hub", { hold: 100 });
  await expect(names(page)).toHaveText(["shop", "blog", "hub"]);
});

test("a tap still folds the group", async ({ page }) => {
  const shop = page.locator(".fleet section.group").first();
  await shop.locator(".group-toggle").tap();
  await expect(shop).toHaveClass(/\bfolded\b/);
  await expect(names(page)).toHaveText(["shop", "blog", "hub"]);
});

test("Alt+ArrowDown on a header moves the group one slot", async ({ page }) => {
  await page.locator(".fleet section.group .group-toggle").first().focus();
  await page.keyboard.press("Alt+ArrowDown");
  await expect(names(page)).toHaveText(["blog", "shop", "hub"]);
  await expect(page.locator(".fleet .group-toggle", { hasText: "shop" })).toBeFocused();
});

test("a render during the hold does not freeze later renders", async ({ page, control }) => {
  // Press and hold the shop header, and let the fleet re-render under the
  // finger before the long press fires: the old header is gone by then.
  const held = page.evaluate(async () => {
    const head = document.querySelector<HTMLElement>(".fleet section.group .group-head")!;
    const r = head.getBoundingClientRect();
    const ev = (type: string) =>
      new PointerEvent(type, { pointerId: 1, pointerType: "touch", isPrimary: true, bubbles: true, cancelable: true, clientX: r.left + 40, clientY: r.top + 10, button: 0, buttons: type === "pointerup" ? 0 : 1 });
    head.dispatchEvent(ev("pointerdown"));
    await new Promise((res) => setTimeout(res, 700));
    // The finger lifts on whatever header is now under it.
    document.querySelector(".fleet section.group .group-head")!.dispatchEvent(ev("pointerup"));
  });
  await control.patchWindow("agentctl-shop", "@11", { activity: "changed during the hold" });
  await held;
  await expect(page.getByText("changed during the hold")).toBeVisible();
  await control.patchWindow("agentctl-shop", "@11", { activity: "changed after the hold" });
  await expect(page.getByText("changed after the hold")).toBeVisible();
  await expect(names(page)).toHaveText(["shop", "blog", "hub"]);
});

test("a mouse press released off the header does not leave the group lifted", async ({ page, control }) => {
  const box = (await page.locator(".fleet section.group .group-head").first().boundingBox())!;
  await page.mouse.move(box.x + 40, box.y + box.height / 2);
  await page.mouse.down();
  // One jump off the header (no intermediate moves over it), then release.
  await page.mouse.move(box.x + 40, box.y + box.height + 300);
  await page.mouse.up();
  await page.waitForTimeout(700);
  await expect(page.locator(".fleet section.lifted")).toHaveCount(0);
  await expect(page.locator(".fleet.reordering")).toHaveCount(0);
  await control.patchWindow("agentctl-shop", "@11", { activity: "changed after the mouse release" });
  await expect(page.getByText("changed after the mouse release")).toBeVisible();
});
