# Implementation

Go module rooted at the repository root. Build with the toolchain in
`~/.local/go` (installed outside apt; remove with `rm -rf ~/.local/go`).

```
cmd/spacemouse-bridge/   flags, TLS, signal handling
internal/wamp/           WAMP v1: message codec, session, reverse-RPC
internal/navlib/         property model, quirks, matrix math, handshake
internal/server/         HTTP/WS wiring, discovery endpoint, probe + orbit
internal/spacenav/       spacenavd client, axis calibration
internal/nav/            navigation model: deflection -> camera motion
internal/config/         persisted settings, flag precedence (doc 10)
internal/logbuf/         in-memory ring of recent log records
internal/webui/          status page, embedded assets, SSE stream
internal/certs/          CA and leaf generation, NSS trust injection
packaging/               systemd user unit, deb maintainer scripts, nfpm
scripts/                 build and run helpers
web/testpage/            dependency-free browser harness
```

The module path is `github.com/kchellappan/spacemouse_linux_ws`. It matches
the repository URL because Go resolves imports by fetching that address —
identity and location are the same string. The Go tree sits at the root
rather than under `src/` so that release tags can be plain `v0.1.0`; a module
in a subdirectory requires tags prefixed with that subdirectory, which would
collide with the tags carrying the `.deb`.

## The status UI

Served at `https://127.51.68.120:8181/`, replacing the three-line placeholder.
Assets are embedded with `embed.FS`, so the single static binary survives and
there is no CDN to fail offline or trip the page's own CSP.

**Server-sent events, not a WebSocket.** The traffic is one-way, the browser
reconnects by itself, and the WAMP endpoint already owns the only WebSocket on
this origin — two socket protocols on one server is a debugging trap. The
stream sends `status` on a 100 ms tick and `logs` on write. 125 Hz device
updates are deliberately not forwarded: ten frames a second is enough to watch
a puck move and cheap enough to leave open all day.

**Log records are teed into a ring buffer** (`internal/logbuf`) as well as
stderr. A user diagnosing a broken setup is usually in a browser, and under a
snap or flatpak browser may not be able to reach `journalctl` at all. Writes
never block: the buffer is on the path of the WAMP read loop and the drive
loop, and a status page must not be able to stall navigation. Subscribers get
a coalescing one-slot channel, so a slow reader misses wake-ups but never
records — it re-reads with `Since`.

**`/api` is same-origin only and carries no CORS headers**, even though it is
read-only, because the log discloses which sites connected. See doc 10.

**The certutil sweep is cached for 10 seconds.** Checking trust forks a process
per browser profile, and the page polls ten times a second.

**Plain HTTP is redirected, not rejected.** Browsers default a bare
`host:port` to `http://`, and this address is typed rather than clicked, so
Go's "Client sent an HTTP request to an HTTPS server" is what a user sees
first. `server.PlaintextRedirect` classifies each connection by its first byte
— `0x16` is a TLS handshake record, which no HTTP method can start with — and
answers plaintext with a 308 that preserves the path.

Classification runs per connection in its own goroutine rather than inline in
`Accept`. Doing it inline means one client that connects and sends nothing
stalls every connection behind it, and browsers open speculative connections
routinely. A test pins that.

## Design decisions worth knowing

**HTTP/1.1 is forced** (`TLSNextProto` set to an empty map). Go negotiates h2
by default and the WebSocket upgrader cannot hijack an h2 connection. Browsers
use HTTP/1.1 for WebSocket regardless, and the real NL-Proxy is HTTP/1.1 only,
so there is no reason to leave the failure mode reachable.

**`OnReady` runs on the session read loop's goroutine.** That loop delivers RPC
replies, so anything calling back into the client must start its own goroutine
or it deadlocks. `Probe` and `Orbit` both do this internally and are safe to
pass directly.

