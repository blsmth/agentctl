// Swipe-left-to-close gesture for a fleet row. DOM only; the decisions
// (direction lock, commit threshold) are pure functions in logic.js.
import { SWIPE_REVEAL, swipeAxis, swipeCommit, swipeOffset } from "./logic.js";

const reducedMotion = () => globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;

// capture runs a pointer-capture call, ignoring the InvalidStateError WebKit
// raises when the pointer is no longer active (the gesture is over anyway).
const capture = (fn) => {
  try {
    fn();
  } catch {
    // nothing to capture or release
  }
};

// Only one row is open at a time; opening another snaps this one back.
let openRow = null; // { snapBack() }

/**
 * attachSwipe makes `node` slide left under a drag that starts on `handle`.
 * A committed release snaps the row open (iOS Mail style) to reveal `action`,
 * a real button; tapping it calls `onClose()`, which resolves true when the row
 * should go away (collapsed) or false to snap back. Tapping the row, swiping it
 * back, scrolling, tapping elsewhere, opening another row or navigating all
 * snap it back. `dragging()` says a long-press drag owns the pointer (drag.js)
 * so the swipe stands down. `onBusy(true|false)` is held for as long as the row is open or
 * being dragged, and released exactly once on every exit path.
 */
export function attachSwipe(wrap, node, handle, action, { onClose, onBusy, dragging = () => false }) {
  let start = null; // { id, x, y, t, axis, width, samples, base }
  let isOpen = false;
  let busy = false;
  let closing = false;
  const self = { snapBack: () => snapBack() };

  let releaseTimer = 0;
  const setBusy = (on) => {
    clearTimeout(releaseTimer);
    if (busy === on) return;
    busy = on;
    onBusy(on);
  };

  // Release the render hold only once the snap animation and the click that may
  // trail a tap are over; a render in between would rebuild the row under that click.
  const setBusySoon = () => {
    clearTimeout(releaseTimer);
    releaseTimer = setTimeout(() => setBusy(false), 450);
  };

  // The gesture hold: taken on pointerdown so a render cannot rebuild the row
  // before the axis locks, released exactly once when the gesture ends. It is
  // separate from `busy` (the x-locked drag / open row hold). A tap releases late
  // so a render cannot replace the link before its click lands.
  let gestureHeld = false;
  let gestureTimer = 0;
  const holdGesture = () => {
    clearTimeout(gestureTimer);
    if (gestureHeld) return;
    gestureHeld = true;
    onBusy(true);
  };
  const releaseGesture = (delay = 0) => {
    clearTimeout(gestureTimer);
    const done = () => {
      if (!gestureHeld) return;
      gestureHeld = false;
      onBusy(false);
    };
    if (delay > 0) gestureTimer = setTimeout(done, delay);
    else done();
  };

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

  // While open, a tap on the row content closes the reveal instead of navigating.
  let openedAt = 0;
  const openGuard = (e) => {
    e.preventDefault();
    e.stopPropagation();
    // The click that trails the opening swipe's own lift is not a tap.
    if (e.timeStamp - openedAt > 150) snapBack();
  };
  const outside = (e) => {
    if (isOpen && !action.contains(e.target) && !handle.contains(e.target)) snapBack();
  };

  function open(width) {
    if (openRow && openRow !== self) openRow.snapBack();
    openRow = self;
    isOpen = true;
    openedAt = performance.now();
    setBusy(true);
    setX(-Math.min(SWIPE_REVEAL, width), true);
    handle.addEventListener("click", openGuard, true);
    document.addEventListener("pointerdown", outside, true);
    addEventListener("hashchange", snapBack);
  }

  function reset() {
    isOpen = false;
    if (openRow === self) openRow = null;
    handle.removeEventListener("click", openGuard, true);
    document.removeEventListener("pointerdown", outside, true);
    removeEventListener("hashchange", snapBack);
  }

  function snapBack() {
    if (!isOpen || closing) return;
    reset();
    setX(0, true);
    if (!start) setBusySoon();
  }

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

  action.addEventListener("click", async (e) => {
    e.preventDefault();
    if (!isOpen || closing) return;
    closing = true;
    let ok = false;
    try {
      ok = await onClose();
    } catch {
      ok = false;
    }
    closing = false;
    if (ok) {
      reset();
      setX(-node.offsetWidth, true);
      collapse();
      setBusy(false);
    } else {
      snapBack();
    }
  });

  function release(commit, width, back) {
    start = null;
    if (isOpen) {
      if (back) snapBack();
      else setX(-Math.min(SWIPE_REVEAL, width), true);
      return;
    }
    if (commit) open(width);
    else {
      setX(0, true);
      setBusySoon();
    }
  }

  handle.addEventListener("pointerdown", (e) => {
    if (start || closing || (e.pointerType === "mouse" && e.button !== 0)) return;
    holdGesture();
    const width = node.offsetWidth;
    const base = isOpen ? -Math.min(SWIPE_REVEAL, width) : 0;
    start = { id: e.pointerId, x: e.clientX, y: e.clientY, t: e.timeStamp, axis: "", width, base, samples: [[e.timeStamp, e.clientX]] };
  });

  handle.addEventListener("pointermove", (e) => {
    if (!start || e.pointerId !== start.id) return;
    if (dragging()) {
      start = null;
      releaseGesture();
      return;
    }
    const dx = e.clientX - start.x;
    const dy = e.clientY - start.y;
    if (!start.axis) {
      start.axis = swipeAxis(dx, dy);
      if (start.axis === "y") {
        start = null; // page scroll wins
        snapBack();
        releaseGesture();
        return;
      }
      if (start.axis === "x") {
        setBusy(true);
        capture(() => handle.setPointerCapture?.(e.pointerId));
      }
    }
    if (start?.axis !== "x") return;
    e.preventDefault();
    start.samples.push([e.timeStamp, e.clientX]);
    if (start.samples.length > 6) start.samples.shift();
    setX(swipeOffset(start.base + dx, start.width), false);
  });

  const end = (e, cancelled) => {
    if (!start || e.pointerId !== start.id) return;
    const s = start;
    if (s.axis !== "x") {
      start = null;
      releaseGesture(cancelled ? 0 : 450);
      if (isOpen) {
        if (!cancelled) suppressClick(); // a tap on the open row only closes the reveal
        snapBack();
      }
      return;
    }
    suppressClick();
    capture(() => handle.releasePointerCapture?.(e.pointerId));
    const dx = e.clientX - s.x;
    const first = s.samples[0];
    const dt = e.timeStamp - first[0];
    const velocity = dt > 0 ? (e.clientX - first[1]) / dt : 0;
    const commit = !cancelled && swipeCommit(swipeOffset(s.base + dx, s.width), s.width, velocity);
    release(commit, s.width, cancelled || dx > 0);
    releaseGesture();
  };
  handle.addEventListener("pointerup", (e) => end(e, false));
  handle.addEventListener("pointercancel", (e) => end(e, true));
}
