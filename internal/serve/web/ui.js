// Small DOM helpers. Text is only ever set through textContent / text nodes,
// never as HTML, so pane and agent text cannot inject markup.

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
 * true on confirm, false on cancel.
 */
export function confirmSheet({ title, lines = [], confirmLabel = "Confirm", danger = false }) {
  return new Promise((resolve) => {
    const done = (v) => {
      overlay.remove();
      resolve(v);
    };
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