**Matrix layout is data, not assumption.** `navlib.QuirksFor` derives it from
the `info` object at `create 3dcontroller`; `Matrix4.Canonical` /
`FromCanonical` convert at the wire boundary so all arithmetic happens in one
column-major form. `WriteMatrix4` refuses to write a matrix tagged with a
different layout than the client's. See doc 03.

**State is per-connection.** The controller is created inside the WebSocket
handler and dies with the socket, so a page reload cannot inherit a stale
camera. Drivers re-read `view.affine` each frame rather than integrating
internally. See doc 01.

**Client-driven frame timing never runs on the read loop.** A client that sets
`frame.timingSource` reports each `requestAnimationFrame` timestamp via
`3dx_rpc:update`, and abandons its animation loop if we take more than 60 ms to
answer. That call arrives on the WAMP read loop — which is also what delivers
replies to *our* RPCs — so doing client work inline would deadlock the two
against each other. `Controller.notifyFrameTime` therefore only publishes to a
lossy one-slot channel and returns; the drive loop wakes on it separately and
uses the client's clock for `dt`. `TestFrameTimingDoesNotDeadlock` guards both
the deadlock and the 60 ms budget.

**The navigation model is a pure function.** `nav.Config.Step(motion, dt,
scene)` takes device units plus the scene and returns the next camera pose, so
it is testable without a device or a browser. The one place the device frame
(Z-up: X right, Y away, Z up) meets the camera frame (OpenGL: X right, Y up,
-Z forward) is the `(x, z, -y)` swap inside `Step`.

**`spacenav.Client` exposes both a channel and a latch.** `State()` returns the
most recent deflection and is always current; the channel can hold stale events
under load. Measured at 125 Hz on a SpaceMouse Compact, so drive navigation
from the latch on your own frame timer rather than one frame per event — the
browser round trip cannot keep up otherwise. When its buffer is full the client
discards the *oldest* event, not the newest, so a consumer that does read the
channel still sees recent data. Anything watching for a state change should
poll the latch. See doc 05.

## Running

```sh
./scripts/run-dev.sh          # terminal 1: the bridge
./scripts/serve-testpage.sh   # terminal 2: http://localhost:8080/web/testpage/
```

Certificates need no separate step: `internal/certs` generates the CA and leaf
under `~/.local/share/spacemouse-bridge` on first start and installs the CA
into every browser profile it finds. Restart the browser afterwards — NSS reads
its store once, at startup. `-untrust` reverses the browser half; `-no-auto-trust`
skips it; explicit `-cert`/`-key` opt out of the managed path entirely, and then
the bridge touches neither the files nor the trust stores.

The shell scripts that used to do this (`dev-certs.sh`, `trust-certs.sh`,
`untrust-certs.sh`) are gone. They duplicated the packaged behaviour in a
second language, which is exactly where a difference between "works on my
machine" and "works after install" would have hidden.

### Modes

| Flag | Effect |
|---|---|
| `-mode drive` (default) | Live navigation: SpaceMouse drives the client's camera. |
| `-mode probe` | Read every navlib property on connect and report what the client implements. Proves the read path. |
| `-mode orbit` | Slowly rotate the client's camera about its model centre. Proves the write path — transactions, motion flags, `view.affine` writes — with no hardware attached. |
| `-mode none` | Connect and idle. |
| `-read-mouse` | Dump spacenavd events and exit. Does not start the server. Use it to confirm hardware before involving a browser. |
| `-calibrate` | Measure the resting noise floor, then walk the six degrees of freedom and report which wire axis, sign and peak each physical motion produces. Full scale is pooled from those six into separate sliding and tipping values. **Writes the result to the settings file** (doc 10). Does not start the server. |
| `-show-config` | Print the effective settings, after flags are layered over the file, and exit. |
| `-selftest` | Check spacenavd, the device, the port, the settings, the certificates and browser trust; report and exit non-zero on failure. |
| `-debug` | Log every WAMP frame in both directions. |

## Verified

