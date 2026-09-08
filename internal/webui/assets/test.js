// Self-test scene. The camera pose arrives from the bridge, already computed
// by the real navigation model; this file only projects and draws. Keeping
// the maths on the server is deliberate — a JavaScript re-derivation would be
// a lookalike, and a bug in the lookalike would be indistinguishable from a
// bug in the bridge, which is the question this page exists to answer.
"use strict";

const canvas = document.getElementById("scene");
const ctx = canvas.getContext("2d");
const liveEl = document.getElementById("live");
const movingEl = document.getElementById("moving");
const framesEl = document.getElementById("frames");

// Unit cube at the origin, matching the model the bridge steps against.
const H = 0.5;
const VERTS = [
  [-H, -H, -H], [H, -H, -H], [H, H, -H], [-H, H, -H],
  [-H, -H,  H], [H, -H,  H], [H, H,  H], [-H, H,  H],
];
const EDGES = [
  [0,1],[1,2],[2,3],[3,0], [4,5],[5,6],[6,7],[7,4], [0,4],[1,5],[2,6],[3,7],
];
// One corner tinted so rotation is readable: a plain wireframe cube looks
// identical after a 90-degree turn.
const AXES = [
  [[0,0,0],[H*1.6,0,0],"#e5484d"],
  [[0,0,0],[0,H*1.6,0],"#30a46c"],
  [[0,0,0],[0,0,H*1.6],"#3b5bdb"],
];

let camera = null;
let frames = 0;

// The camera matrix is camera-to-world, column-major. Rendering needs the
// inverse: world-to-camera. It is a rigid transform, so the inverse is the
// transposed rotation with the translation carried back through it.
function worldToCamera(m) {
  const r = [m[0], m[1], m[2], m[4], m[5], m[6], m[8], m[9], m[10]];
  const t = [m[12], m[13], m[14]];
  const inv = [r[0], r[3], r[6], r[1], r[4], r[7], r[2], r[5], r[8]];
  const it = [
    -(inv[0]*t[0] + inv[3]*t[1] + inv[6]*t[2]),
    -(inv[1]*t[0] + inv[4]*t[1] + inv[7]*t[2]),
    -(inv[2]*t[0] + inv[5]*t[1] + inv[8]*t[2]),
  ];
  return { r: inv, t: it };
}

function project(p, view) {
  const { r, t } = view;
  const x = r[0]*p[0] + r[3]*p[1] + r[6]*p[2] + t[0];
  const y = r[1]*p[0] + r[4]*p[1] + r[7]*p[2] + t[1];
  const z = r[2]*p[0] + r[5]*p[1] + r[8]*p[2] + t[2];

  // Camera looks down -Z, so a visible point has negative z.
  const depth = -z;
  if (depth <= 0.01) return null;

  const f = canvas.height * 0.9;
  return [canvas.width / 2 + (x * f) / depth, canvas.height / 2 - (y * f) / depth];
}

function draw() {
  const style = getComputedStyle(document.body);
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  if (!camera) return;

  const view = worldToCamera(camera);

  ctx.lineWidth = 2;
  for (const [a, b, colour] of AXES) {
    const pa = project(a, view), pb = project(b, view);
    if (!pa || !pb) continue;
    ctx.strokeStyle = colour;
    ctx.beginPath();
    ctx.moveTo(pa[0], pa[1]);
    ctx.lineTo(pb[0], pb[1]);
    ctx.stroke();
  }

  ctx.strokeStyle = style.color;
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  for (const [i, j] of EDGES) {
    const a = project(VERTS[i], view), b = project(VERTS[j], view);
    if (!a || !b) continue;
    ctx.moveTo(a[0], a[1]);
    ctx.lineTo(b[0], b[1]);
  }
  ctx.stroke();
}

function fitCanvas() {
  const width = Math.min(canvas.parentElement.clientWidth - 32, 900);
  const ratio = window.devicePixelRatio || 1;
  canvas.style.width = width + "px";
  canvas.style.height = Math.round(width * 0.62) + "px";
  canvas.width = Math.round(width * ratio);
  canvas.height = Math.round(width * 0.62 * ratio);
  draw();
}

function connect() {
  const es = new EventSource("/api/scene");

  es.addEventListener("open", () => {
    liveEl.className = "live";
    liveEl.innerHTML = "live <b>&bull;</b>";
  });
  es.addEventListener("message", (e) => {
    const frame = JSON.parse(e.data);
    camera = frame.camera;
    frames++;
    movingEl.textContent = frame.moved ? "yes" : "no";
    framesEl.textContent = String(frames);
    draw();
  });
  es.addEventListener("error", () => {
    liveEl.className = "live stale";
    liveEl.innerHTML = "reconnecting <b>&bull;</b>";
  });
}

window.addEventListener("resize", fitCanvas);
fitCanvas();
connect();
