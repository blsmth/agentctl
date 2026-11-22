// Approvals screen: dispatch and command approvals waiting on the laptop's
// popup, answerable from the phone. Prompt and command are shown in full, as
// text only. Deny and every non-dispatch answer go through a confirm sheet;
// approving a dispatch sends at once, since the card shows all of it. Every
// answer carries the payload hash of what was shown.
import { ApiError, get, send } from "./api.js";
import {
  approvalConfirm,
  approvalNeedsConfirm,
  approvalApiPath,
  approvalHash,
  approvalRows,
  approvalSummary,
  buildApprovalRequest,
  formatCountdown,
  promptFold,
  resolvedText,
  secondsLeft,
  windowHash,
} from "./logic.js";
import { clear, confirmSheet, h, toast } from "./ui.js";

/**
 * answerApproval confirms (when needed) and sends one answer. On 409 (already answered, or
 * changed) or 404 it toasts and asks the store to refetch.
 */
async function answerApproval(ctx, a, decision, buttons = []) {
  let req;
  try {
    req = buildApprovalRequest(a, decision);
  } catch (e) {
    toast(e.message, "error");
    return;
  }
  if (approvalNeedsConfirm(a, decision) && !(await confirmSheet(approvalConfirm(a, decision)))) return;
  for (const b of buttons) b.disabled = true;
  try {
    await send(req);
    toast(decision === "allow" ? "Approved" : "Denied");
  } catch (e) {
    for (const b of buttons) b.disabled = false;
    if (e instanceof ApiError && (e.status === 409 || e.status === 404)) toast("Already answered or changed", "error");
    else toast(e instanceof ApiError ? e.message : String(e), "error");
  }
  ctx.refreshApprovals();
}

/**
 * detailRows renders the dispatch's resolved where/how block as compact
 * label/value rows (see approvalRows).
 */
function detailRows(a) {
  const rows = approvalRows(a);
  if (!rows.length) return null;
  return h(
    "dl",
    { class: "approval-rows" },
    ...rows.map((r) =>
      h(
        "div",
        { class: r.flag ? "approval-row flagged" : "approval-row", "data-row": r.label.toLowerCase() },
        h("dt", {}, r.label),
        h(
          "dd",
          {},
          h("span", { class: "approval-value" }, r.value),
          r.flag ? h("span", { class: "approval-flag" }, r.flag) : null,
          r.detail ? h("span", { class: "approval-detail" }, r.detail) : null,
        ),
      ),
    ),
  );
}

/**
 * textBlock is a labelled pre block. A long one is folded to a few lines
 * behind a "Show all" toggle, so a clipped prompt is never mistaken for the
 * whole prompt.
 */
function textBlock(label, text) {
  const pre = h("pre", { class: "approval-text" }, text);
  const { fold, label: more } = promptFold(text);
  if (!fold) return [h("div", { class: "meta" }, label), pre];
  pre.classList.add("foldable", "folded");
  const toggle = h("button", { class: "btn approval-more", type: "button", "aria-expanded": "false" }, more);
  toggle.addEventListener("click", () => {
    const open = pre.classList.toggle("folded") === false;
    toggle.setAttribute("aria-expanded", String(open));
    toggle.textContent = open ? "Show less" : more;
  });
  return [h("div", { class: "meta" }, label), pre, toggle];
}

/**
 * approvalCard renders one approval. countdowns collects {el, approval} so
 * the caller can tick them.
 */
export function approvalCard(ctx, a, countdowns, { compact = false } = {}) {
  const left = h("span", { class: "countdown" }, formatCountdown(secondsLeft(a, Date.now())));
  countdowns.push({ el: left, approval: a });
  const facts = [
    h("div", {}, h("span", { class: "meta" }, "from "), h("a", { href: windowHash(a.session, a.window_id) }, a.asking_agent || "agent"), a.project ? h("span", { class: "meta" }, ` · ${a.project}`) : null),
  ];
  const rows = detailRows(a);
  if (rows) {
    facts.push(rows);
  } else if (a.kind === "dispatch") {
    facts.push(h("div", {}, h("span", { class: "meta" }, "target "), a.target_agent || "(default agent)"));
    if (a.issue) facts.push(h("div", {}, h("span", { class: "meta" }, "issue "), `#${a.issue}`));
  } else if (a.kind === "cron") {
    facts.push(h("div", {}, h("span", { class: "meta" }, "scheduled agent "), a.target_agent || "(unknown)"));
  }
  const body = [];
  if (a.prompt) body.push(...textBlock(a.kind === "cron" ? "Job" : "Prompt", a.prompt));
  body.push(...textBlock("Command", a.command));

  const canWrite = ctx.hasFeature("write");
  let buttons;
  if (canWrite) {
    const pair = [];
    const deny = h("button", { class: "btn danger", onclick: () => answerApproval(ctx, a, "deny", pair) }, "Deny");
    const allow = h(
      "button",
      { class: "btn primary", onclick: () => answerApproval(ctx, a, "allow", pair) },
      approvalNeedsConfirm(a, "allow") ? "Approve…" : "Approve",
    );
    pair.push(deny, allow);
    buttons = h("div", { class: "row" }, deny, allow);
  } else buttons = h("p", { class: "muted" }, "Read-only: answering needs serve.allow_write.");

  return h(
    "section",
    { class: "approval" },
    h("div", { class: "approval-head" }, h("strong", {}, approvalSummary(a)), left),
    ...facts,
    ...(compact ? [h("a", { class: "btn", href: approvalHash(a.id) }, "Review")] : [...body, buttons]),
  );
}

