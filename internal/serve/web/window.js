// Window detail: header, read-only xterm pane, state-aware action bar.
import { ApiError, get, send } from "./api.js";
import {
  actionsFor,
  buildAnswerRequest,
  buildReplyRequest,
  effectiveState,
  formatSince,
  normalizePane,
  paneModeOrDefault,
  safeHttpsUrl,
  terminalHash,
  windowApiPath,
} from "./logic.js";
import { clear, confirmSheet, fitTermFont, h, toast } from "./ui.js";

const PANE_REFRESH_MS = 2000;
const MODE_KEY = "agentctl.paneMode";
const EXACT_MIN_FONT = 4;
const EXACT_MAX_FONT = 14;

function loadMode() {
  try {
    return paneModeOrDefault(localStorage.getItem(MODE_KEY));
  } catch {
    return "reflow";
  }
}

function saveMode(mode) {
  try {
    localStorage.setItem(MODE_KEY, mode);
  } catch {
    // storage unavailable (private mode): the choice just is not remembered
  }
}

/** styleSpan builds a span for one styled run; text only via textContent. */
function styleSpan(run) {
  const span = document.createElement("span");
  span.textContent = run.t;
  let fg = run.fg;
  let bg = run.bg;
  if (run.inverse) {
    [fg, bg] = [bg ?? "#000000", fg ?? "#e5e5e5"];
  }
  if (fg) span.style.color = fg;
  if (bg) span.style.backgroundColor = bg;
  if (run.bold) span.style.fontWeight = "700";
  if (run.dim) span.style.opacity = "0.6";
  if (run.italic) span.style.fontStyle = "italic";
  if (run.underline) span.style.textDecoration = "underline";
  return span;
}

/** reflowView renders the pane as wrapping HTML. */
function reflowView() {
  const el = h("div", { class: "pane-reflow" });
  return {
    el,
    update(pane) {
      const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
      const frag = document.createDocumentFragment();
      for (const b of normalizePane(pane.text)) {
        if (b.rule !== undefined) {
          const rule = document.createElement("div");
          rule.className = "pane-rule";
          if (b.rule) {
            const label = document.createElement("span");
            label.textContent = b.rule;
            rule.append(label);
          }
          frag.append(rule);
        } else {
          const line = document.createElement("div");
          line.className = "pane-line";
          if (b.runs.length === 0) line.textContent = "\u00a0";
          else for (const r of b.runs) line.append(styleSpan(r));
          frag.append(line);
        }
      }
      el.replaceChildren(frag);
      if (atBottom) el.scrollTop = el.scrollHeight;
    },
    destroy() {},
  };
}

/**
 * exactView is the read-only xterm at the window's real size. The font is
 * scaled so the full width fits; cols are never changed by fitting.
 */
