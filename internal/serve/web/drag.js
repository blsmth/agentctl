// Long-press-and-drag reordering, shared by the fleet's project groups and the
// artifact list's groups and rows. DOM only; the decisions (drop slot,
// autoscroll) are pure functions in logic.js.
import { LONG_PRESS_MS, LONG_PRESS_SLOP, autoscrollDelta, dropIndex } from "./logic.js";

/**
 * attachDrag lets `item` be reordered among its siblings by holding `handle`
 * (long press, or a held mouse button) and dragging. A short tap is left to
 * the handle's own click; a touch that starts scrolling before the hold
 * completes is cancelled by the browser (pointercancel) or by the slop check,
 * so the page scrolls as usual. Pointer Events only; while a drag is live the
 * page is kept from scrolling under the finger and autoscrolls near the top
 * and bottom edges.
 *
 * Options:
 *  - list: the container; it gets `listClass` while dragging.
 *  - siblings(): the elements `item` can land among (without `item`).
 *  - onBusy(true|false) brackets the whole gesture so renders are held.
 *  - onDrop(index): the target slot among the siblings (an index into the
 *    list with `item` removed).
 *  - mover: the element that follows the pointer (default: the handle).
 *  - mid(el): an element's vertical midpoint for the drop slot (default: the
 *    middle of its first 56px, a group collapsed to its header).
 *  - ignore: a selector for pointerdown targets that must not start a press.
 *  - listClass, liftClass: the classes set on the list / item while dragging.
 *
 * It returns { active() }, true from the start of a drag to its end.
 */
export function attachDrag(item, handle, { list, siblings, onBusy, onDrop, mover = handle, mid, ignore = "", listClass = "reordering", liftClass = "lifted" }) {
  let press = null; // { id, x, y, timer, buttons }
  let drag = null; // { id, grab, y, slot, raf }

  const midOf = mid ?? ((el) => {
    const r = el.getBoundingClientRect();
    return r.top + Math.min(r.height, 56) / 2; // collapsed while dragging: ~ the header
  });

  const place = () => {
    // Follow the pointer: measure the mover's natural spot, then offset it.
    mover.style.transform = "";
    const natural = mover.getBoundingClientRect().top;
    mover.style.transform = `translateY(${drag.y - drag.grab - natural}px)`;
    const rest = siblings();
    drag.slot = dropIndex(rest.map(midOf), drag.y);
    rest.forEach((el, i) => {
      el.classList.toggle("drop-before", i === drag.slot);
      el.classList.toggle("drop-after", i === rest.length - 1 && drag.slot === rest.length);
    });
  };

  const frame = () => {
    if (!drag) return;
    const dy = autoscrollDelta(drag.y, window.innerHeight);
    if (dy !== 0) {
      window.scrollBy(0, dy);
      place();
    }
    drag.raf = requestAnimationFrame(frame);
  };

  const blockScroll = (e) => e.preventDefault();

  // The press holds renders from pointerdown, so the list is not rebuilt
  // under a finger that is still deciding between tap, scroll and drag. A
  // tap's release is delayed past its click, which a render would swallow.
  // A mouse has no implicit pointer capture, so a press released off the
  // handle never reaches it. While a press is pending, watch the window too.
  const onWinMove = (e) => {
    if (press && e.pointerId === press.id) press.buttons = e.buttons;
  };
  const onWinEnd = (e) => {
    if (press && e.pointerId === press.id) clearPress();
  };
  const unwatchPress = () => {
    window.removeEventListener("pointermove", onWinMove);
    window.removeEventListener("pointerup", onWinEnd);
    window.removeEventListener("pointercancel", onWinEnd);
  };

  const clearPress = (delayMs = 0) => {
    if (!press) return;
    clearTimeout(press.timer);
    press = null;
    unwatchPress();
    if (delayMs > 0) setTimeout(() => onBusy(false), delayMs);
    else onBusy(false);
  };

  const start = () => {
    const p = press;
    press = null;
    unwatchPress();
    // The press already holds renders (busy). Still, never drag an item
    // that has left the page, or one whose pointer was already released.
    if (!p || !handle.isConnected || p.buttons === 0) {
      onBusy(false);
      return;
    }
    drag = { id: p.id, grab: p.y - mover.getBoundingClientRect().top, y: p.y, slot: 0, raf: 0 };
    try {
      handle.setPointerCapture?.(p.id);
    } catch {
      // No active pointer to capture (it lifted already): drop the gesture.
      drag = null;
      onBusy(false);
      return;
    }
    // touch-action only governs a gesture before it starts scrolling, so also
    // veto touchmove (iOS) while the drag is live.
    handle.classList.add("dragging");
    window.addEventListener("touchmove", blockScroll, { passive: false });
    list.classList.add(listClass);
    item.classList.add(liftClass);
    navigator.vibrate?.(12);
    place();
    drag.raf = requestAnimationFrame(frame);
  };

  const finish = (drop) => {
    if (!drag) return;
    const d = drag;
    drag = null;
    cancelAnimationFrame(d.raf);
    window.removeEventListener("touchmove", blockScroll);
    handle.releasePointerCapture?.(d.id);
    mover.style.transform = "";
    handle.classList.remove("dragging");
    list.classList.remove(listClass);
    item.classList.remove(liftClass);
    siblings().forEach((el) => el.classList.remove("drop-before", "drop-after"));
    // The click that follows the release must not activate the handle.
    const stop = (e) => {
      e.preventDefault();
      e.stopPropagation();
    };
    handle.addEventListener("click", stop, { capture: true, once: true });
    setTimeout(() => handle.removeEventListener("click", stop, { capture: true }), 400);
    if (drop) onDrop(d.slot);
    onBusy(false);
  };

  handle.addEventListener("pointerdown", (e) => {
    if (press || drag || (e.pointerType === "mouse" && e.button !== 0) || (ignore && e.target.closest(ignore))) return;
    onBusy(true);
    press = { id: e.pointerId, x: e.clientX, y: e.clientY, timer: setTimeout(start, LONG_PRESS_MS), buttons: e.buttons };
    window.addEventListener("pointermove", onWinMove);
    window.addEventListener("pointerup", onWinEnd);
    window.addEventListener("pointercancel", onWinEnd);
  });
  handle.addEventListener("pointermove", (e) => {
    if (press && e.pointerId === press.id && Math.hypot(e.clientX - press.x, e.clientY - press.y) > LONG_PRESS_SLOP) clearPress();
    if (!drag || e.pointerId !== drag.id) return;
    e.preventDefault();
    drag.y = e.clientY;
    place();
  });
  handle.addEventListener("pointerup", (e) => {
    if (press && e.pointerId === press.id) clearPress(150);
    if (drag && e.pointerId === drag.id) finish(true);
  });
  handle.addEventListener("pointercancel", (e) => {
    if (press && e.pointerId === press.id) clearPress();
    if (drag && e.pointerId === drag.id) finish(false);
  });
  // A long press on Android opens a context menu; it would end the drag.
  handle.addEventListener("contextmenu", (e) => {
    if (press || drag) e.preventDefault();
  });
  return { active: () => drag !== null };
}
