# Architecture and Data Model

## The counterintuitive part: the driver owns the transform

The web application does **not** receive 6-DoF motion data and move its own
camera. It exposes its camera and scene as a set of named properties, and the
**driver** reads them, computes a new camera pose, and writes it back.

The page is a *property server*. The driver is the *property client*.

This is deliberate on 3Dconnexion's part: it is what makes a SpaceMouse feel
identical across every application, and it lets their settings panel change
navigation behaviour without any app cooperation.

**Consequence for this project:** the "driver" is not a transport layer. It is
the navigation algorithm. Reading evdev is the easy 5%.

## Evidence

`3DxWare_SDK_v4-0-6_r22071/web/3DconnexionJS/src/3dconnexion.js`:

- `:123` `clientFnRead`  — properties the driver may read from the app
- `:155` `clientFnUpdate` — properties the driver may write to the app
- `:525` `onEvent` — dispatches inbound driver RPCs against those maps

The native API agrees. `inc/navlib/navlib.h` declares `view_affine_k` with
`eread_write_access` — the navigation library both reads and writes the camera.

Observed on the wire (server→client is `rece`), from a 2020 capture of the
3dconnexion.com three.js sample:

```
rece [8,"…/3dcontroller/3773718827494",[2,"…","self:read","","view.perspective"]]
send [3,"…",false]
rece [8,…,[2,"…","self:read","","view.affine"]]
send [3,"…",[1,0,0,0,0,1,0,0,0,0,1,10,0,0,0,1]]
rece [8,…,[2,"…","self:read","","model.extents"]]
send [3,"…",[-3,-0.75,2.5,0,0.75,5.5]]
rece [8,…,[2,"…","self:update","","motion",true]]
rece [8,…,[2,"…","self:update","","transaction",2829]]
rece [8,…,[2,"…","self:update","","view.extents",[…]]]
rece [8,…,[2,"…","self:update","","view.affine",[0.99999969,-0.00077864,0,…]]]
rece [8,…,[2,"…","self:update","","transaction",0]]
```

Raw axis values never cross the socket.

## Property catalogue

From the two maps in `3dconnexion.js`. Names match `navlib.h` constants exactly,
so the WAMP layer is a thin transport over the native navlib property model.

### Readable by the driver (`clientFnRead`)

| Property | Meaning |
|---|---|
| `view.affine` | Camera world matrix (**see doc 03 for layout**) |
| `view.constructionPlane` | Plane equation; distinguishes 2D/3D ortho projections |
| `view.extents` | View bounding box (orthographic) |
| `view.fov` | Field of view, **radians** |
| `view.frustum` | `[l, r, b, t, near, far]` (perspective) |
| `view.perspective` | bool |
| `view.target` | Look-at point |
| `view.rotatable` | bool — may the driver rotate this view |
| `model.extents` | Model bounding box; drives speed scaling |
| `model.floorPlane` | Plane equation; used by walk mode |
| `model.unitsToMeters` | World-unit → metre conversion |
| `pivot.position` | App's rotation pivot |
| `hit.lookat` | Result of a hit test the driver requested |
| `selection.affine` / `.empty` / `.extents` | Selection state |
| `pointer.position` | Mouse position unprojected onto the near plane |
| `coordinateSystem` | App's axis convention as a matrix |
| `views.front` | Canonical front view |
| `frame.timingSource` / `frame.time` | Client-driven animation (0.8.x) |

### Writable by the driver (`clientFnUpdate`)

| Property | Meaning |
|---|---|
| `motion` | Driver is/isn't navigating — brackets a motion burst |
| `transaction` | `>0` opens a frame, `0` closes it (redraw cue) |
| `view.affine` | **The computed camera.** The main output. |
| `view.extents` | Zoom, in orthographic projections |
| `view.fov`, `view.target` | |
| `commands.activeCommand` | A button-bound command fired |
| `pivot.position`, `pivot.visible` | |
| `hit.lookfrom`, `hit.direction`, `hit.aperture`, `hit.selectionOnly` | Set up a hit test, then read `hit.lookat` |
| `selection.affine` | Move the selection instead of the camera |
| `events.keyPress` / `keyRelease` | V3DK key events |
| `settings.changed` | Profile settings revision counter |

**Unknown properties are tolerated.** A `CALLERROR` reply is normal and the
driver adapts — the 2020 capture is full of `… unknown property` responses while
navigation keeps working. Implement a useful subset first.

## Lifecycle and state

### The driver holds no persistent view state

It seeds from the app. On page refresh the socket drops, a fresh page reports
its own initial camera, and navigation resumes from there. The pre-refresh pose
is not restored, because nobody stored it.

**Implementation trap:** if your bridge caches the matrix and integrates deltas
internally without re-reading, a refreshed page will receive your *stale*
pre-refresh pose on the first nudge and the camera will snap backwards. Keep
view state per-connection so it dies with the socket.

### What does persist

The per-application **profile**, keyed by the `name` string sent at
`create 3dcontroller` — speed, axis inversion, dominant-axis lock, navigation
mode. `navlib.h:522` documents `settings.<PropertyName>` as reading and writing
this profile. Note `settings.changed` is a change counter that is explicitly
*not* persistent across sessions.

### Read cadence

In the 2020 capture the driver reads `view.affine` at the **start** of a motion
burst, then writes frames without re-reading (`view.affine` appears at message
indices 8 and 30 as reads; 56, 64, 70 as writes with no reads between). It
assumes the app applied what it was given.

Whether it re-reads on every motion start or only on focus change could not be
determined from a single capture. `spacenav-ws` sidesteps the question by
re-reading `view.affine` on *every* event — more round-trips, but fully
stateless and therefore trivially correct across refreshes. Start there.

### Frame transactions

`transaction > 0` opens a frame; `transaction === 0` closes it and is the app's
cue to redraw. Coalesce a batch of device events into one transaction rather
than emitting one per event.
