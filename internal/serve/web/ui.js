// Small DOM helpers. Text is only ever set through textContent / text nodes,
// never as HTML, so pane and agent text cannot inject markup.
import { fitFontSize } from "./logic.js";

/** h builds an element: h("button", {class: "btn", onclick: fn}, "label", child...). */
export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs ?? {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat()) {
    if (c === undefined || c === null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

export function clear(el) {
  el.replaceChildren();
}

/** toast shows a short message at the bottom of the screen. */
export function toast(message, kind = "info") {
  const host = document.getElementById("toasts");
  const t = h("div", { class: `toast ${kind}`, role: "status" }, message);
  host.append(t);
  setTimeout(() => t.remove(), 4000);
}

/**
 * confirmSheet shows a bottom sheet with a title and body lines and resolves
 * true on confirm, false on cancel. Navigating away (hashchange) cancels it:
 * the sheet belongs to the view that opened it, and confirming it from the
 * next view would act on the previous one's target.
 */
export function confirmSheet({ title, lines = [], confirmLabel = "Confirm", danger = false }) {
  return new Promise((resolve) => {
    let settled = false;
    const done = (v) => {
      if (settled) return;
      settled = true;
      removeEventListener("hashchange", onNavigate);
      overlay.remove();
      resolve(v);
    };
    const onNavigate = () => done(false);
    addEventListener("hashchange", onNavigate, { once: true });
    const overlay = h(
      "div",
      { class: "overlay", onclick: (e) => e.target === overlay && done(false) },
      h(
        "div",
        { class: "sheet", role: "dialog", "aria-modal": "true" },
        h("h2", {}, title),
        ...lines.map((l) => h("pre", { class: "sheet-body" }, l)),
        h(
          "div",
          { class: "row" },
          h("button", { class: "btn", onclick: () => done(false) }, "Cancel"),
          h("button", { class: `btn ${danger ? "danger" : "primary"}`, onclick: () => done(true) }, confirmLabel),
        ),
      ),
    );
    document.body.append(overlay);
  });
}

/**
 * fitTermFont scales an xterm's font so its current columns fill availWidth.
 * Shared by the live terminal and the exact pane view; never changes cols.
 * Returns true when the font changed.
 */
export function fitTermFont(term, host, availWidth, min, max) {
  const screenEl = host.querySelector(".xterm-screen");
  const measured = screenEl ? screenEl.getBoundingClientRect().width : 0;
  const f = fitFontSize(availWidth, measured, term.options.fontSize, min, max);
  if (Math.abs(f - term.options.fontSize) < 0.1) return false;
  term.options.fontSize = f;
  return true;
}
