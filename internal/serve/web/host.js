// Host screen: connection health, machine load and agentctl processes.
import { get } from "./api.js";
import { formatBytes, formatSince, formatSkew, formatUptime, hostConnection, hostLevels, pushSample } from "./logic.js";
import { h } from "./ui.js";

const REFRESH_MS = 4000;

const lvl = (level) => (level ? ` ${level}` : "");

function bar(frac, level) {
  const pct = Math.round(Math.min(Math.max(frac, 0), 1) * 100);
  return h("div", { class: "hbar" }, h("div", { class: `hbar-fill${lvl(level)}`, style: `width:${pct}%` }));
}

function kv(label, value, level) {
  return h("div", { class: "hkv" }, h("span", { class: "muted" }, label), h("span", { class: `hval${lvl(level)}` }, value));
}

export function mountHost(root, ctx) {
  const body = h("div", { class: "form host" });
  root.append(
    h("header", { class: "bar" }, h("a", { class: "btn", href: "#/settings" }, "‹ Settings"), h("h1", {}, "Host")),
    body,
  );
  let stopped = false;
  let timer = null;
  let gen = 0; // bumped on every (re)start so a stale reply never re-arms
  let rtts = [];
  let data = null;
  let conn = null;
  let error = "";

  function render() {
    body.replaceChildren();
    body.append(connectionSection(), ...(data ? [hostSection(), processSection()] : []));
  }

  function connectionSection() {
    const fleetAt = ctx.store.fleetAt;
    const down = ctx.store.downSince;
    const fleet = down
      ? kv("Fleet feed", `down for ${formatSince(new Date(down).toISOString(), Date.now())}`, "bad")
      : fleetAt
        ? kv("Fleet feed", `ok, last update ${formatSince(new Date(fleetAt).toISOString(), Date.now())} ago`)
        : kv("Fleet feed", "no update yet", "warn");
    const slow = conn && conn.rtt > 1000 ? "bad" : conn && conn.rtt > 400 ? "warn" : "";
    return h(
      "section",
      { class: "hsec" },
      h("h2", {}, "Connection"),
      error ? h("p", { class: "hval bad" }, `Request failed: ${error}`) : null,
      kv("Server", location.host),
      kv("Round trip", conn ? `${conn.rtt} ms` : "…", slow),
      rtts.length > 1 ? kv("Recent", rtts.map((r) => `${r}`).join(" · ") + " ms") : null,
      kv("Clock skew", conn ? formatSkew(conn.skew) : "…", conn && conn.skew !== null && Math.abs(conn.skew) > 5000 ? "warn" : ""),
      fleet,
    );
  }

  function hostSection() {
    const host = data.host ?? {};
    const lv = hostLevels(data);
    const rows = [h("h2", {}, host.hostname ? `Host: ${host.hostname}` : "Host")];
    const sub = [host.os && host.arch ? `${host.os}/${host.arch}` : "", host.uptime_sec ? `up ${formatUptime(host.uptime_sec)}` : ""].filter(Boolean);
    if (sub.length) rows.push(h("p", { class: "muted" }, sub.join(" · ")));
    if (typeof host.cpu_percent === "number") {
      rows.push(kv("CPU", `${host.cpu_percent.toFixed(0)}%${host.cores ? ` of ${host.cores} cores` : ""}`, lv.cpu), bar(host.cpu_percent / 100, lv.cpu));
    }
    if (host.load) {
      const l = host.load;
      rows.push(kv("Load 1/5/15", `${l.l1.toFixed(2)} / ${l.l5.toFixed(2)} / ${l.l15.toFixed(2)}${host.cores ? ` (${host.cores} cores)` : ""}`, lv.load));
      if (host.cores) rows.push(bar(l.l1 / host.cores, lv.load));
    }
    if (host.memory) {
      const m = host.memory;
      rows.push(kv("Memory", `${formatBytes(m.total - m.available)} of ${formatBytes(m.total)} (${formatBytes(m.available)} free)`, lv.memory), bar(1 - m.available / m.total, lv.memory));
    }
    if (host.swap && host.swap.total > 0) {
      rows.push(kv("Swap", `${formatBytes(host.swap.used)} of ${formatBytes(host.swap.total)}`, lv.swap), bar(host.swap.used / host.swap.total, lv.swap));
    }
    if (host.memory_pressure) rows.push(kv("Memory pressure", host.memory_pressure, lv.pressure));
    return h("section", { class: "hsec" }, rows);
  }

  function processSection() {
    const a = data.agentctl ?? {};
    const p = data.processes ?? {};
    const head = [`agentctl ${a.version || "?"}`, a.uptime_sec !== undefined ? `up ${formatUptime(a.uptime_sec)}` : "", a.tmux_windows !== undefined ? `${a.tmux_windows} tmux windows` : ""].filter(Boolean);
    const groups = (p.groups ?? []).map((g) => {
      const level = g.cpu_percent > 100 ? "bad" : g.cpu_percent > 40 ? "warn" : "";
      return h("tr", {}, h("td", {}, g.kind), h("td", { class: "num" }, g.count), h("td", { class: `num${lvl(level)}` }, `${g.cpu_percent.toFixed(0)}%`), h("td", { class: "num" }, formatBytes(g.rss)));
    });
    const top = (p.top ?? []).map((t) =>
      h("tr", {}, h("td", {}, t.kind || t.name), h("td", { class: "num" }, t.pid), h("td", { class: "num" }, `${t.cpu_percent.toFixed(0)}%`), h("td", { class: "num" }, formatBytes(t.rss))),
    );
    return h(
      "section",
      { class: "hsec" },
      h("h2", {}, "agentctl"),
      h("p", { class: "muted" }, head.join(" · ")),
      h("table", { class: "htable" }, h("thead", {}, h("tr", {}, h("th", {}, "Kind"), h("th", { class: "num" }, "#"), h("th", { class: "num" }, "CPU"), h("th", { class: "num" }, "RSS"))), h("tbody", {}, groups)),
      top.length ? h("h2", {}, "Top by CPU") : null,
      top.length ? h("table", { class: "htable" }, h("thead", {}, h("tr", {}, h("th", {}, "Process"), h("th", { class: "num" }, "PID"), h("th", { class: "num" }, "CPU"), h("th", { class: "num" }, "RSS"))), h("tbody", {}, top)) : null,
    );
  }

  async function refresh() {
    if (stopped || document.hidden) return;
    const my = ++gen;
    clearTimeout(timer);
    const sent = Date.now();
    try {
      const res = await get("/v1/host");
      const got = Date.now();
      if (stopped || my !== gen) return;
      data = res;
      conn = hostConnection(res.server_time, sent, got);
      rtts = pushSample(rtts, conn.rtt);
      error = "";
    } catch (e) {
      if (stopped || my !== gen) return;
      error = e instanceof Error ? e.message : String(e);
    }
    render();
    if (!document.hidden) timer = setTimeout(refresh, REFRESH_MS);
  }

  // Also repaint the connection ages when the fleet feed changes.
  const unsub = ctx.store.subscribe((ev) => {
    if (ev.type === "status" || ev.type === "fleet") render();
  });
  // Pause while the page is hidden; resume on return.
  const onVis = () => {
    if (document.hidden) {
      gen++; // invalidate any in-flight reply
      clearTimeout(timer);
    } else refresh();
  };
  document.addEventListener("visibilitychange", onVis);

  render();
  refresh();
  return () => {
    stopped = true;
    clearTimeout(timer);
    unsub();
    document.removeEventListener("visibilitychange", onVis);
  };
}
