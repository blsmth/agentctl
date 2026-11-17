// Fleet screen: project groups ordered by attention.
import { effectiveState, formatClock, formatSince, sortGroups, windowHash } from "./logic.js";
import { clear, h } from "./ui.js";

const GLYPH = { blocked: "⏸", working: "●", done: "✓", idle: "○" };

function windowRow(win) {
  const state = effectiveState(win);
  const since = formatSince(win.blocked_since || win.state_since, Date.now());
  const meta = [win.window_id, win.mode, win.branch].filter(Boolean).join(" · ");
  return h(
    "a",
    { class: `win ${state}`, href: windowHash(win.session, win.window_id) },
    h("span", { class: "glyph", "aria-label": state }, GLYPH[state]),
    h(
      "span",
      { class: "win-main" },
      h("span", { class: "win-title" }, win.agent || "(bare)", h("span", { class: "meta" }, ` ${meta}`)),
      win.activity ? h("span", { class: "activity" }, win.activity) : null,
    ),
    h(
      "span",
      { class: "win-side" },
      since ? h("span", {}, since) : null,
      win.context_pct ? h("span", { class: "meta" }, `${Math.round(win.context_pct)}%`) : null,
    ),
  );
}

export function mountFleet(root, ctx) {
  const banner = h("div", { class: "banner", hidden: true });
  const list = h("div", { class: "fleet" });
  root.append(
    h("header", { class: "bar" }, h("h1", {}, "Fleet"), ctx.hasFeature("write") ? h("a", { class: "btn", href: "#/dispatch" }, "Dispatch") : null),
    banner,
    list,
  );

  function render() {
    const { fleet, downSince } = ctx.store;
    if (downSince && Date.now() - downSince > 15000) {
      banner.textContent = `laptop unreachable since ${formatClock(downSince)}`;
      banner.hidden = false;
    } else {
      banner.hidden = true;
    }
    clear(list);
    if (!fleet) {
      list.append(h("p", { class: "muted pad" }, "Loading…"));
      return;
    }
    const groups = sortGroups(fleet.groups ?? []);
    if (groups.length === 0) list.append(h("p", { class: "muted pad" }, "No agents running."));
    for (const g of groups) {
      list.append(
        h(
          "section",
          { class: `group ${g.rollup}` },
          h("h2", {}, g.project || g.session),
          ...g.windows.map(windowRow),
        ),
      );
    }
  }

  render();
  const unsub = ctx.store.subscribe(render);
  // Refresh "time in state" and the unreachable banner without a server event.
  const tick = setInterval(render, 5000);
  return () => {
    unsub();
    clearInterval(tick);
  };
}
