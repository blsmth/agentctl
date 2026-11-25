/* agentctl artifact kit: renders <script type="application/json" id="spec"> into <main>. No dependencies, ES2020. Contract: kit/SPEC.md. */
(function (global) {
  "use strict";

  var doc = global.document;
  var TONES = ["info", "good", "warn", "bad"];
  var TYPES = ["number", "percent", "currency", "duration", "date", "datetime", "text", "status"];
  var UNKNOWN = "unknown";

  // ---- pinned CDN libraries (loaded only when a block needs them) ----
  var LIBS = {
    chart: {
      src: "https://cdn.jsdelivr.net/npm/chart.js@4.4.7/dist/chart.umd.min.js",
      integrity: "sha384-vsrfeLOOY6KuIYKDlmVH5UiBmgIdB1oEf7p01YgWHuqmOHfZr374+odEv96n9tNC",
      global: "Chart"
    },
    mermaid: {
      src: "https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js",
      integrity: "sha384-rbtjAdnIQE/aQJGEgXrVUlMibdfTSa4PQju4HDhN3sR2PmaKFzhEafuePsl9H/9I",
      global: "mermaid"
    }
  };
  var loading = {};
  function loadLib(name) {
    var lib = LIBS[name];
    if (global[lib.global]) return Promise.resolve(global[lib.global]);
    if (!loading[name]) {
      loading[name] = new Promise(function (resolve, reject) {
        var s = doc.createElement("script");
        s.src = lib.src;
        s.integrity = lib.integrity;
        s.crossOrigin = "anonymous";
        s.onload = function () { global[lib.global] ? resolve(global[lib.global]) : reject(new Error(name + " missing")); };
        s.onerror = function () { reject(new Error(name + " failed to load")); };
        doc.head.appendChild(s);
      });
    }
    return loading[name];
  }

  // ---- formatters: every page formats numbers the same way ----
  var NF = new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 });
  var PF = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });
  var DF = new Intl.DateTimeFormat("en-US", { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" });
  function isNil(v) { return v === null || v === undefined || v === ""; }
  function isNum(v) { return typeof v === "number" && isFinite(v); }
  function pad2(n) { return n < 10 ? "0" + n : String(n); }

  var fmt = {
    unknown: UNKNOWN,
    number: function (v) { return isNil(v) ? UNKNOWN : isNum(v) ? NF.format(v) : String(v); },
    percent: function (v) { return isNil(v) ? UNKNOWN : isNum(v) ? PF.format(v) + "%" : String(v); },
    currency: function (v, code) {
      if (isNil(v)) return UNKNOWN;
      if (!isNum(v)) return String(v);
      var d = Math.abs(v) >= 1000 ? 0 : 2;
      try {
        return new Intl.NumberFormat("en-US", { style: "currency", currency: code || "USD", minimumFractionDigits: d, maximumFractionDigits: d }).format(v);
      } catch (e) { return NF.format(v) + " " + code; }
    },
    // value is in seconds
    duration: function (v) {
      if (isNil(v)) return UNKNOWN;
      if (!isNum(v)) return String(v);
      var sign = v < 0 ? "-" : "", s = Math.abs(v);
      if (s < 1) return sign + Math.round(s * 1000) + " ms";
      if (s < 60) return sign + PF.format(s) + " s";
      var t = Math.round(s), d = Math.floor(t / 86400), hr = Math.floor((t % 86400) / 3600), m = Math.floor((t % 3600) / 60), sec = t % 60;
      if (t < 3600) return sign + m + "m " + pad2(sec) + "s";
      if (t < 86400) return sign + hr + "h " + pad2(m) + "m";
      return sign + d + "d " + hr + "h";
    },
    // ISO "YYYY-MM-DD" or a full ISO timestamp; anything else is shown as given
    date: function (v) {
      if (isNil(v)) return UNKNOWN;
      var s = String(v);
      if (!/^\d{4}-\d{2}-\d{2}(T.*)?$/.test(s)) return s;
      var t = Date.parse(s.length === 10 ? s + "T00:00:00Z" : s);
      return isNaN(t) ? s : DF.format(new Date(t));
    },
    // RFC 3339 timestamp, shown in the viewer's local time as "YYYY-MM-DD HH:MM"
    datetime: function (v) {
      if (isNil(v)) return UNKNOWN;
      var t = Date.parse(String(v));
      if (isNaN(t)) return String(v);
      var d = new Date(t);
      return d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate()) + " " + pad2(d.getHours()) + ":" + pad2(d.getMinutes());
    },
    text: function (v) { return isNil(v) ? UNKNOWN : String(v); },
    status: function (v) { return isNil(v) ? UNKNOWN : String(v); },
    by: function (type, v, code) {
      var f = fmt[type] || fmt.text;
      return f(v, code);
    },
    signed: function (type, v, code) {
      var out = fmt.by(type, v, code);
      return isNum(v) && v > 0 ? "+" + out : out;
    }
  };

  var STATUS_TONES = {
    good: ["ok", "pass", "passed", "passing", "healthy", "good", "done", "success", "succeeded", "green", "up"],
    warn: ["warn", "warning", "degraded", "pending", "slow", "partial", "yellow", "running"],
    bad: ["fail", "failed", "failing", "error", "down", "blocked", "bad", "red", "critical"]
  };
  function statusTone(v) {
    var s = String(v).toLowerCase();
    for (var t in STATUS_TONES) if (STATUS_TONES[t].indexOf(s) >= 0) return t;
    return "";
  }

  // ---- schema: closed set of block types, flat fields ----
  var S = function (o) { return o; };
  var SCHEMA = {
    summary: {
      title: S({ t: "str" }),
      items: S({ t: "arr", req: 1, of: {
        tone: { t: "enum", v: TONES }, title: { t: "str" }, text: { t: "str", req: 1 } } })
    },
    kpis: {
      title: S({ t: "str" }),
      items: S({ t: "arr", req: 1, of: {
        label: { t: "str", req: 1 }, value: { t: "strnum", req: 1, nul: 1 },
        format: { t: "enum", v: ["number", "percent", "currency", "duration", "text"] }, currency: { t: "cur" },
        delta: { t: "strnum" }, trend: { t: "enum", v: ["good", "bad", "neutral"] }, note: { t: "str" },
        breakdown: { t: "arr", of: { label: { t: "str", req: 1 }, value: { t: "strnum", req: 1, nul: 1 } } } } })
    },
    chart: {
      title: S({ t: "str" }),
      chart: S({ t: "enum", req: 1, v: ["line", "bar", "stacked_bar", "doughnut"] }),
      labels: S({ t: "strs", req: 1 }),
      series: S({ t: "arr", req: 1, of: { name: { t: "str", req: 1 }, values: { t: "nums", req: 1 } } }),
      unit: S({ t: "str" }), x_label: S({ t: "str" }), y_label: S({ t: "str" }), note: S({ t: "str" }),
      excluded: S({ t: "strs" })
    },
    table: {
      title: S({ t: "str" }),
      columns: S({ t: "arr", req: 1, of: {
        key: { t: "str", req: 1 }, label: { t: "str", req: 1 }, type: { t: "enum", v: TYPES }, currency: { t: "cur" } } }),
      rows: S({ t: "rows", req: 1 }),
      sortable: S({ t: "bool" }), sort_by: S({ t: "str" }), sort_dir: S({ t: "enum", v: ["asc", "desc"] }), note: S({ t: "str" })
    },
    text: { title: S({ t: "str" }), text: S({ t: "str", req: 1 }) },
    mermaid: { title: S({ t: "str" }), source: S({ t: "str", req: 1 }), caption: S({ t: "str" }) },
    comparison: {
      title: S({ t: "str" }),
      options: S({ t: "arr", req: 1, of: {
        name: { t: "str", req: 1 }, description: { t: "str" }, pros: { t: "strs" }, cons: { t: "strs" },
        effort: { t: "str" }, recommended: { t: "bool" } } }),
      recommendation: S({ t: "str" })
    },
    timeline: {
      title: S({ t: "str" }),
      items: S({ t: "arr", req: 1, of: {
        date: { t: "str", req: 1 }, title: { t: "str", req: 1 }, text: { t: "str" }, tone: { t: "enum", v: TONES } } })
    },
    html: { title: S({ t: "str" }), html: S({ t: "str", req: 1 }) },
    sources: {
      title: S({ t: "str" }),
      items: S({ t: "arr", req: 1, of: { label: { t: "str", req: 1 }, url: { t: "url" }, note: { t: "str" } } })
    }
  };
  var TOP = {
    kind: { t: "enum", req: 1, v: ["report", "dashboard", "comparison", "diagram"] },
    title: { t: "str", req: 1 }, summary: { t: "str" }, date: { t: "isodate" }, author: { t: "str" }, scope: { t: "str" },
    blocks: { t: "arr", req: 1 }
  };

  function isObj(v) { return v !== null && typeof v === "object" && !Array.isArray(v); }

  // ISO calendar date "YYYY-MM-DD" (a real day) or an RFC 3339 timestamp with seconds and an offset
  function validDay(y, m, d) {
    var t = new Date(Date.UTC(y, m - 1, d));
    return t.getUTCFullYear() === y && t.getUTCMonth() === m - 1 && t.getUTCDate() === d;
  }
  function isDateOnly(s) {
    var m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
    return !!m && validDay(+m[1], +m[2], +m[3]);
  }
  function isDateTime(s) {
    var m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-](\d{2}):(\d{2}))$/.exec(s);
    return !!m && validDay(+m[1], +m[2], +m[3]) && +m[4] < 24 && +m[5] < 60 && +m[6] < 60 && (!m[9] || (+m[9] < 24 && +m[10] < 60));
  }

  function check(val, s, path, errs) {
    if (val === undefined) { if (s.req) errs.push([path, "required"]); return; }
    if (val === null) {
      if (s.nul) return;
      errs.push([path, s.req ? "required" : "null is not allowed; omit the field"]);
      return;
    }
    var i;
    switch (s.t) {
      case "str":
        if (typeof val !== "string") errs.push([path, "expected a string"]);
        else if (s.req && val.trim() === "") errs.push([path, "must not be empty"]);
        break;
      case "num": if (!isNum(val)) errs.push([path, "expected a number"]); break;
      case "strnum": if (typeof val !== "string" && !isNum(val)) errs.push([path, "expected a string or number"]); break;
      case "bool": if (typeof val !== "boolean") errs.push([path, "expected true or false"]); break;
      case "enum":
        if (s.v.indexOf(val) < 0) errs.push([path, "expected one of " + s.v.join(", ") + " (got " + JSON.stringify(val) + ")"]);
        break;
      case "isodate":
        if (typeof val !== "string") errs.push([path, "expected a string"]);
        else if (!isDateOnly(val) && !isDateTime(val)) errs.push([path, "expected an ISO date YYYY-MM-DD or an RFC 3339 datetime (got " + JSON.stringify(val) + ")"]);
        break;
      case "cur": if (typeof val !== "string" || !/^[A-Z]{3}$/.test(val)) errs.push([path, "expected a 3-letter ISO currency code such as USD"]); break;
      case "url": if (typeof val !== "string" || !/^https?:\/\//.test(val)) errs.push([path, "expected an http(s) URL"]); break;
      case "strs":
        if (!Array.isArray(val)) errs.push([path, "expected an array of strings"]);
        else val.forEach(function (x, j) { if (typeof x !== "string") errs.push([path + "[" + j + "]", "expected a string"]); });
        break;
      case "nums":
        if (!Array.isArray(val)) errs.push([path, "expected an array of numbers (null for unknown)"]);
        else val.forEach(function (x, j) { if (x !== null && !isNum(x)) errs.push([path + "[" + j + "]", "expected a number or null"]); });
        break;
      case "rows":
        if (!Array.isArray(val)) { errs.push([path, "expected an array of row objects"]); break; }
        val.forEach(function (r, j) {
          if (!isObj(r)) { errs.push([path + "[" + j + "]", "expected an object keyed by column key"]); return; }
          Object.keys(r).forEach(function (k) {
            var x = r[k];
            if (x !== null && typeof x !== "string" && !isNum(x) && typeof x !== "boolean") errs.push([path + "[" + j + "]." + k, "expected a string, number, boolean or null"]);
          });
        });
        break;
      case "arr":
        if (!Array.isArray(val)) { errs.push([path, "expected an array"]); break; }
        if (s.req && val.length === 0) errs.push([path, "must not be empty"]);
        if (s.of) for (i = 0; i < val.length; i++) checkObj(val[i], s.of, path + "[" + i + "]", errs);
        break;
    }
  }
  function checkObj(obj, schema, path, errs) {
    if (!isObj(obj)) { errs.push([path, "expected an object"]); return; }
    Object.keys(obj).forEach(function (k) {
      if (!Object.prototype.hasOwnProperty.call(schema, k)) errs.push([path ? path + "." + k : k, "unknown field"]);
    });
    Object.keys(schema).forEach(function (k) { check(obj[k], schema[k], path ? path + "." + k : k, errs); });
  }

  // cross-field rules that the per-field schema cannot express
  var POST = {
    chart: function (b, errs) {
      if (!Array.isArray(b.labels) || !Array.isArray(b.series)) return;
      b.series.forEach(function (s, i) {
        if (isObj(s) && Array.isArray(s.values) && s.values.length !== b.labels.length)
          errs.push(["series[" + i + "].values", "has " + s.values.length + " values but labels has " + b.labels.length]);
      });
      if (b.chart === "doughnut") {
        if (b.series.length !== 1) errs.push(["series", "a doughnut needs exactly one series"]);
        else if (isObj(b.series[0]) && Array.isArray(b.series[0].values) && b.series[0].values.indexOf(null) >= 0)
          errs.push(["series[0].values", "a doughnut cannot contain null"]);
      }
    },
    table: function (b, errs) {
      if (!Array.isArray(b.columns) || !Array.isArray(b.rows)) return;
      var keys = [];
      b.columns.forEach(function (c, i) {
        if (!isObj(c) || typeof c.key !== "string") return;
        if (keys.indexOf(c.key) >= 0) errs.push(["columns[" + i + "].key", "duplicate column key " + JSON.stringify(c.key)]);
        keys.push(c.key);
      });
      b.rows.forEach(function (r, i) {
        if (isObj(r)) Object.keys(r).forEach(function (k) {
          var ci = keys.indexOf(k);
          if (ci < 0) { errs.push(["rows[" + i + "]." + k, "no column with this key"]); return; }
          var type = b.columns[ci].type, x = r[k];
          if (x === null || (type !== "date" && type !== "datetime")) return;
          if (typeof x !== "string" || !(type === "date" ? isDateOnly(x) || isDateTime(x) : isDateTime(x)))
            errs.push(["rows[" + i + "]." + k, type === "date" ? "expected an ISO date YYYY-MM-DD or an RFC 3339 datetime" : "expected an RFC 3339 datetime such as 2026-11-24T09:30:00Z"]);
        });
      });
      if (typeof b.sort_by === "string" && keys.indexOf(b.sort_by) < 0) errs.push(["sort_by", "no column with this key"]);
    },
    comparison: function (b, errs) {
      if (!Array.isArray(b.options)) return;
      if (b.options.length < 2) errs.push(["options", "needs at least 2 options"]);
      var rec = b.options.filter(function (o) { return isObj(o) && o.recommended === true; });
      if (rec.length > 1) errs.push(["options", "at most one option may be recommended"]);
    }
  };

  function validateBlock(b) {
    var errs = [];
    if (!isObj(b)) return [["type", "block must be an object"]];
    if (typeof b.type !== "string" || !Object.prototype.hasOwnProperty.call(SCHEMA, b.type)) {
      return [["type", "unknown block type " + JSON.stringify(b.type) + " (expected one of " + Object.keys(SCHEMA).join(", ") + ")"]];
    }
    var schema = SCHEMA[b.type];
    Object.keys(b).forEach(function (k) {
      if (k !== "type" && !Object.prototype.hasOwnProperty.call(schema, k)) errs.push([k, "unknown field"]);
    });
    Object.keys(schema).forEach(function (k) { check(b[k], schema[k], k, errs); });
    if (!errs.length && POST[b.type]) POST[b.type](b, errs);
    return errs;
  }
  function validateSpec(spec) {
    var errs = [];
    if (!isObj(spec)) return [["spec", "must be a JSON object"]];
    Object.keys(spec).forEach(function (k) { if (!TOP[k]) errs.push([k, "unknown field"]); });
    Object.keys(TOP).forEach(function (k) { check(spec[k], TOP[k], k, errs); });
    return errs;
  }

  // ---- DOM helpers (everything is set via textContent, so content is escaped) ----
  function h(tag, cls, text) {
    var e = doc.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }
  function cssVar(n) { return getComputedStyle(doc.documentElement).getPropertyValue(n).trim(); }
  function isDark() { return global.matchMedia && global.matchMedia("(prefers-color-scheme: dark)").matches; }

  function errorCard(title, errs) {
    var c = h("div", "error-card");
    c.setAttribute("role", "alert");
    c.append(h("b", "", title));
    var ul = h("ul");
    errs.forEach(function (e) {
      var li = h("li");
      li.append(h("code", "", e[0]), doc.createTextNode(": " + e[1]));
      ul.append(li);
    });
    c.append(ul);
    return c;
  }

  var state = { redraws: [] };

  function section(b, cls, defaultTitle) {
    var s = h("section", "block block-" + cls);
    var t = b.title || defaultTitle;
    if (t) s.append(h("h2", "", t));
    return s;
  }

  // ---- block renderers ----
  var R = {};

  R.summary = function (b) {
    var s = section(b, "summary"), g = h("div", "callouts");
    b.items.forEach(function (it) {
      var c = h("div", "card callout " + (it.tone || "info"));
      if (it.title) c.append(h("b", "", it.title));
      c.append(doc.createTextNode(it.text));
      g.append(c);
    });
    s.append(g);
    return s;
  };

  R.kpis = function (b) {
    var s = section(b, "kpis"), g = h("div", "kpis");
    b.items.forEach(function (k) {
      var type = k.format || "number";
      var c = h("div", "card kpi");
      c.append(h("div", "label", k.label));
      var v = h("div", "value", fmt.by(type, k.value, k.currency));
      if (isNil(k.value)) v.classList.add("unknown");
      c.append(v);
      if (!isNil(k.delta)) {
        var d = h("div", "delta " + (k.trend && k.trend !== "neutral" ? k.trend : ""));
        var text = isNum(k.delta) ? fmt.signed(type, k.delta, k.currency) : String(k.delta);
        var first = text.charAt(0);
        var arrow = first === "+" ? "▲ " : first === "-" || first === "−" ? "▼ " : "";
        d.append(doc.createTextNode(arrow + text));
        if (k.trend === "good" || k.trend === "bad") d.append(h("span", "sr", " (" + k.trend + ")"));
        c.append(d);
      }
      if (k.note) c.append(h("div", "note", k.note));
      if (k.breakdown && k.breakdown.length) {
        var ul = h("ul", "breakdown");
        k.breakdown.forEach(function (r) {
          var li = h("li");
          li.append(h("span", "", r.label), h("span", "", fmt.by(type, r.value, k.currency)));
          ul.append(li);
        });
        c.append(ul);
      }
      g.append(c);
    });
    s.append(g);
    return s;
  };

  function compare(type, a, b) {
    var an = isNil(a), bn = isNil(b);
    if (an || bn) return an === bn ? 0 : an ? 1 : -1; // unknown always last (handled by caller for direction)
    if (type === "date" || type === "datetime") return Date.parse(a) - Date.parse(b) || 0;
    if (isNum(a) && isNum(b)) return a - b;
    return String(a).localeCompare(String(b), "en", { numeric: true });
  }

  function buildTable(columns, rows, opts) {
    opts = opts || {};
    var wrap = h("div", "table-wrap"), table = h("table");
    var thead = h("thead"), hr = h("tr"), tbody = h("tbody");
    var sort = { i: -1, dir: 1 };
    var ths = [];
    function numeric(c) { return c.type === "number" || c.type === "percent" || c.type === "currency" || c.type === "duration"; }
    function paint() {
      var order = rows.slice();
      if (sort.i >= 0) {
        var c = columns[sort.i];
        order.sort(function (x, y) {
          var xn = isNil(x[c.key]), yn = isNil(y[c.key]);
          if (xn || yn) return xn === yn ? 0 : xn ? 1 : -1;
          return compare(c.type, x[c.key], y[c.key]) * sort.dir;
        });
      }
      tbody.replaceChildren();
      order.forEach(function (r) {
        var tr = h("tr");
        columns.forEach(function (c) {
          var td = h("td", numeric(c) ? "num" : c.type === "date" || c.type === "datetime" ? "nowrap" : "");
          var v = r[c.key];
          if (isNil(v)) td.append(h("span", "unknown", UNKNOWN));
          else if (c.type === "status") td.append(h("span", "badge " + statusTone(v), String(v)));
          else td.textContent = fmt.by(c.type || "text", v, c.currency);
          tr.append(td);
        });
        tbody.append(tr);
      });
      ths.forEach(function (th, i) {
        if (i === sort.i) th.setAttribute("aria-sort", sort.dir === 1 ? "ascending" : "descending");
        else th.removeAttribute("aria-sort");
      });
    }
    columns.forEach(function (c, i) {
      var th = h("th", numeric(c) ? "num" : "");
      th.scope = "col";
      if (opts.sortable === false) th.append(h("span", "", c.label));
      else {
        var btn = h("button", "sort", c.label);
        btn.type = "button";
        btn.addEventListener("click", function () {
          sort = { i: i, dir: sort.i === i ? -sort.dir : 1 };
          paint();
        });
        th.append(btn);
      }
      ths.push(th);
      hr.append(th);
    });
    if (opts.sortBy !== undefined) {
      sort.i = columns.findIndex(function (c) { return c.key === opts.sortBy; });
      if (opts.sortDir === "desc") sort.dir = -1;
    }
    thead.append(hr);
    table.append(thead, tbody);
    wrap.append(table);
    paint();
    return wrap;
  }

  R.table = function (b) {
    var s = section(b, "table");
    s.append(buildTable(b.columns, b.rows, { sortable: b.sortable, sortBy: b.sort_by, sortDir: b.sort_dir }));
    if (b.note) s.append(h("p", "chart-note", b.note));
    return s;
  };

  R.text = function (b) {
    var s = section(b, "text"), box = h("div", "text");
    var list = null, para = null;
    function flush() { list = null; para = null; }
    b.text.split("\n").forEach(function (line) {
      var l = line.trim();
      if (l === "") { flush(); return; }
      if (/^[-*] /.test(l)) {
        para = null;
        if (!list) { list = h("ul"); box.append(list); }
        list.append(h("li", "", l.slice(2).trim()));
      } else {
        list = null;
        if (!para) { para = h("p", "", l); box.append(para); }
        else para.textContent += " " + l;
      }
    });
    s.append(box);
    return s;
  };

  R.html = function (b) {
    var s = section(b, "html"), box = h("div", "html-block");
    box.innerHTML = b.html;
    s.append(box);
    return s;
  };

  R.comparison = function (b) {
    var s = section(b, "comparison"), g = h("div", "options");
    b.options.forEach(function (o) {
      var c = h("article", "card opt" + (o.recommended ? " rec" : ""));
      if (o.recommended) c.append(h("span", "badge good", "Recommended"));
      c.append(h("h3", "", o.name));
      if (o.description) c.append(h("p", "", o.description));
      [["Pros", "pros", o.pros], ["Cons", "cons", o.cons]].forEach(function (g2) {
        if (!g2[2] || !g2[2].length) return;
        c.append(h("h4", "", g2[0]));
        var ul = h("ul", g2[1]);
        g2[2].forEach(function (t) { ul.append(h("li", "", t)); });
        c.append(ul);
      });
      if (o.effort) { c.append(h("h4", "", "Effort")); c.append(h("p", "", o.effort)); }
      g.append(c);
    });
    s.append(g);
    if (b.recommendation) {
      var r = h("div", "card reco");
      r.append(h("b", "", "Recommendation"), h("p", "", b.recommendation));
      s.append(r);
    }
    return s;
  };

  R.timeline = function (b) {
    var s = section(b, "timeline"), ol = h("ol", "timeline");
    b.items.forEach(function (it) {
      var li = h("li", it.tone && it.tone !== "info" ? it.tone : "");
      li.append(h("div", "when", fmt.date(it.date)), h("b", "", it.title));
      if (it.text) li.append(h("div", "", it.text));
      ol.append(li);
    });
    s.append(ol);
    return s;
  };

  R.sources = function (b) {
    var s = h("footer", "block block-sources sources");
    s.append(h("h2", "", b.title || "Sources"));
    var ol = h("ol");
    b.items.forEach(function (it) {
      var li = h("li");
      if (it.url) {
        var a = h("a", "", it.label);
        a.href = it.url; a.target = "_blank"; a.rel = "noopener";
        li.append(a);
      } else li.append(doc.createTextNode(it.label));
      if (it.note) li.append(h("span", "muted", " — " + it.note));
      ol.append(li);
    });
    s.append(ol);
    return s;
  };

  R.mermaid = function (b) {
    var s = section(b, "mermaid"), card = h("div", "card diagram");
    var pre = h("pre", "mermaid", b.source);
    card.append(pre);
    s.append(card);
    if (b.caption) s.append(h("p", "chart-note", b.caption));
    var note = h("p", "chart-note", "Mermaid needs internet access to load; the diagram source is shown instead.");
    note.hidden = true;
    s.append(note);
    var lib = null;
    function paint() {
      pre.textContent = b.source;
      pre.removeAttribute("data-processed");
      lib.initialize({
        startOnLoad: false, securityLevel: "strict", theme: "base",
        themeVariables: {
          darkMode: isDark(), background: cssVar("--bg"), fontFamily: cssVar("--font"),
          primaryColor: cssVar("--card"), primaryTextColor: cssVar("--fg"), primaryBorderColor: cssVar("--muted"),
          secondaryColor: cssVar("--card"), tertiaryColor: cssVar("--bg"),
          lineColor: cssVar("--muted"), textColor: cssVar("--fg"), edgeLabelBackground: cssVar("--bg"), noteBkgColor: cssVar("--card"), noteTextColor: cssVar("--fg"),
          actorBkg: cssVar("--card"), actorBorder: cssVar("--muted"), actorTextColor: cssVar("--fg"), signalColor: cssVar("--muted"), signalTextColor: cssVar("--fg")
        }
      });
      return lib.run({ nodes: [pre] }).catch(function (e) {
        pre.textContent = b.source;
        pre.classList.add("plain");
        note.textContent = "Mermaid could not render this diagram (" + (e && e.message ? e.message : e) + "); the source is shown instead.";
        note.hidden = false;
      });
    }
    state.work.push(loadLib("mermaid").then(function (m) {
      lib = m;
      state.redraws.push(paint);
      return paint();
    }, function () {
      pre.classList.add("plain");
      note.hidden = false;
    }));
    return s;
  };

  R.chart = function (b) {
    var s = section(b, "chart"), card = h("div", "card");
    var box = h("div", "chart-box"), cv = h("canvas");
    cv.setAttribute("role", "img");
    cv.setAttribute("aria-label", b.title || "Chart");
    box.append(cv);
    card.append(box);
    s.append(card);
    if (b.note) s.append(h("p", "chart-note", b.note));
    (b.excluded || []).forEach(function (t) { s.append(h("p", "chart-note excluded", "Excluded: " + t)); });
    var chart = null;
    function paint(Chart) {
      if (chart) chart.destroy();
      var pal = [1, 2, 3, 4, 5, 6].map(function (i) { return cssVar("--c" + i); });
      var line = cssVar("--line"), muted = cssVar("--muted"), fg = cssVar("--fg"), cardBg = cssVar("--card");
      Chart.defaults.color = muted;
      Chart.defaults.borderColor = line;
      Chart.defaults.font.family = cssVar("--font");
      var unit = b.unit ? " " + b.unit : "";
      var dough = b.chart === "doughnut";
      var type = b.chart === "stacked_bar" ? "bar" : b.chart;
      var datasets = b.series.map(function (se, i) {
        var col = pal[i % pal.length];
        if (dough) return { label: se.name, data: se.values, backgroundColor: b.labels.map(function (_, j) { return pal[j % pal.length]; }), borderColor: cardBg, borderWidth: 2 };
        return { label: se.name, data: se.values, borderColor: col, backgroundColor: col, tension: 0.25, borderWidth: 2, pointRadius: 2, spanGaps: false };
      });
      function axisTitle(label, withUnit) {
        var t = [label, withUnit && b.unit ? "(" + b.unit + ")" : ""].filter(Boolean).join(" ");
        return t ? { display: true, text: t, color: muted } : { display: false };
      }
      var options = {
        responsive: true, maintainAspectRatio: false,
        plugins: {
          legend: { display: dough || b.series.length > 1, position: "bottom", labels: { color: fg, usePointStyle: true, boxWidth: 8, boxHeight: 8 } },
          tooltip: { callbacks: { label: function (c) {
            var v = c.parsed && typeof c.parsed === "object" ? c.parsed.y : c.parsed;
            return (dough ? c.label : c.dataset.label) + ": " + fmt.number(v) + unit;
          } } }
        }
      };
      if (!dough) {
        var stacked = b.chart === "stacked_bar";
        options.scales = {
          x: { stacked: stacked, grid: { color: line }, ticks: { color: muted }, title: axisTitle(b.x_label, false) },
          y: { stacked: stacked, beginAtZero: type === "bar", grid: { color: line }, ticks: { color: muted, callback: function (v) { return fmt.number(v); } }, title: axisTitle(b.y_label, true) }
        };
      } else options.cutout = "60%";
      chart = new Chart(cv, { type: type, data: { labels: b.labels, datasets: datasets }, options: options });
    }
    state.work.push(loadLib("chart").then(function (Chart) {
      paint(Chart);
      state.redraws.push(function () { paint(Chart); });
    }, function () {
      box.remove();
      card.append(h("p", "muted", "Charts need internet access to load Chart.js; the numbers are shown as a table instead."));
      var cols = [{ key: "label", label: b.x_label || "Label", type: "text" }].concat(b.series.map(function (se, i) {
        return { key: "s" + i, label: se.name + (b.unit ? " (" + b.unit + ")" : ""), type: "number" };
      }));
      var rows = b.labels.map(function (l, j) {
        var r = { label: l };
        b.series.forEach(function (se, i) { r["s" + i] = se.values[j]; });
        return r;
      });
      card.append(buildTable(cols, rows, { sortable: false }));
    }));
    return s;
  };

  // ---- page ----
  function header(spec) {
    var hd = h("header", "page-head");
    hd.append(h("h1", "", spec.title));
    if (spec.summary) hd.append(h("p", "lede", spec.summary));
    var meta = [spec.date ? fmt.date(spec.date) : "", spec.author || "", spec.scope || ""].filter(Boolean).join(" · ");
    if (meta) hd.append(h("p", "meta", meta));
    return hd;
  }

  function render() {
    var main = doc.getElementById("kit-main") || doc.querySelector("main");
    if (!main) { main = doc.createElement("main"); doc.body.prepend(main); }
    main.replaceChildren();
    state = { redraws: [], work: [] };
    var el = doc.getElementById("spec"), spec;
    if (!el) { main.append(errorCard("Spec missing", [["#spec", 'no <script type="application/json" id="spec"> element'] ])); return Promise.resolve(); }
    try { spec = JSON.parse(el.textContent); }
    catch (e) { main.append(errorCard("Spec is not valid JSON", [["#spec", e.message]])); return Promise.resolve(); }

    var topErrs = validateSpec(spec);
    // the blocks array is checked per block below; only report top-level problems here
    if (topErrs.length) main.append(errorCard("Spec problem", topErrs.map(function (e) { return [e[0], e[1]]; })));
    if (!isObj(spec) || typeof spec.title !== "string") return Promise.resolve();
    doc.title = spec.title;
    main.append(header(spec));

    var blocks = Array.isArray(spec.blocks) ? spec.blocks : [];
    var hasSources = false;
    blocks.forEach(function (b, i) {
      var errs = validateBlock(b);
      if (errs.length) {
        main.append(errorCard("Block " + i + (isObj(b) && typeof b.type === "string" ? " (" + b.type + ")" : ""),
          errs.map(function (e) { return ["blocks[" + i + "]." + e[0], e[1]]; })));
        return;
      }
      if (b.type === "sources") hasSources = true;
      try { main.append(R[b.type](b)); }
      catch (e) { main.append(errorCard("Block " + i + " (" + b.type + ") failed to render", [["blocks[" + i + "]", String(e && e.message || e)]])); }
    });
    if (!hasSources && (spec.kind === "report" || spec.kind === "dashboard")) {
      var w = h("div", "card callout warn");
      w.append(h("b", "", "No sources"), doc.createTextNode("This " + spec.kind + " has no sources block, so its numbers cannot be traced. Add one."));
      main.append(w);
    }
    return Promise.all(state.work);
  }

  var Kit = { version: 1, render: render, validate: validateSpec, validateBlock: validateBlock, fmt: fmt, blockTypes: Object.keys(SCHEMA) };
  global.Kit = Kit;

  if (global.matchMedia) {
    var mq = global.matchMedia("(prefers-color-scheme: dark)");
    var onScheme = function () { state.redraws.forEach(function (f) { f(); }); };
    if (mq.addEventListener) mq.addEventListener("change", onScheme); else if (mq.addListener) mq.addListener(onScheme);
  }
  if (doc.getElementById("spec")) render();
})(typeof window !== "undefined" ? window : this);
