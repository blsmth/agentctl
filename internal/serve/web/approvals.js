// Approvals screen: dispatch and command approvals waiting on the laptop's
// popup, answerable from the phone. Prompt and command are shown in full, as
// text only, and every answer goes through a confirm sheet and carries the
// payload hash of what was shown.
import { ApiError, send } from "./api.js";
import {
  approvalConfirm,
  approvalHash,
  approvalSummary,
  buildApprovalRequest,
  formatCountdown,
  secondsLeft,
  windowHash,
} from "./logic.js";
import { clear, confirmSheet, h, toast } from "./ui.js";

/**
 * answerApproval confirms and sends one answer. On 409 (already answered, or
 * changed) or 404 it toasts and asks the store to refetch.
 */
async function answerApproval(ctx, a, decision) {
  let req;
  try {
    req = buildApprovalRequest(a, decision);
  } catch (e) {
    toast(e.message, "error");
    return;
  }
  if (!(await confirmSheet(approvalConfirm(a, decision)))) return;
  try {
    await send(req);
    toast(decision === "allow" ? "Approved" : "Denied");
  } catch (e) {
    if (e instanceof ApiError && (e.status === 409 || e.status === 404)) toast("Already answered or changed", "error");
    else toast(e instanceof ApiError ? e.message : String(e), "error");
  }
  ctx.refreshApprovals();
}

/**
 * approvalCard renders one approval. countdowns collects {el, approval} so
 * the caller can tick them.
 */
export function approvalCard(ctx, a, countdowns, { compact = false } = {}) {
  const left = h("span", { class: "countdown" }, formatCountdown(secondsLeft(a, Date.now())));
  countdowns.push({ el: left, approval: a });
  const facts = [
    h("div", {}, h("span", { class: "meta" }, "from "), h("a", { href: windowHash(a.session, a.window_id) }, `${a.asking_agent || "agent"} ${a.window_id}`), a.project ? h("span", { class: "meta" }, ` · ${a.project}`) : null),
  ];
  if (a.kind === "dispatch") {
    facts.push(h("div", {}, h("span", { class: "meta" }, "target "), a.target_agent || "(default agent)"));
    if (a.issue) facts.push(h("div", {}, h("span", { class: "meta" }, "issue "), `#${a.issue}`));
  }
  const body = [];
  if (a.prompt) body.push(h("div", { class: "meta" }, "Prompt"), h("pre", { class: "approval-text" }, a.prompt));
  body.push(h("div", { class: "meta" }, "Command"), h("pre", { class: "approval-text" }, a.command));

  const canWrite = ctx.hasFeature("write");
  const buttons = canWrite
    ? h(
        "div",
        { class: "row" },
        h("button", { class: "btn danger", onclick: () => answerApproval(ctx, a, "deny") }, "Deny"),
        h("button", { class: "btn primary", onclick: () => answerApproval(ctx, a, "allow") }, "Approve…"),
      )
    : h("p", { class: "muted" }, "Read-only: answering needs serve.allow_write.");

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

export function mountApprovals(root, ctx, { id }) {
  const list = h("div", { class: "approvals" });
  root.append(h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Fleet"), h("h1", {}, "Approvals")), list);
  let countdowns = [];

  function render() {
    clear(list);
    countdowns = [];
    const all = ctx.store.approvals ?? [];
    const shown = id ? all.filter((a) => a.id === id) : all;
    if (id && shown.length === 0) {
      list.append(
        h("p", { class: "muted pad" }, "This approval is no longer pending."),
        all.length > 0 ? h("p", { class: "pad" }, h("a", { href: approvalHash("") }, `Show ${all.length} waiting`)) : null,
      );
      return;
    }
    if (shown.length === 0) {
      list.append(h("p", { class: "muted pad" }, "No approvals waiting."));
      return;
    }
    for (const a of shown) list.append(approvalCard(ctx, a, countdowns));
  }

  render();
  ctx.refreshApprovals();
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "approvals") render();
  });
  const tick = setInterval(() => tickCountdowns(countdowns), 1000);
  return () => {
    unsub();
    clearInterval(tick);
  };
}
