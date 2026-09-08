// Status page. No dependencies and no build step: the page is served from the
// bridge's own binary, and a CDN would be both a network dependency and a CSP
// violation on an origin that CAD sites already talk to.
"use strict";

import { onSettings } from "/assets/tuning.js";

const $ = (id) => document.getElementById(id);
const AXES = [
  ["x", "x", false], ["y", "y", false], ["z", "z", false],
  ["rx", "rx", true], ["ry", "ry", true], ["rz", "rz", true],
];

let lastSeq = 0;

function pill(text, kind) {
  const s = document.createElement("span");
  s.className = "pill " + kind;
  s.textContent = text;
  return s;
}

function setPill(el, text, kind) {
  el.replaceChildren(pill(text, kind));
}

function fmtDate(iso) {
  if (!iso || iso.startsWith("0001-01-01")) return "—";
  return new Date(iso).toLocaleDateString(undefined,
    { year: "numeric", month: "short", day: "numeric" });
}

function fmtSince(iso) {
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 60) return Math.floor(secs) + "s";
  if (secs < 3600) return Math.floor(secs / 60) + "m";
  if (secs < 86400) return Math.floor(secs / 3600) + "h";
  return Math.floor(secs / 86400) + "d";
}

function buildAxes() {
  const host = $("axes");
  for (const [key, label] of AXES) {
    const row = document.createElement("div");
    row.className = "axis";

    const name = document.createElement("span");
    name.textContent = label;

    const bar = document.createElement("div");
    bar.className = "bar";
    const fill = document.createElement("i");
    fill.id = "bar-" + key;
    bar.appendChild(fill);

    const out = document.createElement("output");
    out.id = "val-" + key;
    out.textContent = "0";

    row.append(name, bar, out);
    host.appendChild(row);
  }
}

// Each axis is drawn against its own full scale: translation and rotation do
// not share a range, so one divisor would misreport half of them.
function renderDevice(d) {
  for (const [key, , isRot] of AXES) {
    const scale = (isRot ? d.rotationFullScale : d.fullScale) || 350;
    const v = d[key] || 0;
    const frac = Math.max(-1, Math.min(1, v / scale));
    const fill = $("bar-" + key);
    const pct = Math.abs(frac) * 50;
    fill.style.left = (frac < 0 ? 50 - pct : 50) + "%";
    fill.style.width = pct + "%";
    $("val-" + key).textContent = String(v);
  }
}

function renderClients(list) {
  const body = $("clients");
  if (!list || list.length === 0) {
    body.innerHTML = "";
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 6;
    td.className = "empty";
    td.textContent = "no pages connected";
    tr.appendChild(td);
    body.appendChild(tr);
    return;
  }
  body.replaceChildren(...list.map((c) => {
    const tr = document.createElement("tr");
    for (const text of [
      c.origin || "—",
      c.name || "—",
      c.libVersion ? String(c.libVersion) : "—",
      c.matrixLayout || "—",
      c.clientFrameTiming ? "client" : "bridge",
      fmtSince(c.connectedAt),
    ]) {
      const td = document.createElement("td");
      td.textContent = text;
      tr.appendChild(td);
    }
    return tr;
  }));
}

function renderProfiles(list) {
  const body = $("profiles");
  if (!list || list.length === 0) {
    body.replaceChildren();
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.className = "empty";
    td.textContent = "none found";
    tr.appendChild(td);
    body.appendChild(tr);
    return;
  }
  body.replaceChildren(...list.map((p) => {
    const tr = document.createElement("tr");
    const name = document.createElement("td");
    name.textContent = p.label;
    const state = document.createElement("td");
    state.appendChild(p.trusted ? pill("trusted", "ok") : pill("missing", "warn"));
    tr.append(name, state);
    return tr;
  }));
}

function renderStatus(s) {
  $("version").textContent = s.version ? "v" + s.version : "";
  $("uptime").textContent = s.startedAt ? "up " + fmtSince(s.startedAt) : "";

  const sp = s.spacenavd || {};
  if (!sp.enabled) {
    // Not an error: probe, orbit and none modes never open the device.
    setPill($("spnav-status"), "not in drive mode", "warn");
  } else {
    setPill($("spnav-status"), sp.connected ? "connected" : (sp.error || "disconnected"),
      sp.connected ? "ok" : "bad");
  }
  $("spnav-socket").textContent = sp.socket || "—";
  $("spnav-dropped").textContent = String(sp.dropped || 0);

  renderDevice(s.device || {});

  const c = s.certs || {};
  setPill($("cert-state"), c.present ? (c.keyModeOK ? "installed" : "key permissions")
    : "not generated", c.present ? (c.keyModeOK ? "ok" : "bad") : "warn");
  $("cert-ca").textContent = fmtDate(c.caExpiry);
  $("cert-leaf").textContent = fmtDate(c.leafExpiry);
  $("cert-certutil").textContent = c.certutilOK ? "available" : "not installed";

  renderProfiles(s.profiles);
  renderClients(s.clients);
  onSettings(s.settings);
}

function appendLogs(records) {
  const box = $("log");
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;

  for (const r of records) {
    if (r.seq <= lastSeq) continue;
    lastSeq = r.seq;

    const line = document.createElement("div");
    line.className = "rec " + (r.level || "INFO");

    const t = document.createElement("time");
    t.textContent = new Date(r.time).toLocaleTimeString();
    const lvl = document.createElement("span");
    lvl.className = "lvl";
    lvl.textContent = r.level || "";
    const msg = document.createElement("span");
    msg.className = "msg";
    msg.textContent = r.message;

    line.append(t, lvl, msg);
    if (r.attrs) {
      const at = document.createElement("span");
      at.className = "at";
      at.textContent = Object.entries(r.attrs)
        .map(([k, v]) => k + "=" + v).join(" ");
      line.appendChild(at);
    }
    box.appendChild(line);
  }

  // Trim, or a page left open all day grows without bound.
  while (box.childElementCount > 500) box.removeChild(box.firstChild);
  if (atBottom) box.scrollTop = box.scrollHeight;
}

function connect() {
  const live = $("live");
  // EventSource reconnects on its own; `since` means a reconnect resumes
  // rather than replaying everything already on screen.
  const es = new EventSource("/api/events?since=" + lastSeq);

  es.addEventListener("open", () => {
    live.className = "live";
    live.innerHTML = "live <b>&bull;</b>";
  });
  es.addEventListener("status", (e) => renderStatus(JSON.parse(e.data)));
  es.addEventListener("logs", (e) => appendLogs(JSON.parse(e.data)));
  es.addEventListener("error", () => {
    live.className = "live stale";
    live.innerHTML = "reconnecting <b>&bull;</b>";
  });
}

buildAxes();
connect();
