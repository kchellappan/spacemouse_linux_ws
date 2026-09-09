// spacenavd's own configuration — effectively spnavcfg in the browser.
//
// Everything here is machine-wide: spacenavd applies it to every client it
// serves, not just browser CAD. That is why this lives in its own card, with
// its own save button, rather than among per-application controls.
"use strict";

(function () {
  const AXES = ["x", "y", "z", "rx", "ry", "rz"];
  const $ = (id) => document.getElementById(id);

  let current = null;

  function status(text, kind) {
    const el = $("dev-status");
    el.className = "meta" + (kind ? " " + kind : "");
    el.textContent = text;
  }

  async function send(path, method, body) {
    const resp = await fetch(path, {
      method,
      // Not decoration: this header forces a cross-origin caller to
      // preflight, and nothing answers the preflight.
      headers: { "Content-Type": "application/json" },
      body: method === "GET" ? undefined : JSON.stringify(body === undefined ? {} : body),
    });
    const text = await resp.text();
    if (!resp.ok) throw new Error(text.trim() || resp.statusText);
    return text ? JSON.parse(text) : null;
  }

  async function push() {
    try {
      const updated = await send("/api/device", "PUT", current);
      render(updated, { keepStatus: true });
      status("applied — not yet saved");
    } catch (err) {
      status(String(err.message || err), "bad");
    }
  }

  function slider(label, value, opts, onChange) {
    const wrap = document.createElement("div");
    wrap.className = "tune";

    const l = document.createElement("label");
    l.textContent = label;
    if (opts.hint) l.title = opts.hint;

    const input = document.createElement("input");
    input.type = "range";
    input.min = opts.min;
    input.max = opts.max;
    input.step = opts.step;
    input.value = value;

    const out = document.createElement("output");
    const fmt = (v) => (opts.integer ? String(v) : Number(v).toFixed(2));
    out.textContent = fmt(value);

    input.addEventListener("input", () => {
      const v = opts.integer ? parseInt(input.value, 10) : parseFloat(input.value);
      out.textContent = fmt(v);
      onChange(v);
    });
    input.addEventListener("change", push);

    wrap.append(l, input, out);
    return wrap;
  }

  function check(label, value, onChange) {
    const wrap = document.createElement("div");
    wrap.className = "tune check";
    const l = document.createElement("label");
    l.textContent = label;
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = !!value;
    input.addEventListener("change", () => { onChange(input.checked); push(); });
    wrap.append(l, input);
    return wrap;
  }

  function group(title, rows) {
    const g = document.createElement("div");
    g.className = "devgroup";
    const h = document.createElement("h3");
    h.textContent = title;
    g.append(h, ...rows);
    return g;
  }

  function render(cfg, opts) {
    opts = opts || {};
    const host = $("device");
    const unsupported = $("dev-unsupported");

    if (!cfg || !cfg.supported) {
      host.replaceChildren();
      $("dev-actions").hidden = true;
      $("dev-scope").hidden = true;
      unsupported.hidden = false;
      unsupported.textContent = (cfg && cfg.reason)
        ? cfg.reason
        : "spacenavd's configuration is not available.";
      return;
    }

    current = JSON.parse(JSON.stringify(cfg));
    unsupported.hidden = true;
    $("dev-scope").hidden = false;
    $("dev-actions").hidden = false;
    if (!opts.keepStatus) status("");

    // Sensitivity is a bare multiply with no clamp, so full deflection
    // reports 350 x this. Above about 4 the device saturates before the cap
    // reaches its stop, which is why the range stops where it does.
    const sens = [
      slider("overall", cfg.sensitivity,
        { min: 0.1, max: 4, step: 0.05, hint: "multiplies every motion value" },
        (v) => { current.sensitivity = v; }),
    ];
    AXES.forEach((name, i) => {
      sens.push(slider(name, cfg.axisSensitivity[i], { min: 0, max: 4, step: 0.05 },
        (v) => { current.axisSensitivity[i] = v; }));
    });

    const dz = AXES.map((name, i) =>
      slider(name, cfg.deadzone[i], { min: 0, max: 50, step: 1, integer: true,
        hint: "raw device units ignored as noise" },
        (v) => { current.deadzone[i] = v; }));

    const inv = AXES.map((name, i) =>
      check("invert " + name, cfg.invert[i], (v) => { current.invert[i] = v; }));
    inv.push(check("swap Y and Z", cfg.swapYZ, (v) => { current.swapYZ = v; }));

    const grid = document.createElement("div");
    grid.className = "devgrid";
    grid.append(
      group("sensitivity", sens),
      group("dead zone", dz),
      group("axis direction", inv),
    );
    host.replaceChildren(grid);
  }

  async function load() {
    try {
      render(await send("/api/device", "GET"));
    } catch (err) {
      render({ supported: false, reason: String(err.message || err) });
    }
  }

  $("dev-save").addEventListener("click", async () => {
    try {
      render(await send("/api/device/save", "POST"), { keepStatus: true });
      status("saved to /etc/spnavrc", "ok");
    } catch (err) {
      status(String(err.message || err), "bad");
    }
  });

  $("dev-reload").addEventListener("click", async () => {
    try {
      render(await send("/api/device/restore", "POST"), { keepStatus: true });
      status("reloaded from /etc/spnavrc", "ok");
    } catch (err) {
      status(String(err.message || err), "bad");
    }
  });

  // Read once at load. This is not on the status stream: it is not live data,
  // and polling it would mean a request storm of six dead-zone reads every
  // tick.
  load();
})();
