// Artifacts tab: the list of agent-published artifacts and the viewer. The
// content is untrusted: it only ever loads inside a sandboxed iframe without
// allow-same-origin (an opaque origin, no cookie, no reach into /v1), from the
// raw_url the server gives: the direct /a/<id>/<n>/ for an artifact whose id
// is itself the capability, or a token URL minted per device (valid ~24h) for
// a legacy 10-character id.
import { ApiError, api, get } from "./api.js";
import {
  ARTIFACT_FEEDBACK_MAX,
  artifactCommentStatus,
  artifactCommentToast,
  artifactFeedbackFrom,
  artifactHash,
  artifactIcon,
  artifactOrigin,
  artifactRevLabel,
  artifactRevPill,
  artifactRowMeta,
  artifactTokenStale,
  artifactWindowLive,
  artifactsForWindow,
  artifactsHash,
  groupArtifacts,
  formatAgo,
  windowHash,
} from "./logic.js";
import { clear, confirmSheet, h, toast } from "./ui.js";

const POLL_MS = 30000;
// Burst of store events (an add is several fs events): one refetch per burst.
const REFRESH_DEBOUNCE_MS = 300;
const SANDBOX = "allow-scripts allow-forms allow-popups allow-modals allow-downloads";
const enc = encodeURIComponent;

export function mountArtifacts(root, ctx, route) {
  if (route.view === "viewer") return mountViewer(root, ctx, route);
  return mountList(root, ctx, route.filter);
}

const errText = (e) => (e instanceof Error ? e.message : String(e));

/** debounced returns fn wrapped so bursts collapse into one trailing call; .cancel() drops a pending one. */
function debounced(fn, ms) {
  let t = null;
  const wrapped = () => {
    clearTimeout(t);
    t = setTimeout(fn, ms);
  };
  wrapped.cancel = () => clearTimeout(t);
  return wrapped;
}

// ---- list ----

function mountList(root, ctx, filter) {
  const status = h("p", { class: "muted pad", hidden: true });
  const list = h("div", { class: "artifact-list", "aria-live": "polite" });
  const filterBar = h("div", { class: "artifact-filter", hidden: !filter });
  root.append(
    h("header", { class: "bar" }, h("h1", {}, "Artifacts")),
    filterBar,
    status,
    list,
  );
  let disposed = false;
  let data = null;

  function row(a) {
    return h(
      "a",
      { class: "win artifact-row", href: artifactHash(a.id), "data-id": a.id },
      h("span", { class: `glyph artifact-icon kind-${a.kind}`, "aria-label": a.kind }, artifactIcon(a.kind)),
      h(
        "span",
        { class: "win-main" },
        h("span", { class: "win-title" }, h("span", { class: "agent" }, a.title)),
        a.description ? h("span", { class: "activity" }, a.description) : null,
        a.agent ? h("span", { class: "win-chips" }, h("span", { class: "tag agent-chip" }, a.agent)) : null,
      ),
      h(
        "span",
        { class: "win-side" },
        h("span", {}, artifactRowMeta(a)),
      ),
    );
  }

  function render() {
    if (disposed || !data) return;
    const shown = filter ? artifactsForWindow(data, filter.session, filter.windowId) : data;
    clear(filterBar);
    if (filter) {
      filterBar.append(
        h("span", { class: "muted" }, `From window ${filter.windowId}`),
        h("a", { class: "btn small", href: artifactsHash() }, "Show all"),
      );
    }
    clear(list);
    status.hidden = true;
    if (shown.length === 0) {
      list.append(
        h(
          "div",
          { class: "empty artifact-empty" },
          h("p", {}, filter ? "No artifacts from this window." : "No artifacts yet."),
          h("p", { class: "muted" }, "An agent publishes one with:"),
          h("pre", { class: "sheet-body" }, "agentctl artifact add report.html --title 'Weekly report'"),
          h("p", { class: "muted" }, "Reports, tables, charts and mockups show up here, and you get a push when one arrives."),
        ),
      );
      return;
    }
    for (const g of groupArtifacts(shown, ctx.store.fleet?.groups)) {
      list.append(h("section", { class: "group" }, h("h2", {}, g.name), h("div", { class: "nodes" }, ...g.items.map((a) => h("div", { class: "node" }, row(a))))));
    }
  }

  async function load() {
    try {
      data = (await get("/v1/artifacts")).artifacts ?? [];
      if (!disposed) render();
    } catch (e) {
      if (disposed) return;
      if (!data) {
        status.textContent = `Could not load artifacts: ${errText(e)}`;
        status.hidden = false;
      }
    }
  }

  const refresh = debounced(load, REFRESH_DEBOUNCE_MS);
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "artifact") refresh();
    // The fleet feed also carries the saved group order, and a reconnect
    // may have dropped artifact events.
    if (ev?.type === "fleet") render();
    if (ev?.type === "status" && !ctx.store.downSince) refresh();
  });
  const timer = setInterval(() => !document.hidden && load(), POLL_MS);
  load();

  return () => {
    disposed = true;
    refresh.cancel();
    clearInterval(timer);
    unsub();
  };
}

