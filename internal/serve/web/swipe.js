// Swipe-left-to-close gesture for a fleet row. DOM only; the decisions
// (direction lock, commit threshold) are pure functions in logic.js.
import { swipeAxis, swipeCommit, swipeOffset } from "./logic.js";

const reducedMotion = () => globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;

/**
 * attachSwipe makes `node` slide left under a drag that starts on `handle`.
 * A committed release calls `onCommit()`, which resolves true when the row
 * should go away (collapsed) or false to snap back. `onBusy(true|false)` brackets
 * every gesture so the caller can defer re-renders; the false call always runs.
 */
export function attachSwipe(wrap, node, handle, { onCommit, onBusy }) {
  let start = null; // { id, x, y, t, axis, width, samples }
  let settling = false;

  const setX = (x, animate) => {
    node.style.transition = animate && !reducedMotion() ? "transform .2s ease-out" : "none";
    node.style.transform = x === 0 ? "" : `translateX(${x}px)`;
    wrap.classList.toggle("swiping", x !== 0);
  };

  const suppressClick = () => {
    const stop = (e) => {
      e.preventDefault();
      e.stopPropagation();
    };
    handle.addEventListener("click", stop, { capture: true, once: true });
    // A swipe that produces no click must not leave the guard behind for a later tap.
    setTimeout(() => handle.removeEventListener("click", stop, { capture: true }), 400);
  };

  const finish = (x) => {
    start = null;
    setX(x, true);
  };

  const collapse = () => {
    if (reducedMotion()) {
      wrap.classList.add("swipe-gone");
      return;
    }
    wrap.style.height = `${wrap.offsetHeight}px`;
    wrap.style.overflow = "hidden";
    wrap.style.transition = "height .2s ease-out";
    requestAnimationFrame(() => {
      wrap.style.height = "0px";
    });
  };

  async function release(commit, width) {
    settling = true;
    try {
      if (!commit) {
        setX(0, true);
        return;
      }
      setX(-width * 0.5, true); // hold part-way, action visible, while confirming
      let ok = false;
      try {
        ok = await onCommit();
      } catch {
        ok = false;
      }
      if (ok) {
        setX(-width, true);
        collapse();
      } else {
        setX(0, true);
      }
    } finally {
      settling = false;
      start = null;
      onBusy(false);
    }
  }

  handle.addEventListener("pointerdown", (e) => {
    if (settling || start || (e.pointerType === "mouse" && e.button !== 0)) return;
    start = { id: e.pointerId, x: e.clientX, y: e.clientY, t: e.timeStamp, axis: "", width: node.offsetWidth, samples: [[e.timeStamp, e.clientX]] };
  });

  handle.addEventListener("pointermove", (e) => {
    if (!start || e.pointerId !== start.id || settling) return;
    const dx = e.clientX - start.x;
    const dy = e.clientY - start.y;
    if (!start.axis) {
      start.axis = swipeAxis(dx, dy);
      if (start.axis === "y") {
        start = null; // page scroll wins
        return;
      }
      if (start.axis === "x") {
        onBusy(true);
        handle.setPointerCapture?.(e.pointerId);
      }
    }
    if (start?.axis !== "x") return;
    e.preventDefault();
    start.samples.push([e.timeStamp, e.clientX]);
    if (start.samples.length > 6) start.samples.shift();
    setX(swipeOffset(dx, start.width), false);
  });

  const end = (e, cancelled) => {
    if (!start || e.pointerId !== start.id || settling) return;
    const s = start;
    if (s.axis !== "x") {
      start = null;
      return;
    }
    suppressClick();
    handle.releasePointerCapture?.(e.pointerId);
    const dx = e.clientX - s.x;
    const first = s.samples[0];
    const dt = e.timeStamp - first[0];
    const velocity = dt > 0 ? (e.clientX - first[1]) / dt : 0;
    const commit = !cancelled && swipeCommit(swipeOffset(dx, s.width), s.width, velocity);
    release(commit, s.width);
  };
  handle.addEventListener("pointerup", (e) => end(e, false));
  handle.addEventListener("pointercancel", (e) => end(e, true));
}
