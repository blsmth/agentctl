// Fleet's "Recently closed" section: windows closed in the last 7 days (GET
// /v1/closed), the hub's Closed section on the phone. Collapsed by default;
// tapping a resumable row confirms in a sheet, resumes through POST
// /v1/closed/{handle}/resume and opens the new window.
import {
  CLOSED_GLYPH,
  CLOSED_PATH,
  buildResumeRequest,
  canResume,
  closedHeader,
  closedMeta,
  closedNote,
  closedPr,
  closedResumeConfirm,
  closedRowHref,
  closedTitle,
  resumeTarget,
} from "./logic.js";
import { get, send, ApiError } from "./api.js";
import { clear, confirmSheet, h, toast } from "./ui.js";

const OPEN_KEY = "agentctl.fleet.closedOpen";

function loadOpen() {
  try {
    return localStorage.getItem(OPEN_KEY) === "1";
  } catch {
    return false;
  }
}

function saveOpen(open) {
  try {
    if (open) localStorage.setItem(OPEN_KEY, "1");
    else localStorage.removeItem(OPEN_KEY);
  } catch {
    // private mode: the section just starts collapsed next time
  }
}

/**
 * mountClosed builds the section. The returned node is kept across fleet
 * renders (the fleet re-appends it); sync(sig) re-reads the ledger when the
 * set of live windows changed, which is when something may have closed.
 */
export function mountClosed(ctx) {
  const section = h("section", { class: "closed-group folded" });
  let items = null;
  let error = "";
  let open = loadOpen();
  let lastSig = null;
  let loading = false;
  let again = false;
  const resuming = new Set();

  async function load() {
    if (loading) {
      again = true;
      return;
    }
    loading = true;
    try {
      const body = await get(CLOSED_PATH);
      items = body?.closed ?? [];
      error = body?.error ?? "";
    } catch (err) {
      error = err instanceof ApiError || err instanceof Error ? err.message : "Could not load closed agents";
      items = items ?? [];
    } finally {
      loading = false;
    }
    render();
    if (again) {
      again = false;
      load();
    }
  }

  async function resume(it) {
    if (resuming.has(it.handle)) return;
    if (!(await confirmSheet(closedResumeConfirm(it)))) return;
    resuming.add(it.handle);
    render();
    try {
      const res = await send(buildResumeRequest(it.handle));
      toast(`Resumed ${closedTitle(it)}`);
      location.hash = resumeTarget(res);
    } catch (err) {
      toast(err instanceof ApiError || err instanceof Error ? err.message : "Resume failed", "error");
      load();
    } finally {
      resuming.delete(it.handle);
      render();
    }
  }

  function row(it) {
    const href = closedRowHref(it);
    const resumable = !href && canResume(it, ctx.features);
    const busy = resuming.has(it.handle);
    const note = busy ? "resuming…" : closedNote(it);
    const body = [
      h("span", { class: "glyph", "aria-hidden": "true" }, CLOSED_GLYPH),
      h(
        "span",
        { class: "win-main" },
        h("span", { class: "win-title" }, h("span", { class: "agent" }, closedTitle(it))),
        h("span", { class: "activity" }, closedMeta(it)),
        note ? h("span", { class: "closed-note" }, note) : null,
      ),
    ];
    let main;
    if (href) main = h("a", { class: "closed-main", href }, ...body);
    else if (resumable) {
      main = h(
        "button",
        { class: "closed-main", type: "button", disabled: busy, "aria-label": `Resume ${closedTitle(it)}`, onclick: () => resume(it) },
        ...body,
      );
    } else main = h("div", { class: "closed-main" }, ...body);
    const pr = closedPr(it);
    return h(
      "div",
      { class: `win closed-row${resumable ? " resumable" : ""}${href ? " live" : ""}`, "data-handle": it.handle || null },
      main,
      pr ? h("a", { class: "closed-pr", href: pr.href, target: "_blank", rel: "noopener noreferrer" }, pr.text) : null,
    );
  }

  function render() {
    clear(section);
    section.classList.toggle("folded", !open);
    const n = items?.length ?? 0;
    const toggle = () => {
      open = !open;
      saveOpen(open);
      render();
      if (open) load();
    };
    section.append(
      h(
        "div",
        { class: "group-head" },
        h("h2", {}, h("button", { class: "group-toggle", type: "button", "aria-expanded": String(open), onclick: toggle }, closedHeader(n))),
      ),
    );
    if (!open) return;
    if (error) section.append(h("p", { class: "muted pad closed-error", role: "status" }, error));
    if (items === null) section.append(h("p", { class: "muted pad" }, "Loading…"));
    else if (n === 0 && !error) section.append(h("p", { class: "muted pad" }, "None in the last 7 days."));
    else for (const it of items) section.append(row(it));
  }

  render();
  return {
    node: section,
    /** sync re-reads the ledger when sig (the live window keys) changed. */
    sync(sig) {
      if (sig === lastSig) return;
      lastSig = sig;
      load();
    },
  };
}