function exactView() {
  const host = h("div", { class: "pane-exact-host" });
  const el = h("div", { class: "pane-exact" }, host);
  let mode = loadMode();
  let view = null;

  function mountView() {
    view?.destroy();
    clear(paneHost);
    view = mode === "exact" ? exactView() : reflowView();
    paneHost.append(view.el);
    for (const [m, btn] of Object.entries(modeBtns)) btn.setAttribute("aria-pressed", String(m === mode));
    if (pane) view.update(pane);
  }

  function setMode(next) {
    if (next === mode) return;
    mode = next;
    saveMode(mode);
    mountView();
  }
  mountView();

  function renderHeader() {
    if (!win) return;
    title.textContent = win.agent || windowId;
    clear(sub);
    const bits = [win.branch, win.mode, win.context_pct ? `${Math.round(win.context_pct)}% ctx` : "", effectiveState(win)];
    const since = formatSince(win.blocked_since || win.state_since, Date.now());
    if (since) bits.push(since);
    sub.append(bits.filter(Boolean).join(" · "));
    const pr = safeHttpsUrl(win.pr_url);
    if (pr) {
      sub.append(" · ", h("a", { href: pr, target: "_blank", rel: "noopener noreferrer" }, "PR"));
    }
  }

  // run confirms, sends, and on failure toasts and refreshes (409 = stale).
  async function run(confirm, req) {
    if (!(await confirmSheet(confirm))) return false;
    try {
      await send(req);
      toast("Sent");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : String(e), "error");
    }
    await refresh();
    return true;
  }

  function answerButtons() {
    const dialog = pane.dialog;
    const shown = (key) => {
      let req;
      try {
        req = buildAnswerRequest(session, windowId, dialog, key);
      } catch (e) {
        toast(e.message, "error");
        return;
      }
      run(
        {
          title: key === "esc" ? "Send Escape?" : `Answer "${key}"?`,
          lines: [dialog.text],
          confirmLabel: "Send",
        },
        req,
      );
    };
    return [
      h("pre", { class: "dialog" }, dialog.text),
      h(
        "div",
        { class: "choices" },
        ...(dialog.options ?? []).map((o) =>
          h("button", { class: "btn", onclick: () => shown(o.key) }, `${o.key}. ${o.label}`),
        ),
        h("button", { class: "btn", onclick: () => shown("esc") }, "Esc"),
      ),
    ];
  }

  function replyBox() {
    const text = h("textarea", { rows: "3", placeholder: "Reply to the agent…", maxlength: "4000" });
    return [
      text,
      h(
        "div",
        { class: "row" },
        h(
          "button",
          {
            class: "btn primary",
            onclick: () => {
              const value = text.value.trim();
              if (value) run({ title: "Send reply?", lines: [value], confirmLabel: "Send" }, buildReplyRequest(session, windowId, value));
            },
          },
          "Reply",
        ),
        h(
          "button",
          {
            class: "btn danger",
            onclick: () =>
              run(
                { title: "Close window?", lines: [`${win?.agent ?? ""} ${windowId}`], confirmLabel: "Close", danger: true },
                { method: "POST", path: `${base}/close`, body: {} },
              ),
          },
          "Close window",
        ),
      ),
    ];
  }

  function interruptButton() {
    return [
      h(
        "button",
        {
          class: "btn danger",
          onclick: () =>
            run(
              { title: "Interrupt agent?", lines: ["Sends Escape to the running agent."], confirmLabel: "Interrupt", danger: true },
              { method: "POST", path: `${base}/interrupt`, body: {} },
            ),
        },
        "Interrupt",
      ),
    ];
  }

  function renderActions() {
    if (!win) return;
    if (!canWrite) {
      clear(actions);
      actions.append(h("p", { class: "muted" }, "Read-only: write actions are not enabled on this server."));
      return;
    }
    const kind = actionsFor(win, pane);
    // Do not rebuild while the user may be typing a reply: only when the kind or dialog changes.
    const key = `${kind}:${pane?.dialog?.hash ?? ""}`;
    if (key === lastActionsKey) return;
    lastActionsKey = key;
    clear(actions);
    if (kind === "answer") actions.append(...answerButtons());
    else if (kind === "reply") actions.append(...replyBox());
    else if (kind === "interrupt") actions.append(...interruptButton());
  }

  async function refreshPane() {
    try {
      pane = await get(`${base}/pane?lines=200`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) gone = true;
      return;
    }
    const key = `${pane.cols}x${pane.rows}\n${pane.text}`;
    if (key !== lastText) {
      lastText = key;
      view.update(pane);
    }
  }

  async function refreshWindow() {
    try {
      win = await get(base);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        gone = true;
        title.textContent = "Window closed";
      }
    }
  }

  async function refresh() {
    await Promise.all([refreshWindow(), refreshPane()]);
    if (gone) return;
    renderHeader();
    renderActions();
  }

  refresh();
  const timer = setInterval(() => {
    if (!document.hidden && !gone) refresh();
  }, PANE_REFRESH_MS);
  const onVisible = () => !document.hidden && refresh();
  document.addEventListener("visibilitychange", onVisible);
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "window" && ev.win.session === session && ev.win.window_id === windowId) refresh();
  });

  return () => {
    clearInterval(timer);
    unsub();
    document.removeEventListener("visibilitychange", onVisible);
    view.destroy();
  };
}