/** tickCountdowns refreshes countdown text from the phone's clock. */
export function tickCountdowns(countdowns) {
  const now = Date.now();
  for (const c of countdowns) c.el.textContent = formatCountdown(secondsLeft(c.approval, now));
}

/** resolvedCard says what happened to an approval that is no longer pending. */
function resolvedCard(r, { withBack = false } = {}) {
  const links = [];
  if (withBack) links.push(h("a", { class: "btn", href: approvalHash("") }, "Approvals"));
  if (r.session && r.window_id) links.push(h("a", { class: "btn", href: windowHash(r.session, r.window_id) }, r.asking_agent || "agent"));
  return h(
    "section",
    { class: "approval-resolved" },
    h("strong", {}, resolvedText(r, Date.now())),
    h("div", { class: "meta" }, approvalSummary(r)),
    links.length ? h("div", { class: "row" }, ...links) : null,
  );
}

export function mountApprovals(root, ctx, { id }) {
  const list = h("div", { class: "approvals" });
  root.append(h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Fleet"), h("h1", {}, "Approvals")), list);
  let countdowns = [];
  // resolved is the GET /v1/approvals/{id} answer for an id that is not
  // pending: a history entry, or "gone" for 404.
  let resolved = null;
  let lookedUp = false;

  async function lookup() {
    if (lookedUp) return;
    lookedUp = true;
    try {
      const r = await get(approvalApiPath(id));
      resolved = r?.status === "resolved" ? r : null;
      if (r?.status === "pending") ctx.refreshApprovals();
    } catch (e) {
      resolved = e instanceof ApiError && e.status === 404 ? { gone: true } : null;
    }
    render();
  }

  function recentSection() {
    const recent = ctx.store.recentApprovals ?? [];
    if (id || recent.length === 0) return null;
    const rows = recent.map((r) =>
      h("div", { class: "row-item" }, h("a", { href: approvalHash(r.id) }, approvalSummary(r)), h("div", { class: "meta" }, resolvedText(r, Date.now()))),
    );
    return h("details", { class: "approval-recent" }, h("summary", {}, `Recent (${recent.length})`), ...rows);
  }

  function appendRecent() {
    const r = recentSection();
    if (r) list.append(r);
  }

  function render() {
    clear(list);
    countdowns = [];
    const all = ctx.store.approvals ?? [];
    const shown = id ? all.filter((a) => a.id === id) : all;
    if (id && shown.length === 0) {
      if (resolved && !resolved.gone) list.append(resolvedCard(resolved, { withBack: true }));
      else if (resolved?.gone) {
        list.append(
          h("p", { class: "muted pad" }, "This approval is no longer available."),
          h("p", { class: "pad" }, h("a", { href: approvalHash("") }, "Approvals")),
        );
      } else {
        list.append(h("p", { class: "muted pad" }, "Checking…"));
        lookup();
      }
      return;
    }
    if (shown.length === 0) {
      list.append(h("p", { class: "muted pad" }, "No approvals waiting."));
      appendRecent();
      return;
    }
    for (const a of shown) list.append(approvalCard(ctx, a, countdowns));
    appendRecent();
  }

  render();
  ctx.refreshApprovals();
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "approvals") {
      // A pending approval that just resolved needs a fresh lookup.
      if (id && !(ctx.store.approvals ?? []).some((a) => a.id === id)) {
        lookedUp = false;
        resolved = null;
      }
      render();
    }
  });
  const tick = setInterval(() => tickCountdowns(countdowns), 1000);
  return () => {
    unsub();
    clearInterval(tick);
  };
}
