// Tuning controls. Edits apply live so the change can be felt with the puck
// in hand; nothing reaches disk until Save.
"use strict";

// Ranges are wide on purpose. They bound what will not break navigation, not
// what is a sensible choice — someone who wants an absurdly fast pan is
// entitled to it. The server validates the same bounds independently.
const FIELDS = [
  { key: "panSpeed",    label: "pan speed",    min: 0.05, max: 4,   step: 0.05,
    hint: "model diagonals per second" },
  { key: "rotateSpeed", label: "rotate speed", min: 0.1,  max: 6,   step: 0.1,
    hint: "radians per second" },
  { key: "zoomSpeed",   label: "zoom speed",   min: 0.1,  max: 5,   step: 0.1,
    hint: "orthographic e-foldings per second" },
  { key: "curve",       label: "curve",        min: 1,    max: 4,   step: 0.1,
    hint: "1 is linear; higher gives finer control near centre" },
  { key: "deadzone",    label: "dead zone",    min: 0,    max: 0.4, step: 0.005,
    hint: "fraction of full scale ignored" },
  { key: "frameRate",   label: "frame rate",   min: 15,   max: 144, step: 1,
    hint: "camera updates per second", integer: true },
];

const TOGGLES = [
  { key: "enableTranslation", label: "translation" },
  { key: "enableRotation",    label: "rotation" },
  { key: "dominantAxis",      label: "dominant axis" },
];

const ACTIONS = ["fit", "menu", "dominant-axis", "rotation-lock", "none"];

let applied = null;   // what the server currently has
let editing = false;  // suppress incoming refreshes mid-drag

const statusEl = () => document.getElementById("tune-status");

function setStatus(text, kind) {
  const el = statusEl();
  el.className = "meta" + (kind ? " " + kind : "");
  el.textContent = text;
}

async function push(settings, persist) {
  const url = "/api/settings" + (persist ? "?persist=1" : "");
  const resp = await fetch(url, {
    method: "PUT",
    // Not decoration: this header is what forces a cross-origin caller to
    // preflight, and nothing answers the preflight.
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
  if (!resp.ok) {
    throw new Error((await resp.text()).trim() || resp.statusText);
  }
  return resp.json();
}

async function apply(persist) {
  try {
    await push(applied, persist);
    setStatus(persist ? "saved" : "applied", persist ? "ok" : "");
  } catch (err) {
    setStatus(String(err.message || err), "bad");
  }
}

function row(field, value) {
  const wrap = document.createElement("div");
  wrap.className = "tune";

  const label = document.createElement("label");
  label.textContent = field.label;
  label.title = field.hint;
  label.htmlFor = "tune-" + field.key;

  const input = document.createElement("input");
  input.type = "range";
  input.id = "tune-" + field.key;
  input.min = field.min;
  input.max = field.max;
  input.step = field.step;
  input.value = value;

  const out = document.createElement("output");
  out.textContent = field.integer ? String(value) : Number(value).toFixed(2);

  input.addEventListener("input", () => {
    editing = true;
    const v = field.integer ? parseInt(input.value, 10) : parseFloat(input.value);
    applied[field.key] = v;
    out.textContent = field.integer ? String(v) : v.toFixed(2);
    apply(false);
  });
  input.addEventListener("change", () => { editing = false; });

  wrap.append(label, input, out);
  return wrap;
}

function toggle(t, value) {
  const wrap = document.createElement("div");
  wrap.className = "tune check";

  const label = document.createElement("label");
  label.textContent = t.label;
  label.htmlFor = "tune-" + t.key;

  const input = document.createElement("input");
  input.type = "checkbox";
  input.id = "tune-" + t.key;
  input.checked = !!value;
  input.addEventListener("change", () => {
    applied[t.key] = input.checked;
    apply(false);
  });

  wrap.append(label, input);
  return wrap;
}

function modeRow(value) {
  const wrap = document.createElement("div");
  wrap.className = "tune check";
  const label = document.createElement("label");
  label.textContent = "nav mode";
  label.title = "object: the model follows the cap. camera: the camera does.";

  const sel = document.createElement("select");
  for (const m of ["object", "camera"]) {
    const opt = document.createElement("option");
    opt.value = m;
    opt.textContent = m;
    opt.selected = value === m;
    sel.appendChild(opt);
  }
  sel.addEventListener("change", () => {
    applied.navMode = sel.value;
    apply(false);
  });

  wrap.append(label, sel);
  return wrap;
}

function buttonRow(id, action) {
  const wrap = document.createElement("div");
  wrap.className = "tune check";
  const label = document.createElement("label");
  label.textContent = "button " + id;

  const sel = document.createElement("select");
  for (const a of ACTIONS) {
    const opt = document.createElement("option");
    opt.value = a;
    opt.textContent = a;
    opt.selected = action === a;
    sel.appendChild(opt);
  }
  sel.addEventListener("change", () => {
    applied.buttons = Object.assign({}, applied.buttons, { [id]: sel.value });
    apply(false);
  });

  wrap.append(label, sel);
  return wrap;
}

function render(settings) {
  applied = JSON.parse(JSON.stringify(settings));
  const host = document.getElementById("tuning");
  host.replaceChildren();

  for (const f of FIELDS) host.appendChild(row(f, settings[f.key]));
  host.appendChild(modeRow(settings.navMode));
  for (const t of TOGGLES) host.appendChild(toggle(t, settings[t.key]));

  const ids = Object.keys(settings.buttons || {}).sort((a, b) => a - b);
  for (const id of ids) host.appendChild(buttonRow(id, settings.buttons[id]));
}

// The status stream is the source of truth, so a change made elsewhere — a
// button toggling dominant-axis, say — shows up here without a reload. It is
// ignored mid-drag, or the slider would fight the user's thumb.
export function onSettings(settings) {
  if (!settings) return;
  if (!applied) {
    render(settings);
    return;
  }
  if (editing) return;
  if (JSON.stringify(settings) !== JSON.stringify(applied)) render(settings);
}

document.getElementById("save").addEventListener("click", () => apply(true));
document.getElementById("revert").addEventListener("click", async () => {
  try {
    const resp = await fetch("/api/status");
    const snap = await resp.json();
    // Reverting means "what is on disk", which a reload of the service would
    // restore. Applying the saved document is the closest we can get without
    // a second endpoint.
    render(snap.settings);
    await apply(false);
    setStatus("reverted to the applied settings");
  } catch (err) {
    setStatus(String(err.message || err), "bad");
  }
});
