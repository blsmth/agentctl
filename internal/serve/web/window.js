// Window detail: header, pane (reflowed or exact), state-aware actions.
import { ApiError, get, send, uploadImages } from "./api.js";
import { approvalCard, tickCountdowns } from "./approvals.js";
import { imagePicker, svgIcon } from "./attach.js";
import {
  INTERRUPT_FOLLOWUP_MS,
  actionsWithQueue,
  approvalsForWindow,
  buildAnswerRequest,
  checkboxChoices,
  buildQueueRequest,
  buildReplyRequest,
  buildUploadPath,
  dialogQuestion,
  effectiveState,
  exactRows,
  focusedOption,
  hangingIndent,
  headerChips,
  liveness,
  paneLinesForExact,
  paneModeOrDefault,
  safeHttpsUrl,
  scheduledLine,
  splitPane,
  stateLabel,
  headerLines,
  terminalHash,
  windowApiPath,
} from "./logic.js";
import { clear, confirmSheet, fitTermFont, h, toast } from "./ui.js";

const PANE_REFRESH_MS = 2000;
const MODE_KEY = "agentctl.paneMode";
const STATUS_KEY = "agentctl.statusBarOpen";
const EXACT_HINT_KEY = "agentctl.exactHintSeen";
const EXACT_MIN_FONT = 8;
const EXACT_MAX_FONT = 14;
// The live feed counts as down (for the liveness chip) after this long.
const FEED_DOWN_MS = 15000;

const SEND_SVG =
  '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 19V5M5 12l7-7 7 7"/></svg>';
const CLOSE_SVG =
  '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';

function loadFlag(key) {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

function saveFlag(key, on) {
  try {
    localStorage.setItem(key, on ? "1" : "0");
  } catch {
    // storage unavailable (private mode): the choice just is not remembered
  }
}

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

/** paneLine renders one reflowed line with a hanging indent for wrapped continuations. */
function paneLine(runs) {
  const line = document.createElement("div");
  line.className = "pane-line";
  if (runs.length === 0) {
    line.textContent = " ";
    return line;
  }
  const indent = hangingIndent(runs.map((r) => r.t).join(""));
  if (indent !== 2) {
    line.style.paddingLeft = `${indent}ch`;
    line.style.textIndent = `-${indent}ch`;
  }
  for (const r of runs) line.append(styleSpan(r));
  return line;
}

/**
 * reflowView renders the pane as wrapping HTML. When output changes while
 * the reader is scrolled up, a "New output" pill offers the way back down.
 */
function reflowView() {
  const scroller = h("div", { class: "pane-reflow" });
  const pill = h("button", { type: "button", class: "new-output", hidden: true }, "New output ↓");
  const el = h("div", { class: "pane-reflow-wrap" }, scroller, pill);
  const atBottom = () => scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 24;
  scroller.addEventListener("scroll", () => {
    if (atBottom()) pill.hidden = true;
  });
  pill.addEventListener("click", () => {
    scroller.scrollTop = scroller.scrollHeight;
    pill.hidden = true;
  });

  let statusOpen = loadFlag(STATUS_KEY);
  let statusLines = null;
  let rendered = false;
  // statusBar is Claude Code's chrome below the prompt box: one muted row,
  // tap to show the raw lines.
  function statusBar() {
    const wrap = document.createElement("div");
    wrap.className = "pane-status";
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "pane-status-toggle";
    toggle.setAttribute("aria-expanded", String(statusOpen));
    toggle.textContent = statusOpen ? "Claude Code status ▾" : "Claude Code status ▸";
    toggle.addEventListener("click", () => {
      statusOpen = !statusOpen;
      saveFlag(STATUS_KEY, statusOpen);
      wrap.replaceWith(statusBar());
    });
    wrap.append(toggle);
    if (statusOpen) {
      const pre = document.createElement("pre");
      pre.className = "pane-status-raw";
      pre.textContent = statusLines.join("\n");
      wrap.append(pre);
    }
    return wrap;
  }
  return {
    el,
    update(pane) {
      const follow = atBottom();
      const frag = document.createDocumentFragment();
      const split = splitPane(pane.text);
      statusLines = split.status ? split.status.lines : null;
      for (const b of split.body) {
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
          frag.append(paneLine(b.runs));
        }
      }
      if (split.body.length === 0 && split.prompt?.empty) {
        frag.append(h("div", { class: "pane-empty" }, "At an empty prompt."));
      }
      if (statusLines) frag.append(statusBar());
      scroller.replaceChildren(frag);
      if (follow) scroller.scrollTop = scroller.scrollHeight;
      else if (rendered) pill.hidden = false;
      rendered = true;
    },
    destroy() {},
  };
}