**End-to-end navigation works** (2026-09-06): a SpaceMouse Compact drives the
camera in a browser through the full chain — device, spacenavd, bridge, WAMP
over TLS, `view.affine` — in all six degrees of freedom.

Also verified along the way:

- Full handshake and property exchange against the real 3DconnexionJS 0.8.1
  library in Chrome, Firefox and Zen, over TLS on `127.51.68.120:8181`.
- Ten properties read; three unimplemented ones correctly returned `CALLERROR`
  and were reported as unsupported rather than failing.
- Quirks detection: a 0.8 client with `rowMajorOrder: false` resolves to
  column-major with frame timing available.
- The device axis map, twice, independently (doc 05).

## Tuning

`-mode drive` flags, all live-adjustable by restarting:

| Flag | Default | Meaning |
|---|---|---|
| `-nav-mode` | `object` | `object`: the model follows the cap. `camera`: the camera does. |
| `-full-scale` | 350 | Device units at full deflection; take it from `-calibrate`. |
| `-deadzone` | 0.06 | Fraction of full scale ignored. Must exceed the resting noise floor. |
| `-curve` | 1.6 | 1 is linear; higher gives finer control near centre. |
| `-pan-speed` | 0.9 | Model diagonals per second at full deflection. |
| `-rotate-speed` | 1.6 | Radians per second at full deflection. |
| `-zoom-speed` | 1.2 | Orthographic zoom, e-foldings per second at full deflection. |
| `-dominant-axis` | off | Use only the strongest axis. |
| `-frame-rate` | 60 | Camera updates per second when we drive the clock. |
| `-fit-button` | 0 | Device button that frames the model; -1 disables. Unmapped presses are logged so ids can be discovered. |
| `-no-rotate` / `-no-translate` | off | Isolate one half while tuning. |

### Buttons

`-buttons` takes `id=action` pairs, e.g. `-buttons 0=fit,1=rotation-lock`.
Ids vary by model; unmapped presses are logged with a hint so they can be
discovered. On a **SpaceMouse Compact**, 0 is the left button and 1 the right
(measured).

| Action | Effect |
|---|---|
| `fit` | Frame the model, keeping the camera's orientation. Computed here, so it needs no application support. |
| `menu` | Send `V3DK_MENU` via `events.keyPress`/`keyRelease`. Applications that do not implement it answer `CALLERROR` and nothing happens. |
| `dominant-axis` | Toggle dominant-axis filtering, to suppress cross-talk for a deliberate gesture. |
| `rotation-lock` | Toggle rotation, leaving pan and zoom. |
| `none` | Mapped to nothing, and silent. |

## Tests

```sh
go test ./...
```

- `internal/navlib` — quirks derivation across versions, both matrix layouts
  (including that misreading a layout does *not* silently produce a plausible
  translation), rotation and orbit identities, canonical round trips.
- `internal/spacenav` — wire decode including the non-obvious `x, z, y` slot
  order, button press/release, latching, dominant-axis detection and its
  settle behaviour, drop-oldest buffering, rest detection against a real
  device's residual offset, and noise-floor measurement — all against a fake
  AF_UNIX daemon.
- `internal/nav` — deadzone against the real device's resting noise, curve
  shaping, object vs camera mode direction, the device-to-camera axis mapping,
  rotation preserving distance from the pivot, frame-rate independence,
  dominant-axis filtering, orthographic zoom (scaling extents rather than
  dollying, and returning exactly to the start on a round trip), and
  suppressing rotation on a pinned view.
- `internal/server` — a fake 3DconnexionJS client drives the full handshake;
  covers property exchange, `CALLERROR` handling, pre-0.5 client detection,
  the discovery endpoint with CORS, and that orbit rotates without translating.

## Not yet built

### Functional gaps

- **Tuning.** Every constant in `nav.DefaultConfig` is derived, not felt.
  Speeds, curve and deadzone all want a pass with the device in hand.

### Plumbing
- Certificate generation and NSS injection inside the binary; currently the
  shell scripts do it (doc 08).
- Packaging.
