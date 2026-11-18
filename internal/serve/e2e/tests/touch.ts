// Touch gesture synthesis. Playwright's touchscreen only taps, and WebKit
// has no protocol-level touch move, so drags are PointerEvents with
// pointerType "touch" dispatched on the row, the events swipe.js listens to.
// A real phone also follows some drags with a click on the row (mouse and
// Android always do once the finger lifts over the same element), which is
// what swipe.js's click suppression exists for, so drag() sends one too.
import type { Locator } from "@playwright/test";

export type DragOptions = {
  dx: number;
  dy?: number;
  /** Number of pointermove events. */
  steps?: number;
  /** Milliseconds between moves; slower drags avoid the fling velocity rule. */
  stepMs?: number;
  /** Lift the finger at the end (false leaves the gesture in progress). */
  release?: boolean;
  /** End with pointercancel instead of pointerup. */
  cancel?: boolean;
  /** Dispatch the click a browser may synthesize after the lift. */
  click?: boolean;
};

// Primary pointer ID: setPointerCapture only accepts an active pointer, and
// WebKit treats the primary (mouse) pointer as always active.
const POINTER_ID = 1;

/** drag performs a one-finger drag starting at the centre of target. */
export async function drag(target: Locator, opts: DragOptions) {
  await target.evaluate(
    async (el, o) => {
      const r = el.getBoundingClientRect();
      const x0 = r.left + r.width / 2;
      const y0 = r.top + r.height / 2;
      const fire = (type: string, x: number, y: number, extra: PointerEventInit = {}) =>
        el.dispatchEvent(
          new PointerEvent(type, {
            pointerId: o.pointerId,
            pointerType: "touch",
            isPrimary: true,
            bubbles: true,
            cancelable: true,
            composed: true,
            clientX: x,
            clientY: y,
            button: 0,
            buttons: type === "pointerup" || type === "pointercancel" ? 0 : 1,
            ...extra,
          }),
        );
      const sleep = (ms: number) => new Promise((res) => setTimeout(res, ms));
      (window as unknown as { __drag: unknown }).__drag = { x0, y0 };
      fire("pointerdown", x0, y0);
      const steps = o.steps ?? 8;
      for (let i = 1; i <= steps; i++) {
        await sleep(o.stepMs ?? 16);
        fire("pointermove", x0 + (o.dx * i) / steps, y0 + ((o.dy ?? 0) * i) / steps);
      }
      if (o.release === false) return;
      const x1 = x0 + o.dx;
      const y1 = y0 + (o.dy ?? 0);
      fire(o.cancel ? "pointercancel" : "pointerup", x1, y1);
      if (o.click !== false && !o.cancel) {
        el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, clientX: x1, clientY: y1 }));
      }
    },
    { ...opts, pointerId: POINTER_ID },
  );
}

/** move continues an open gesture to offset (dx, dy) from where it started. */
export async function move(target: Locator, dx: number, dy = 0) {
  await target.evaluate(
    (el, o) => {
      const { x0, y0 } = (window as unknown as { __drag: { x0: number; y0: number } }).__drag;
      el.dispatchEvent(
        new PointerEvent("pointermove", {
          pointerId: o.pointerId, pointerType: "touch", isPrimary: true, bubbles: true, cancelable: true,
          clientX: x0 + o.dx, clientY: y0 + o.dy, button: 0, buttons: 1,
        }),
      );
    },
    { dx, dy, pointerId: POINTER_ID },
  );
}

/** lift ends a gesture left open by drag({release: false}) at offset (dx, dy) from its start. */
export async function lift(target: Locator, dx: number, dy = 0) {
  await target.evaluate(
    (el, o) => {
      // The row has moved under the finger; measure from where it went down.
      const { x0, y0 } = (window as unknown as { __drag: { x0: number; y0: number } }).__drag;
      el.dispatchEvent(
        new PointerEvent("pointerup", {
          pointerId: o.pointerId, pointerType: "touch", isPrimary: true, bubbles: true, cancelable: true,
          clientX: x0 + o.dx, clientY: y0 + o.dy, button: 0, buttons: 0,
        }),
      );
    },
    { dx, dy, pointerId: POINTER_ID },
  );
}

/** offsetX is the row's current translateX in px (0 when untransformed). */
export async function offsetX(node: Locator): Promise<number> {
  return node.evaluate((el) => {
    const t = getComputedStyle(el).transform;
    return !t || t === "none" ? 0 : new DOMMatrixReadOnly(t).m41;
  });
}
