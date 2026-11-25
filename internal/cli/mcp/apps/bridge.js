// hlp MCP Apps bridge: a hand-written postMessage JSON-RPC client for the
// io.modelcontextprotocol/ui extension (protocol 2026-01-26). No SDK.
// View side only: ui/initialize, tool-input/tool-result/host-context-changed
// notifications, size-changed, tools/call, ui/update-model-context,
// ui/open-link. Third-party text is set with textContent only, never parsed as HTML.
const hlp = (() => {
  "use strict";
  const PROTOCOL = "2026-01-26";
  const pending = new Map();
  const listeners = {};
  let seq = 0, hostContext = {}, hostCaps = {}, hostInfo = {}, ready = false;
  let last = { input: null, result: null };

  function post(msg) {
    msg.jsonrpc = "2.0";
    window.parent.postMessage(msg, "*");
  }
  function request(method, params, timeoutMs) {
    const id = ++seq;
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => {
        pending.delete(id);
        reject(new Error(method + ": no reply from host"));
      }, timeoutMs || 30000);
      pending.set(id, { resolve, reject, t });
      post({ id, method, params: params || {} });
    });
  }
  function notify(method, params) { post({ method, params: params || {} }); }
  function emit(ev, arg) { (listeners[ev] || []).forEach((fn) => { try { fn(arg); } catch (e) { console.error(e); } }); }
  function on(ev, fn) {
    (listeners[ev] = listeners[ev] || []).push(fn);
    if (ev === "result" && last.result) fn(last.result); // late subscribers still see the call
    if (ev === "input" && last.input) fn(last.input);
  }

  window.addEventListener("message", (ev) => {
    if (ev.source !== window.parent) return; // only our host
    const m = ev.data;
    if (!m || m.jsonrpc !== "2.0") return;
    if (m.id != null && !m.method) {
      const p = pending.get(m.id);
      if (!p) return;
      pending.delete(m.id);
      clearTimeout(p.t);
      if (m.error) p.reject(Object.assign(new Error(m.error.message || "host error"), { code: m.error.code }));
      else p.resolve(m.result);
      return;
    }
    switch (m.method) {
      case "ui/notifications/tool-input":
        last.input = (m.params && m.params.arguments) || {};
        emit("input", last.input); break;
      case "ui/notifications/tool-result":
        last.result = m.params || {};
        emit("result", last.result); break;
      case "ui/notifications/tool-cancelled":
        emit("cancelled", m.params || {}); break;
      case "ui/notifications/host-context-changed":
        hostContext = Object.assign({}, hostContext, m.params || {});
        applyTheme(); emit("context", hostContext); break;
      case "ui/resource-teardown":
        emit("teardown", m.params || {});
        if (m.id != null) post({ id: m.id, result: {} });
        break;
      default:
        if (m.id != null) post({ id: m.id, error: { code: -32601, message: "method not found" } });
    }
  });

  function applyTheme() {
    let t = hostContext.theme;
    if (t !== "light" && t !== "dark") {
      t = window.matchMedia && matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    }
    document.documentElement.dataset.theme = t;
  }

  let lastH = 0;
  function reportSize() {
    // body, not documentElement: the root is at least the iframe's height,
    // so it could never report a shrink.
    const r = document.body.getBoundingClientRect();
    const h = Math.ceil(r.height), w = Math.ceil(r.width);
    if (h === lastH) return;
    lastH = h;
    notify("ui/notifications/size-changed", { width: w, height: h });
  }

  async function init(appInfo) {
    applyTheme();
    try {
      const r = await request("ui/initialize", {
        appInfo: appInfo || { name: "hlp", version: "1" },
        appCapabilities: {},
        protocolVersion: PROTOCOL,
      }, 10000);
      hostContext = (r && r.hostContext) || {};
      hostCaps = (r && r.hostCapabilities) || {};
      hostInfo = (r && r.hostInfo) || {};
      ready = true;
      applyTheme();
      notify("ui/notifications/initialized", {});
    } catch (e) {
      emit("error", e);
    }
    if (window.ResizeObserver) new ResizeObserver(reportSize).observe(document.body);
    reportSize();
    return { hostContext, hostCaps, hostInfo };
  }

  // tools/call through the host. Resolves to the CallToolResult; a tool
  // error (isError) rejects with the server's code and hint.
  async function callTool(name, args) {
    if (!ready) throw new Error("host not initialized");
    const r = await request("tools/call", { name, arguments: args || {} }, 60000);
    if (r && r.isError) {
      let err = { code: "ERROR", message: "tool error" };
      try { err = JSON.parse(r.content[0].text).error || err; } catch (_) {}
      throw Object.assign(new Error(err.message), err);
    }
    return r;
  }
  function canCallTools() { return ready && !!hostCaps.serverTools; }
  function canOpenLinks() { return ready && !!hostCaps.openLinks; }

  // Tell the model what the user did. A request that was sent is never
  // reported as done.
  function tellModel(text, structured) {
    if (!ready) return Promise.resolve();
    const p = { content: [{ type: "text", text: String(text) }] };
    if (structured) p.structuredContent = structured;
    return request("ui/update-model-context", p).catch((e) => emit("error", e));
  }
  function openLink(url) { return request("ui/open-link", { url: String(url) }); }

  // DOM helper: text only, never HTML.
  function el(tag, attrs, ...kids) {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === "class") n.className = v;
      else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
      else n.setAttribute(k, v === true ? "" : String(v));
    }
    for (const c of kids.flat()) if (c != null && c !== false) n.append(c instanceof Node ? c : document.createTextNode(String(c)));
    return n;
  }
  // Parse server-rendered SVG with DOMParser; drops anything scripted.
  function svg(text) {
    if (!text) return null;
    const doc = new DOMParser().parseFromString(text, "image/svg+xml");
    const root = doc.documentElement;
    if (!root || root.nodeName !== "svg") return null;
    root.querySelectorAll("script,foreignObject").forEach((n) => n.remove());
    return document.importNode(root, true);
  }

  const nf = (d) => new Intl.NumberFormat("en-US", { maximumFractionDigits: d, minimumFractionDigits: d });
  const fmt = {
    usd(v, d) { if (v == null || !isFinite(v)) return "–"; const a = Math.abs(v);
      if (a >= 1e9) return "$" + nf(2).format(v / 1e9) + "B"; if (a >= 1e6) return "$" + nf(2).format(v / 1e6) + "M";
      if (a >= 1e4) return "$" + nf(1).format(v / 1e3) + "K"; return "$" + nf(d == null ? 2 : d).format(v); },
    pct(v, d) { return v == null || !isFinite(v) ? "–" : (v > 0 ? "+" : "") + nf(d == null ? 1 : d).format(v * 100) + "%"; },
    num(v, d) { return v == null || !isFinite(v) ? "–" : nf(d == null ? 2 : d).format(v); },
    px(v) { if (v == null || !isFinite(v)) return "–"; const a = Math.abs(v);
      return nf(a >= 1000 ? 0 : a >= 100 ? 1 : a >= 1 ? 2 : Math.min(8, 3 - Math.floor(Math.log10(a || 1)))).format(v); },
    ago(iso) { const t = Date.parse(iso); if (!t) return ""; const s = (Date.now() - t) / 1000;
      return s < 90 ? "just now" : s < 5400 ? Math.round(s / 60) + "m ago" : s < 129600 ? Math.round(s / 3600) + "h ago" : Math.round(s / 86400) + "d ago"; },
  };
  const env = (r) => (r && r.structuredContent) || {};

  return { init, on, callTool, canCallTools, canOpenLinks, tellModel, openLink, el, svg, fmt, env,
    get hostContext() { return hostContext; }, get last() { return last; } };
})();
