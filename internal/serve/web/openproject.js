// Fleet's "Open project" sheet, the hub's "+ open" on the phone: recent
// project dirs (GET /v1/projects/recent) plus a path field. Choosing one asks
// the laptop to start that project's agentctl session detached (POST
// /v1/projects/open); the caller focuses the project's group when it appears.
import {
  RECENT_PROJECTS_PATH,
  buildOpenProjectRequest,
  filterRecentProjects,
  openPathProblem,
} from "./logic.js";
import { get, send, ApiError } from "./api.js";
import { clear, h } from "./ui.js";

/**
 * openProjectSheet shows the sheet. onOpened(res) runs with the server's
 * answer ({ session, dir, running }) after a successful open, once the sheet
 * has closed. A refused open keeps the sheet up with the server's message.
 * Like the other sheets it closes on navigation.
 */
export function openProjectSheet({ onOpened }) {
  let recent = null; // null while loading
  let busy = false;

  const onNavigate = () => overlay.remove();
  const close = () => {
    removeEventListener("hashchange", onNavigate);
    overlay.remove();
  };
  addEventListener("hashchange", onNavigate, { once: true });

  const input = h("input", {
    type: "text",
    class: "open-input",
    placeholder: "~/code/project",
    "aria-label": "Project directory",
    autocapitalize: "off",
    autocorrect: "off",
    autocomplete: "off",
    spellcheck: "false",
    enterkeyhint: "go",
    oninput: () => {
      err.hidden = true;
      renderList();
    },
    onkeydown: (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        submit(input.value);
      }
    },
  });
  const err = h("p", { class: "sheet-error", role: "alert", hidden: true });
  const list = h("div", { class: "open-recent", role: "list", "aria-label": "Recent projects" });
  const openBtn = h("button", { class: "btn primary", type: "button", onclick: () => submit(input.value) }, "Open");
  const cancelBtn = h("button", { class: "btn", type: "button", onclick: close }, "Cancel");

  function showError(msg) {
    err.textContent = msg;
    err.hidden = false;
  }

  function setBusy(on) {
    busy = on;
    input.disabled = on;
    openBtn.disabled = on;
    openBtn.textContent = on ? "Opening…" : "Open";
    for (const b of list.querySelectorAll("button")) b.disabled = on;
  }

  async function submit(dir) {
    if (busy) return;
    const problem = openPathProblem(dir);
    if (problem) {
      showError(problem);
      input.focus();
      return;
    }
    err.hidden = true;
    setBusy(true);
    let res;
    try {
      res = await send(buildOpenProjectRequest(dir));
    } catch (e) {
      setBusy(false);
      showError(e instanceof ApiError || e instanceof Error ? e.message : "Could not open the project");
      return;
    }
    close();
    onOpened?.(res);
  }

  function renderList() {
    clear(list);
    if (recent === null) {
      list.append(h("p", { class: "muted open-note" }, "Loading recent projects…"));
      return;
    }
    const shown = filterRecentProjects(recent, input.value);
    list.hidden = shown.length === 0;
    for (const d of shown) {
      list.append(
        h(
          "button",
          { class: "open-dir", type: "button", role: "listitem", title: d.dir, disabled: busy, "aria-label": `Open ${d.label || d.dir}`, onclick: () => submit(d.dir) },
          d.label || d.dir,
        ),
      );
    }
  }

  const overlay = h(
    "div",
    { class: "overlay", onclick: (e) => e.target === overlay && !busy && close() },
    h(
      "div",
      { class: "sheet open-sheet", role: "dialog", "aria-modal": "true", "aria-label": "Open project" },
      h("h2", {}, "Open project"),
      h("p", { class: "muted open-note" }, "Starts the project's agentctl session on the laptop."),
      input,
      err,
      list,
      h("div", { class: "row" }, cancelBtn, openBtn),
    ),
  );
  document.body.append(overlay);
  renderList();

  get(RECENT_PROJECTS_PATH)
    .then((body) => {
      recent = body?.dirs ?? [];
    })
    .catch(() => {
      recent = [];
    })
    .finally(renderList);
}