/**
 * exactView is the read-only xterm at the window's real width. The font is
 * scaled so the full width fits (down to EXACT_MIN_FONT, then the view pans
 * sideways); cols are never changed by fitting. Claude Code's footer is
 * dropped and rows are cut to what fits, so no blank band is left.
 */
function exactView() {
  const host = h("div", { class: "pane-exact-host" });
  const el = h("div", { class: "pane-exact" }, host);
  const term = new window.Terminal({
    disableStdin: true,
    convertEol: true,
    fontSize: 12,
    scrollback: 0,
    cursorBlink: false,
    cursorStyle: "underline",
    cursorInactiveStyle: "none",
  });
  term.open(host);

  const cellHeight = () => {
    const screen = host.querySelector(".xterm-screen");
    const hgt = screen ? screen.getBoundingClientRect().height : 0;
    return hgt > 0 && term.rows > 0 ? hgt / term.rows : 0;
  };

  function draw(lines, cols, rows, then) {
    if (term.cols !== cols || term.rows !== rows) term.resize(cols, rows);
    term.write(`\x1b[2J\x1b[3J\x1b[H${lines.join("\n")}`, then);
  }

  return {
    el,
    /** setBackground matches the terminal to the pane it sits in. */
    setBackground(color) {
      if (color) term.options.theme = { ...term.options.theme, background: color };
    },
    update(pane) {
      const lines = paneLinesForExact(pane.text);
      const cols = pane.cols > 0 ? pane.cols : 80;
      const paneRows = pane.rows > 0 ? pane.rows : 24;
      const rowsFor = () => exactRows(lines.length, paneRows, el.clientHeight - 4, cellHeight());
      draw(lines, cols, rowsFor(), () => {
        fitTermFont(term, host, el.clientWidth - 4, EXACT_MIN_FONT, EXACT_MAX_FONT);
        // The font (and so the cell height) may have changed: one more pass.
        const rows = rowsFor();
        if (rows !== term.rows) draw(lines, cols, rows);
      });
    },
    destroy() {
      term.dispose();
    },
  };
}

