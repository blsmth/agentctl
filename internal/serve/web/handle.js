// #/s/<handle>: a session by handle. The server resolves it to a live window
// (opened at once) or a closed session (said so, with how to resume it).
import { get } from "./api.js";
import { handleApiPath, handleOutcome } from "./logic.js";
import { h } from "./ui.js";

export function mountHandle(root, _ctx, { handle }) {
  let gone = false;
  const body = h("div", { class: "empty" }, h("p", { class: "muted" }, "Looking up session…"));
  root.append(h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Back"), h("h1", {}, handle)), body);
  (async () => {
    let res = null;
    let err = null;
    try {
      res = await get(handleApiPath(handle));
    } catch (e) {
      err = e;
    }
    if (gone) return;
    const out = handleOutcome(handle, res, err);
    if (out.go) {
      location.replace(out.go);
      return;
    }
    body.replaceChildren(h("p", { style: "white-space: pre-wrap" }, out.message));
  })();
  return () => {
    gone = true;
  };
}
