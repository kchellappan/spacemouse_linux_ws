# Prior Art: spacenav-ws

<https://github.com/RmStorm/spacenav-ws> — Python/FastAPI bridge exposing
spacenavd over WSS for Onshape on Linux. MIT. Roughly 570 lines.

Referenced from the spacenavd README, where upstream asks for someone to take
it over and fold it into the free spacenav project (see doc 05).

Read it before writing anything. It is the only working reference for the
protocol, and its navigation model is the only reference for the algorithm.

## Layout

| File | Role |
|---|---|
| `main.py` | FastAPI app, HTTPS `/3dconnexion/nlproxy`, WSS `/`, CLI, TLS |
| `wamp.py` | WAMP v1 message types, prefix resolution, RPC bookkeeping |
| `controller.py` | Handshake, property read/write, **navigation model** |
| `spacenav.py` | spacenavd AF_UNIX socket reader, event decode |
| `additional/har_captures/` | Two captured browser sessions — very useful |
| `additional/onshape-3d-mouse-linux.user.js` | Platform-sniff workaround (**now obsolete**, doc 07) |

## What it gets right

- The full WAMP v1 handshake, including the reverse-RPC-inside-EVENT envelope.
- CORS origins: `https://127.51.68.120`, `https://127.51.68.120:8181`,
  `https://3dconnexion.com`, `https://cad.onshape.com`.
- `{"port": 8181, "version": "1.4.8.21486"}` from the discovery endpoint.
- Per-connection controller state — `create_mouse_controller` is called inside
  the WebSocket handler, so state dies with the socket. Its hardcoded
  `"controller0"` id is safe precisely because nothing outlives the connection.
- **Stateless with respect to the view.** It re-reads `view.affine` on every
  event rather than accumulating internally, which makes page-refresh
  correctness free (see doc 01).

## The navigation model

`controller.py::update_client` — the closest thing to a reference for the part
3Dconnexion ships as a closed binary:

1. Read `model.extents`, `view.perspective`, `view.affine`.
2. Extract camera rotation as the transpose of the top-left 3×3, then
   **orthonormalise via SVD** (`U @ Vt`) — the comment notes the raw transpose
   is numerically unstable.
3. Build the delta: Euler `xyz` from `(pitch, yaw, -roll) × 0.02` degrees;
   translation from `(-x, -z, y) × 0.0005`.
4. Conjugate the rotation into world space: `R_world = R_cam @ R_delta @ R_camᵀ`.
5. Apply about the model bounding-box centre:
   `new = trans_delta @ curr @ (pivot_neg @ rot_delta @ pivot_pos)`.
6. Orthographic: also scale `view.extents` by `1 + y × 0.0002`.
7. Write `motion` then `view.affine`.

Button press = snap to `views.front` and fit (`view.extents × 1.2`).

Note this assumes translation at flat indices 12–14 (`trans_delta[3, :3]`),
i.e. the modern layout — and it works against Onshape, which is how we know
Onshape uses that layout.

## Gaps — the work if you extend rather than rewrite

| Gap | Impact |
|---|---|
| **Private key committed** (`certs/ip.key`) | Disqualifying for distribution. See doc 04. |
| No `frame.timingSource` support | The 0.8.1 sample's animation loop times out after 60 ms and stalls. |
| Matrix layout hardcoded | Breaks on pre-0.5 clients. See doc 03. |
| One frame per device event | Steady hold produces stutter, not smooth motion. See doc 05. |
| Client name whitelist | `["Onshape", "WebThreeJS Sample"]` — anything else is refused. |
| No hit testing | `hit.lookfrom` / `hit.lookat` unimplemented, so no surface-aware pivot. |
| No `commands` / `images` | Button-bound app commands unsupported. |
| Fixed gain constants | No sensitivity config, no deadzone shaping. |
| Single client | One WebSocket assumed. |
| Obsolete userscript in README | Tells users to install Tampermonkey unnecessarily. |

## Build on it or rewrite?

Its gaps map almost exactly onto what upstream describes as "past the proof of
concept stage". If the goal includes upstreaming, extending it is the shorter
path and comes with a maintainer who wants the result.

If the goal is fleet distribution (doc 08), a Go rewrite gives a static binary
and a far simpler `.deb` — but the protocol layer and the navigation model
should still be ported from here rather than rediscovered.

Either way, the two HAR captures in `additional/har_captures/` are the most
valuable artefact in the repo. Use them to build replay tests.