export function mountWindow(root, ctx, { session, windowId }) {
  const base = windowApiPath(session, windowId);
  const canWrite = ctx.hasFeature("write");
  const canQueue = canWrite && ctx.hasFeature("queue");
  const canUpload = canWrite && ctx.hasFeature("upload");
  let win = null;
  let pane = null;
  let lastText = null;
  let lastActionsKey = null;
  let gone = false;
  // Liveness: when the pane last changed, and since when fetches fail.
  let lastChangeAt = 0;
  let fetchFailingSince = 0;
  // Set by a successful interrupt; the reply box that follows gets focus.
  let interruptedAt = 0;

  const title = h("h1", {}, windowId);
  const statePill = h("span", { class: "state-pill" }, h("span", { class: "dot" }), h("span", { class: "state-text" }));
  const where = h("span", { class: "win-where" });
  const task = h("div", { class: "win-task", hidden: true });
  const scheduled = h("div", { class: "win-scheduled", hidden: true });
  // Tap the recap to show it in full; it is clamped to two lines.
  const recap = h("div", { class: "win-recap", hidden: true, onclick: () => recap.classList.toggle("open") });
  const chips = h("div", { class: "chips", hidden: true });
  const head = h("div", { class: "win-head" }, h("div", { class: "win-state" }, statePill, where), task, recap, scheduled, chips);

  const liveChip = h("span", { class: "live-chip", role: "status" });
  const modeBtns = {
    reflow: h("button", { class: "seg", onclick: () => setMode("reflow") }, "Reflow"),
    exact: h("button", { class: "seg", onclick: () => setMode("exact") }, "Exact"),
  };
  const liveSeg = ctx.hasFeature("terminal")
    ? h("a", { class: "seg", href: terminalHash(session, windowId) }, "Live")
    : null;
  const paneBar = h("div", { class: "pane-bar" }, liveChip, h("div", { class: "segmented" }, modeBtns.reflow, modeBtns.exact, liveSeg));
  const exactHint = h(
    "div",
    { class: "exact-hint", hidden: true },
    h("p", {}, "Exact shows the terminal at its real width. Pan sideways for long lines."),
    h(
      "button",
      {
        type: "button",
        class: "btn-text",
        onclick: () => {
          saveFlag(EXACT_HINT_KEY, true);
          exactHint.hidden = true;
        },
      },
      "Got it",
    ),
  );
  const paneHost = h("div", { class: "pane" });
  const answerHost = h("div", { class: "answer-host" });
  const actions = h("div", { class: "actions" });
  const goneHost = h(
    "div",
    { class: "gone", hidden: true },
    h("p", { class: "muted" }, "This window was closed."),
    h("a", { class: "btn", href: "#/" }, "Back to Fleet"),
  );
  const closeBtn = h(
    "button",
    {
      type: "button",
      class: "icon-btn close-window",
      "aria-label": "Close window",
      hidden: !canWrite,
      onclick: () =>
        run(
          { title: "Close window?", lines: [`${win?.agent ?? ""} ${windowId}`], confirmLabel: "Close", danger: true },
          { method: "POST", path: `${base}/close`, body: {} },
        ),
    },
    svgIcon(CLOSE_SVG),
  );
  const approvalHost = h("div", { class: "window-approvals" });
  let approvalCountdowns = [];
  let approvalKey = null;
  root.append(
    h(
      "div",
      { class: "window-view" },
      h("header", { class: "bar" }, h("a", { class: "back-link", href: "#/" }, "‹ Fleet"), title, closeBtn),
      head,
      approvalHost,
      answerHost,
      paneBar,
      exactHint,
      paneHost,
      goneHost,
      actions,
    ),
  );

  let mode = loadMode();
  let view = null;

  function mountView() {
    view?.destroy();
    clear(paneHost);
    view = mode === "exact" ? exactView() : reflowView();
    paneHost.append(view.el);
    view.setBackground?.(getComputedStyle(paneHost).backgroundColor);
    for (const [m, btn] of Object.entries(modeBtns)) btn.setAttribute("aria-pressed", String(m === mode));
    exactHint.hidden = gone || mode !== "exact" || loadFlag(EXACT_HINT_KEY);
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
    const st = effectiveState(win);
    statePill.className = `state-pill ${st}`;
    statePill.querySelector(".state-text").textContent = stateLabel(win, Date.now());
    where.textContent = [win.project, win.branch].filter(Boolean).join(" · ");
    const { task: line, recap: summary } = headerLines(win);
    task.textContent = line;
    task.hidden = !line;
    recap.textContent = summary;
    recap.hidden = !summary;
    const sched = scheduledLine(win);
    scheduled.textContent = sched;
    scheduled.hidden = !sched;
    clear(chips);
    for (const c of headerChips(win, safeHttpsUrl)) {
      chips.append(
        c.href
          ? h("a", { class: `chip ${c.kind}`, href: c.href, target: "_blank", rel: "noopener noreferrer" }, c.text)
          : h("span", { class: `chip ${c.kind}` }, c.text),
      );
    }
    chips.hidden = chips.childElementCount === 0;
  }

  function offlineSince() {
    const down = ctx.store.downSince;
    const feedDown = down && Date.now() - down > FEED_DOWN_MS ? down : 0;
    if (fetchFailingSince && feedDown) return Math.min(fetchFailingSince, feedDown);
    return fetchFailingSince || feedDown;
  }

  function renderLiveness() {
    if (gone) {
      liveChip.textContent = "";
      return;
    }
    const l = liveness({ lastChangeAt, offlineSince: offlineSince() }, Date.now());
    liveChip.textContent = l.text;
    liveChip.className = `live-chip ${l.kind}`;
  }

  function showGone() {
    title.textContent = "Window closed";
    clear(actions);
    clear(answerHost);
    lastActionsKey = null;
    closeBtn.hidden = true;
    paneBar.hidden = true;
    exactHint.hidden = true;
    goneHost.hidden = false;
  }

  // run confirms (skipped when confirm is null), sends, and on failure toasts; it always refreshes (a 409
  // means the view was stale). onOk runs before that refresh. Returns
  // whether the request succeeded.
  async function run(confirm, req, onOk) {
    if (confirm && !(await confirmSheet(confirm))) return false;
    let ok = false;
    try {
      // req may be a function, for requests that first upload attachments.
      await send(typeof req === "function" ? await req() : req);
      ok = true;
      toast("Sent");
      onOk?.();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : String(e), "error");
    }
    await refresh();
    return ok;
  }

  // Local tick state for a multi-select question, seeded from the pane's
  // [x] marks and kept across polls while the same dialog (hash) is up.
  let multiSel = { hash: "", keys: new Set() };

  // True while an answer request is in flight: further taps are ignored and
  // the choice buttons are disabled, so one tap cannot send two keys.
  let answering = false;
  async function sendAnswer(confirm, req) {
    if (answering) return;
    answering = true;
    const btns = [...document.querySelectorAll(".answer button.choice")];
    if (!confirm) btns.forEach((b) => (b.disabled = true));
    try {
      await run(confirm, req);
    } finally {
      answering = false;
      btns.forEach((b) => (b.disabled = false));
    }
  }

  function multiAnswer(dialog, answer) {
    const choices = checkboxChoices(dialog);
    if (multiSel.hash !== dialog.hash) {
      multiSel = { hash: dialog.hash, keys: new Set(choices.filter((c) => c.checked).map((c) => c.key)) };
    }
    const submit = () => {
      let req;
      try {
        req = buildAnswerRequest(session, windowId, dialog, "submit", [...multiSel.keys]);
      } catch (e) {
        toast(e.message, "error");
        return;
      }
      sendAnswer(null, req);
    };
    return [
      h(
        "div",
        { class: "choices" },
        ...choices.map((c) => {
          const box = h("input", { type: "checkbox" });
          box.checked = multiSel.keys.has(c.key);
          box.onchange = () => (box.checked ? multiSel.keys.add(c.key) : multiSel.keys.delete(c.key));
          return h("label", { class: "btn choice" }, box, ` ${c.label}`);
        }),
        dialog.submit
          ? h("button", { class: "btn choice primary", onclick: submit }, dialog.submit.label || "Submit")
          : h("p", { class: "note" }, "Cannot find the submit row; finish this question on the terminal."),
        h("button", { class: "btn choice", onclick: () => answer("esc") }, "Dismiss (Esc)"),
      ),
    ];
  }

  function answerSection() {
    const dialog = pane.dialog;
    const recommended = focusedOption(pane.text, dialog) || dialog.options?.[0]?.key || "";
    const answer = (key) => {
      let req;
      try {
        req = buildAnswerRequest(session, windowId, dialog, key);
      } catch (e) {
        toast(e.message, "error");
        return;
      }
      // Dismiss cancels a whole AskUserQuestion set, so it still confirms.
      sendAnswer(
        key === "esc" ? { title: "Dismiss the dialog (Esc)?", lines: [dialog.text], confirmLabel: "Send" } : null,
        req,
      );
    };
    if (dialog.multi) {
      return h("section", { class: "answer" }, h("p", { class: "question" }, dialogQuestion(dialog)), ...multiAnswer(dialog, answer));
    }
    return h(
      "section",
      { class: "answer" },
      h("p", { class: "question" }, dialogQuestion(dialog)),
      h(
        "div",
        { class: "choices" },
        ...(dialog.options ?? []).map((o) =>
          h(
            "button",
            { class: `btn choice${o.key === recommended ? " primary" : ""}`, onclick: () => answer(o.key) },
            `${o.key}. ${o.label}`,
          ),
        ),
        h("button", { class: "btn choice", onclick: () => answer("esc") }, "Dismiss (Esc)"),
      ),
    );
  }

  // attachments is the composer's image picker (attach.js); null when the
  // server cannot take uploads.
  function attachments({ icon = false } = {}) {
    if (!canUpload) return null;
    const picker = imagePicker({ icon });
    return {
      controls: picker.controls,
      trigger: picker.trigger,
      count: picker.count,
      // upload stores the files and returns their IDs.
      upload: () => (picker.count() ? uploadImages(buildUploadPath(session, windowId), picker.files()) : Promise.resolve([])),
      clear: picker.clear,
    };
  }

  function imageLine(n) {
    return n ? [`+ ${n} image${n === 1 ? "" : "s"}`] : [];
  }

  // replyBox is the chat-style composer: thumbnails above one row of camera
  // button, auto-growing textarea and a round send button.
  function replyBox() {
    const att = attachments({ icon: true });
    const text = h("textarea", {
      rows: "1",
      class: "composer-text",
      placeholder: "Reply to the agent…",
      maxlength: "4000",
      oninput: () => grow(text),
    });
    const send = h(
      "button",
      {
        type: "button",
        class: "icon-btn send",
        "aria-label": "Send reply",
        onclick: () => {
          const value = text.value.trim();
          const n = att ? att.count() : 0;
          if (!value && !n) return;
          run(
            { title: "Send reply?", lines: [...(value ? [value] : []), ...imageLine(n)], confirmLabel: "Send" },
            async () => buildReplyRequest(session, windowId, value, att ? await att.upload() : []),
            () => att?.clear(),
          );
        },
      },
      svgIcon(SEND_SVG),
    );
    const row = h("div", { class: "composer" }, ...(att ? att.trigger : []), text, send);
    return { text, nodes: [...(att ? att.controls : []), row] };
  }

  function grow(el) {
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }

  function interruptButton() {
    return h(
      "button",
      {
        class: "btn small danger",
        onclick: () =>
          run(
            { title: "Interrupt agent?", lines: ["Sends Escape to the running agent."], confirmLabel: "Interrupt", danger: true },
            { method: "POST", path: `${base}/interrupt`, body: {} },
            () => {
              interruptedAt = Date.now();
            },
          ),
      },
      "Interrupt",
    );
  }

  function queueBox() {
    // One line until there is more to show: the pane keeps the height.
    const text = h("textarea", {
      rows: "1",
      class: "compact",
      placeholder: "Queue a message for the agent…",
      maxlength: "4000",
      oninput: () => {
        text.style.height = "auto";
        text.style.height = `${text.scrollHeight + 2}px`;
      },
    });
    const att = attachments();
    return [
      text,
      ...(att ? att.controls : []),
      h(
        "div",
        { class: "row" },
        h(
          "button",
          {
            class: "btn",
            onclick: () => {
              const value = text.value.trim();
              const n = att ? att.count() : 0;
              if (!value && !n) return;
              run(
                { title: "Queue message?", lines: [...(value ? [value] : []), ...imageLine(n), "Delivered after the current step."], confirmLabel: "Queue" },
                async () => buildQueueRequest(session, windowId, value, att ? await att.upload() : []),
                () => {
                  text.value = "";
                  text.style.height = "";
                  att?.clear();
                },
              );
            },
          },
          "Queue message",
        ),
        interruptButton(),
      ),
    ];
  }

  function renderActions() {
    if (!win || gone) return;
    if (!canWrite) {
      clear(actions);
      actions.append(h("p", { class: "muted" }, "Read-only: write actions are not enabled on this server."));
      return;
    }
    const kind = actionsWithQueue(win, pane, canQueue);
    // Do not rebuild while the user may be typing: only when the kind or dialog changes.
    const key = `${kind}:${pane?.dialog?.hash ?? ""}`;
    if (key === lastActionsKey) return;
    lastActionsKey = key;
    clear(actions);
    clear(answerHost);
    if (kind === "answer") answerHost.append(answerSection());
    else if (kind === "reply") {
      const { text, nodes } = replyBox();
      actions.append(...nodes);
      if (interruptedAt && Date.now() - interruptedAt < INTERRUPT_FOLLOWUP_MS) {
        text.focus();
        toast("Interrupted. Tell it what to do next.");
      }
    } else if (kind === "queue") actions.append(...queueBox());
    else if (kind === "interrupt") actions.append(h("div", { class: "row end" }, interruptButton()));
    if (kind !== "interrupt" && kind !== "queue") interruptedAt = 0;
  }

  // fetchFailed records a failed fetch: 404 means the window is gone, anything
  // else (network, 5xx) that the laptop cannot be reached right now.
  function fetchFailed(e) {
    if (e instanceof ApiError && e.status === 404) gone = true;
    else if (!fetchFailingSince) fetchFailingSince = Date.now();
  }

  async function refreshPane() {
    try {
      pane = await get(`${base}/pane?lines=200`);
    } catch (e) {
      fetchFailed(e);
      return false;
    }
    const key = `${pane.cols}x${pane.rows}\n${pane.text}`;
    if (key !== lastText) {
      if (lastText !== null) lastChangeAt = Date.now();
      lastText = key;
      view.update(pane);
    }
    return true;
  }

  async function refreshWindow() {
    try {
      win = await get(base);
    } catch (e) {
      fetchFailed(e);
      return false;
    }
    // Until the pane is seen to change, the window's last recorded activity
    // is the best guess at when it last did.
    const recorded = Date.parse(win.detail?.last_activity ?? "");
    if (!Number.isNaN(recorded) && recorded > lastChangeAt) lastChangeAt = Math.min(recorded, Date.now());
    return true;
  }

  async function refresh() {
    if (gone) return;
    const [okWin, okPane] = await Promise.all([refreshWindow(), refreshPane()]);
    if (gone) {
      showGone();
      return;
    }
    if (okWin && okPane) fetchFailingSince = 0;
    renderHeader();
    renderActions();
    renderLiveness();
  }

  // The window's own pending approval, shown in full above the pane.
  function renderApprovals() {
    const mine = approvalsForWindow(ctx.store.approvals, session, windowId);
    const key = mine.map((a) => a.id).join(",");
    if (key === approvalKey) return;
    approvalKey = key;
    clear(approvalHost);
    approvalCountdowns = [];
    for (const a of mine) approvalHost.append(approvalCard(ctx, a, approvalCountdowns));
  }
  renderApprovals();
  // One 1s tick: approval countdowns, the liveness chip and the state age.
  const tick = setInterval(() => {
    tickCountdowns(approvalCountdowns);
    if (document.hidden || gone) return;
    renderLiveness();
    if (win) statePill.querySelector(".state-text").textContent = stateLabel(win, Date.now());
  }, 1000);

  refresh();
  const timer = setInterval(() => {
    if (!document.hidden && !gone) refresh();
  }, PANE_REFRESH_MS);
  const onVisible = () => !document.hidden && refresh();
  document.addEventListener("visibilitychange", onVisible);
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "window" && ev.win.session === session && ev.win.window_id === windowId) refresh();
    if (ev?.type === "gone") refresh();
    if (ev?.type === "status") {
      renderLiveness();
      // Back online: fetch at once rather than on the next poll.
      if (!ctx.store.downSince) refresh();
    }
    if (ev?.type === "approvals") renderApprovals();
  });

  return () => {
    clearInterval(timer);
    clearInterval(tick);
    unsub();
    document.removeEventListener("visibilitychange", onVisible);
    view.destroy();
  };
}
