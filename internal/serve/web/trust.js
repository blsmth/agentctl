// The folder-trust card: Claude Code's "trust this folder?" dialog as two
// buttons, shown on a trust-blocked fleet row and on its window screen.
import { TRUST_EXPLAIN, TRUST_KEY_EXIT, TRUST_KEY_TRUST, TRUST_TITLE, trustExitConfirm } from "./logic.js";
import { confirmSheet, h } from "./ui.js";

/**
 * trustCard renders the card for folder. answer(key) sends the answer
 * (TRUST_KEY_TRUST or TRUST_KEY_EXIT) and returns a promise; exit asks for a
 * confirm first. While an answer is in flight both buttons are disabled, so
 * one tap sends one answer. extraClass adds a class to the card ("row-trust"
 * on the fleet list).
 */
export function trustCard(folder, answer, extraClass = "") {
  let busy = false;
  const trustBtn = h("button", { class: "btn choice primary", type: "button", "data-trust": TRUST_KEY_TRUST }, "Trust folder");
  const exitBtn = h("button", { class: "btn choice danger", type: "button", "data-trust": TRUST_KEY_EXIT }, "Exit session");
  const go = async (key, e) => {
    e?.preventDefault();
    e?.stopPropagation();
    if (busy) return;
    if (key === TRUST_KEY_EXIT && !(await confirmSheet(trustExitConfirm(folder)))) return;
    busy = true;
    trustBtn.disabled = exitBtn.disabled = true;
    try {
      await answer(key);
    } finally {
      busy = false;
      trustBtn.disabled = exitBtn.disabled = false;
    }
  };
  trustBtn.onclick = (e) => go(TRUST_KEY_TRUST, e);
  exitBtn.onclick = (e) => go(TRUST_KEY_EXIT, e);
  return h(
    "section",
    { class: `answer trust-card${extraClass ? ` ${extraClass}` : ""}`, "aria-label": TRUST_TITLE },
    h("p", { class: "question" }, TRUST_TITLE),
    folder ? h("p", { class: "trust-folder" }, folder) : h("p", { class: "note" }, "The folder path is not shown; check it on the terminal."),
    h("p", { class: "trust-explain" }, TRUST_EXPLAIN),
    h("div", { class: "choices trust-choices" }, trustBtn, exitBtn),
  );
}