// ---- viewer ----

function mountViewer(root, ctx, route) {
  const write = ctx.hasFeature("write");
  const canComment = write && ctx.hasFeature("artifact-comments");
  const id = route.id;
  let detail = null;
  let fetchedAt = 0;
  let disposed = false;
  let frameSrc = "";

  const title = h("h1", {}, "");
  const revSlot = h("span", { class: "artifact-revslot" });
  const more = h("button", { class: "btn icon", type: "button", "aria-label": "More actions", hidden: true, onclick: () => moreSheet() }, "⋯");
  const commentBtn = h("button", { class: "btn icon", type: "button", "aria-label": "Send feedback to the agent", hidden: true, onclick: () => composer("") }, "\u{1F4AC}");
  const frame = h("iframe", {
    class: "artifact-frame",
    sandbox: SANDBOX,
    title: "Artifact",
    referrerpolicy: "no-referrer",
    hidden: true,
  });
  const message = h("div", { class: "artifact-message", hidden: true });
  const view = h(
    "div",
    { class: "artifact-view" },
    h("header", { class: "bar artifact-bar" }, h("a", { class: "btn icon", href: "#/artifacts", "aria-label": "Back to artifacts" }, "‹"), title, revSlot, commentBtn, more),
    message,
    frame,
  );
  root.append(view);

  const revNumber = () => {
    if (!detail) return 0;
    return route.rev && detail.revs.some((r) => r.n === route.rev) ? route.rev : detail.latest;
  };
  const currentUrl = () => {
    const r = detail?.revs.find((x) => x.n === revNumber());
    return r?.raw_url || detail?.raw_url || "";
  };

  function showMessage(text, backLabel = "Back to artifacts") {
    more.hidden = true;
    commentBtn.hidden = true;
    clear(revSlot);
    frame.hidden = true;
    frameSrc = "";
    frame.removeAttribute("src");
    clear(message);
    message.append(h("p", {}, text), h("a", { class: "btn", href: "#/artifacts" }, backLabel));
    message.hidden = false;
  }

  function deleted() {
    title.textContent = "Artifact";
    showMessage("This artifact was deleted");
  }

  function setFrame() {
    const url = currentUrl();
    if (!url) return;
    // Assigning the same src would reload the document for nothing.
    if (url !== frameSrc) {
      frameSrc = url;
      frame.src = url;
    }
    frame.hidden = false;
    message.hidden = true;
  }

  async function togglePin() {
    try {
      detail = { ...detail, ...(await api("POST", `/v1/artifacts/${enc(id)}/pin`, { pinned: !detail.pinned })) };
      renderTools();
    } catch (e) {
      toast(`${detail.pinned ? "Unpin" : "Pin"} failed: ${errText(e)}`, "error");
    }
  }

  async function remove() {
    const ok = await confirmSheet({
      title: `Delete ${detail.title}?`,
      lines: ["Removes the artifact and all its revisions from this machine."],
      confirmLabel: "Delete",
      danger: true,
    });
    if (!ok || disposed) return;
    try {
      await api("DELETE", `/v1/artifacts/${enc(id)}`, {});
      toast("Artifact deleted");
      location.hash = "#/artifacts";
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) deleted();
      else toast(`Delete failed: ${errText(e)}`, "error");
    }
  }

  function renderTools() {
    title.textContent = detail.title;
    clear(revSlot);
    if (detail.revs.length > 1) {
      const n = revNumber();
      const sel = h(
        "select",
        {
          class: "artifact-revs",
          "aria-label": "Revision",
          onchange: () => {
            const v = Number(sel.value);
            location.hash = artifactHash(id, v === detail.latest ? null : v);
          },
        },
        ...[...detail.revs].reverse().map((r) => h("option", { value: r.n, selected: r.n === n }, artifactRevLabel(r))),
      );
      revSlot.append(h("span", { class: `artifact-pill${n === detail.latest ? "" : " old"}` }, artifactRevPill(n, detail.latest, detail.revs.length)), sel);
    }
    more.hidden = false;
    commentBtn.hidden = !canComment;
  }

  let composerEl = null;

  /**
   * composer is the feedback bottom sheet: the revision it applies to, a text
   * box and Send. prefill comes from the postMessage bridge: it only fills the
   * box; nothing is sent until Brendan taps Send. An open composer keeps what
   * he typed and gets the new text on a new line.
   */
  function composer(prefill) {
    if (!detail || !canComment) return;
    if (composerEl) {
      const ta = composerEl.querySelector("textarea");
      if (prefill) ta.value = (ta.value.trim() ? `${ta.value}\n${prefill}` : prefill).slice(0, ARTIFACT_FEEDBACK_MAX);
      ta.focus();
      return;
    }
    const rev = revNumber();
    let sending = false;
    const close = () => {
      removeEventListener("hashchange", close);
      composerEl = null;
      overlay.remove();
    };
    addEventListener("hashchange", close, { once: true });
    const ta = h("textarea", { rows: 5, maxlength: String(ARTIFACT_FEEDBACK_MAX), placeholder: "What should change?", "aria-label": "Feedback" });
    ta.value = prefill;
    const err = h("p", { class: "sheet-error", hidden: true });
    const send = h("button", { class: "btn primary", type: "button", onclick: submit }, "Send");
    async function submit() {
      const text = ta.value.trim();
      if (!text || sending) return;
      sending = true;
      send.disabled = true;
      err.hidden = true;
      try {
        const res = await api("POST", `/v1/artifacts/${enc(id)}/comments`, { text, rev });
        close();
        const [msg, kind] = artifactCommentToast(res);
        toast(msg, kind);
        load();
      } catch (e) {
        sending = false;
        send.disabled = false;
        err.textContent = `Could not send: ${errText(e)}`;
        err.hidden = false;
      }
    }
    const overlay = h(
      "div",
      { class: "overlay", onclick: (e) => e.target === overlay && close() },
      h(
        "div",
        { class: "sheet artifact-sheet", role: "dialog", "aria-modal": "true", "aria-label": "Send feedback" },
        h("h2", {}, "Feedback for the agent"),
        h("p", { class: "artifact-origin" }, `Applies to v${rev}${rev === detail.latest ? "" : " (not the latest)"}`),
        ta,
        err,
        h("div", { class: "row" }, h("button", { class: "btn", type: "button", onclick: close }, "Cancel"), send),
      ),
    );
    composerEl = overlay;
    document.body.append(overlay);
    ta.focus();
  }

  // The bridge: an artifact may propose feedback with
  // parent.postMessage({type: "agentctl:feedback", text}, "*"). Only the
  // iframe's own window counts, and it never sends: it opens the composer
  // prefilled so a person reviews the text and taps Send.
  const onMessage = (e) => {
    const text = artifactFeedbackFrom(e, frame.contentWindow);
    if (text) composer(text);
  };
  addEventListener("message", onMessage);

  /** moreSheet is the ⋯ bottom sheet: origin, open in a new tab, pin and delete. */
  function moreSheet() {
    const close = () => {
      removeEventListener("hashchange", close);
      overlay.remove();
    };
    addEventListener("hashchange", close, { once: true });
    const live = artifactWindowLive(detail, ctx.store.fleet?.groups);
    const origin = artifactOrigin(detail);
    const overlay = h(
      "div",
      { class: "overlay", onclick: (e) => e.target === overlay && close() },
      h(
        "div",
        { class: "sheet artifact-sheet", role: "dialog", "aria-modal": "true", "aria-label": "Artifact actions" },
        h("h2", {}, detail.title),
        origin ? h("p", { class: "artifact-origin" }, origin) : null,
        live ? h("a", { class: "btn", href: windowHash(detail.session, detail.window_id), onclick: close }, "Go to window ›") : null,
        h("a", { class: "btn", href: currentUrl(), target: "_blank", rel: "noopener noreferrer", onclick: close }, "Open in new tab ↗"),
        detail.comments?.length
          ? h(
              "div",
              { class: "artifact-comments" },
              h("h3", {}, `Feedback sent (${detail.comments.length})`),
              ...[...detail.comments].reverse().map((c) =>
                h(
                  "div",
                  { class: "artifact-comment" },
                  h("span", { class: "muted" }, `v${c.rev} \u00B7 ${formatAgo(c.at) || "now"} \u00B7 ${artifactCommentStatus(c)}`),
                  h("p", {}, c.text),
                ),
              ),
            )
          : null,
        write
          ? h("button", { class: "btn", type: "button", onclick: () => { close(); togglePin(); } }, detail.pinned ? "Unpin" : "Pin")
          : null,
        write ? h("button", { class: "btn danger", type: "button", onclick: () => { close(); remove(); } }, "Delete") : null,
        h("button", { class: "btn", type: "button", onclick: close }, "Close"),
      ),
    );
    document.body.append(overlay);
  }

  async function load() {
    try {
      const d = await get(`/v1/artifacts/${enc(id)}`);
      if (disposed) return;
      detail = d;
      fetchedAt = Date.now();
      renderTools();
      setFrame();
    } catch (e) {
      if (disposed) return;
      if (e instanceof ApiError && e.status === 404) deleted();
      else if (!detail) showMessage(`Could not load this artifact: ${errText(e)}`, "Back");
    }
  }

  // Tokens last ~24h; a viewer left open in the background gets a fresh URL
  // when it comes back and the one it has is more than an hour old.
  const onBack = () => {
    if (document.visibilityState !== "visible" || !detail) return;
    if (artifactTokenStale(fetchedAt)) load();
  };
  document.addEventListener("visibilitychange", onBack);
  addEventListener("focus", onBack);

  const refresh = debounced(load, REFRESH_DEBOUNCE_MS);
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "artifact" && (!ev.change?.id || ev.change.id === id)) {
      if (ev.change?.action === "rm") deleted();
      else refresh();
    }
    if (ev?.type === "fleet" && detail) renderTools();
  });

  load();

  return () => {
    disposed = true;
    refresh.cancel();
    unsub();
    removeEventListener("message", onMessage);
    if (composerEl) composerEl.remove();
    document.removeEventListener("visibilitychange", onBack);
    removeEventListener("focus", onBack);
  };
}
