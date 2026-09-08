# spacenavd

Free user-space driver for 6-DoF space mice. GPL-3.
<https://github.com/FreeSpacenav/spacenavd> — actively maintained.

Ubuntu noble: `spacenavd 1.2-1` in universe. Also `libspnav0`, `libspnav-dev`,
and the `spnavcfg` GUI tuner.

## Build on it, don't bypass it

spacenavd is the **input layer**; your bridge is the **navigation + transport**
layer. They stack, they are not alternatives. `spacenav-ws` is itself a
spacenavd client.

| | spacenavd | your bridge |
|---|---|---|
| Device I/O, hotplug, grab | yes | — |
| Filtering, calibration, config | yes | — |
| Multi-client arbitration | yes | — |
| Camera / view matrix / pivot | **no** | yes |
| Navigation model | **no** | yes |
| WAMP / WSS transport | **no** | yes |

What you get for free by not reading evdev directly:

- **Device grab.** `grab = true` by default. The config comments are explicit:
  *"some versions of Xorg will use the device to move the mouse pointer if we
  don't grab it."* Read evdev while X also sees the device and the cursor flies
  around the screen.
- **Hotplug** — `hotplug_linux.c`, `dev_usb_linux.c`.
- **Multi-client arbitration.** Blender and your bridge can hold the device
  simultaneously. Direct evdev reads alongside a running spacenavd means both
  see every event with no coordination.
- **Filtering you would otherwise hand-roll** — see config below.
- **`spnavcfg`**, a GUI tuner. Users adjust sensitivity without you building a
  settings UI.
- Device database, LED control, serial devices, and the legacy X11 protocol.

## Two client protocols

`src/proto_unix.c` and `src/proto_x11.c`.

### Native libspnav — `/var/run/spnav.sock`

AF_UNIX, versioned. Used by Blender, FreeCAD, OpenSCAD, and by `spacenav-ws`.
**This is the one you want.**

### X11 ClientMessage ("magellan" / 3dxsrv compatibility)

spacenavd is a drop-in replacement for 3Dconnexion's discontinued proprietary
Linux daemon, so software built against the old magellan SDK works without
recompilation. The README names **Maya** and **Houdini**.

## Wayland

**spacenavd works fine under Wayland.** The commonly repeated claim that it
"requires X11" is wrong, and conflates the daemon with its legacy compatibility
protocol.

Evidence:

- Device I/O is kernel-level (evdev/USB). No display server involved.
- `configure` states plainly: `x11: X11 support, needed for 3dxsrv
  compatibility (default: on)`, and `--disable-x11` warns only about
  applications written for the proprietary driver.
- `proto_unix.c` contains three `USE_X11` references — **all three** are in
  `REQ_SCFG_KBMAP` / `REQ_GCFG_KBMAP`, i.e. keysym *name lookup* for the
  button-to-key config. Event delivery touches no X11 code.
- `kbemu` has a **uinput** backend (`kbemu_uinput.c`, selected when
  `!cfg.kbemu_use_x11`) added precisely so button-to-key emulation works
  without an X server.

What *is* affected: the magellan/3dxsrv path, i.e. Maya and Houdini. Under
XWayland it can work, but `DISPLAY` detection and `.Xauthority` cause trouble
(spacenavd discussion #37). In a pure-Wayland session with no X server it cannot
work at all.

**Irrelevant to this project** — the bridge uses the AF_UNIX socket, and the
browser reaches it over WSS on loopback. Neither leg involves a display server.

## Configuration (`/etc/spnavrc`)

From `doc/example-spnavrc`:

- `sensitivity`, `sensitivity-translation`, `sensitivity-rotation`, and per-axis
  `sensitivity-{translation,rotation}-{x,y,z}`
- `dead-zone`, per-axis variants, and per-device-axis `dead-zoneN`
- `invert-rot`, `invert-trans` (combinations of `x`, `y`, `z`), `swap-yz`
- `axismapN`, `bnmapN` — axis and button remapping
- `bnactN` — button actions: `sensitivity-up`, `sensitivity-down`,
  `sensitivity-reset`, `disable-rotation`, `disable-translation`,
  **`dominant-axis`** (mirrors a navlib feature)
- `kbmapN` — button → keysym
- `led`, `serial`, `device-id`, `grab`
- **`repeat-interval`** — see below

### Event cadence: measured, not assumed

Measured on a **SpaceMouse Compact** (2026-09-06), spacenavd emits motion
events at a steady **125 Hz (8 ms)** while the puck is deflected, and it
repeats identical values rather than going quiet:

```
21:53:35.487 motion x=-9 ry=2 rz=-4 periodMs=8
21:53:35.495 motion x=-9 ry=2 rz=-4 periodMs=8
21:53:35.503 motion x=-9 ry=2 rz=-4 periodMs=8   <- unchanged values, still streaming
```

So for this device the starvation problem that `repeat-interval` exists to fix
does **not** occur:

```
# repeat-interval = -1
# Non-deadzone events are repeated every so many milliseconds (-1 to disable).
# Set it to something like 250 if you find that your apps stop moving the view
# while you hold the puck/ball at a fixed off-center position
```

The opposite problem is the real one. **125 Hz is far above display rate**, and
each frame costs a read plus a write round trip to the browser. Driving one
frame per event would mean ~125 round trips per second per axis change.

The fix is the same either way, and it is what `spacenav.Client` is built for:
**latch the deflection and drive navigation from your own frame timer** at
display rate. `State()` returns the most recent value; the event channel is for
buttons and for detecting the return to centre. This also matches how the real
driver behaves — a continuous frame loop, not one frame per device report.

Keep `repeat-interval` in mind for other models, but do not assume it is needed.

## Axis mapping is per-device — measure it

Which physical motion drives which wire axis, and with what sign, depends on
the device model and on spacenavd configuration (`invert-rot`, `invert-trans`,
`swap-yz`, `axismapN`). Do not hardcode it from documentation.

`spacemouse-bridge -calibrate` measures it. It first learns the device's full
deflection so its threshold is not a guess, then walks the six degrees of
freedom, retrying any gesture where a second axis reached more than a third of
the dominant one. It warns when two gestures land on the same axis.

**Cross-talk is easy to produce accidentally and hard to notice.** A casual
sideways push on a Compact produced `x=-9` alongside `ry=2, rz=-4`; a first
calibration attempt at "slide right" came back as `-rz` (peak 53, next 23) —
a roll, not a translation. Push firmly and purely, and treat a "mixed" reading
as a signal to redo the gesture rather than as data.

### The puck does not rest at zero

Measured on a SpaceMouse Compact: released, with no hand on it, the device
settles at a residual offset rather than exact zero.

```
{X:6 Y:-2 Z:-11 RX:9 RY:4 RZ:-6}     full deflection = 279
```

That is ~4% of full scale on the worst axis, and spacenavd's default
`dead-zone = 2` does not remove it. Two consequences:

1. **Never test for exact zero** to decide whether the user let go. Use
   `Motion.AtRest(threshold)` with a threshold above the measured floor;
   `Motion.Zero()` exists for wire-level checks, not for release detection.
2. **The navigation model needs its own deadzone.** A resting offset of 11
   would otherwise drift the camera continuously while the user is not
   touching the device.

`-calibrate` measures the floor first (hand off the puck) and reports it, and
warns if it exceeds 10% of full scale — at which point raising `dead-zone` in
`/etc/spnavrc` is the better fix than compensating in software.

### Do not drive state changes from the event channel

The device streams at 125 Hz. Any consumer that watches the event channel for
a state change — the puck re-centring, say — races the buffer: under load a
backlog of stale events is served while the event actually wanted is gone.
`spacenav.Client` mitigates this by discarding the *oldest* event when its
buffer is full rather than the newest, but the robust answer is to read
`State()`, which is always current. `WaitCentred` and `DetectDeflection` both
poll the latch for exactly this reason.

## Event format

32 bytes, eight `int32`:

```
[0] type:  0 = motion, 1 = button press, 2 = button release
motion:  [1..6] = axes, [7] = period in ms
button:  [1] = button id
```

### Axis map, measured on a SpaceMouse Compact (2026-09-06)

`-calibrate` produced this, in a **Z-up right-handed** frame
(X = right, Y = away toward the screen, Z = up):

| Slot | Axis | Positive means |
|---|---|---|
| `[1]` | X | cap slides RIGHT |
| `[2]` | **Z** | cap lifts UP |
| `[3]` | **Y** | cap slides AWAY, toward the screen |
| `[4]` | RX | far edge LIFTS (so tipping forward is negative) |
| `[5]` | **RZ** | COUNTER-clockwise seen from above |
| `[6]` | **RY** | right edge goes DOWN |

**The slot order is `(x, z, y)` for rotation as well as translation.** This is
the trap: applying the swap to translations only — which is what a first
reading of existing implementations suggests — silently exchanges **yaw with
roll**. Calibration caught it, because "twist clockwise" landed on the axis
labelled RY and "tip right" on RZ. Rotations would have felt wrong in a way
that is very hard to debug by feel.

Derivation from the raw readings, by the right-hand rule:

- about X, positive curls Y→Z (away→up), so the far edge lifts; tipping
  forward measured negative on slot 4. Consistent: slot 4 is X.
- about Z, positive curls X→Y (right→away) = counter-clockwise from above;
  clockwise measured negative on slot 5. So slot 5 is **Z**, not Y.
- about Y, positive curls Z→X (up→right), tipping the top right so the right
  edge drops; that measured positive on slot 6. So slot 6 is **Y**, not Z.

### Rotation cross-talk is technique, not hardware

A first calibration pass showed ~70% cross-talk on both tipping gestures:

```
pitch forward    mixed (71% cross-talk)
roll right       mixed (70% cross-talk)
```

A second pass, same device, same build, came back **clean on all six**. So the
coupling is a function of how deliberately the cap is tipped, not an inherent
property of the device. Two consequences:

- A "mixed" reading during calibration means *redo the gesture*, not *the
  device is like that*.
- Dominant-axis filtering (`nav.Config.DominantAxis`, or `bnactN =
  dominant-axis` in spnavrc) is worth offering as an option, but it is not
  required to make rotation usable. Do not enable it by default.

The confirmed map above is the one produced by the clean pass, and it is
pinned by `TestCalibratedAxisMap`.

## Linux desktop applications

Unlike the web case, desktop apps consume **raw 6-DoF deltas and compute their
own camera**. Transform ownership is inverted relative to doc 01.

| App | Path |
|---|---|
| Blender | native libspnav, out of the box; the README's reference client |
| FreeCAD | native |
| OpenSCAD | native |
| Maya, Houdini | X11 magellan compatibility |

Others come up in discussion (Solvespace, MeshLab, ParaView, KiCad, ROS
`spacenav_node`) but were not verified; support quality varies.

**Practical use:** install Blender plus spacenavd as a known-good reference. If
the puck drives Blender correctly but your bridge misbehaves, the bug is in your
navigation math, not your device handling.

## Upstream wants this work

From the spacenavd README, verbatim:

> There's an effort to create a websocket layer, to allow web applications to
> receive 6dof input from spacenavd. This is currently hosted separately at:
> https://github.com/RmStorm/spacenav-ws
>
> We're looking for someone interested to take over maintainance of that
> interface, in order to improve it past the proof of concept stage, and
> make it a part of the free spacenav project.

## Output saturates at ±350

Measured on a SpaceMouse Compact, 2026-09-07, from a 1176-sample `-read-mouse`
dump taken while deliberately pushing every axis to its stop:

| | max + | max − |
|---|---|---|
| `x`, `y`, `z` | 350 | 350 |
| `rx`, `ry`, `rz` | 350 | 350 |

All six axes, both directions, exactly 350. It is a clamp rather than a spring
limit: 650 of 7056 axis readings (9.2%) sit at exactly 350 — the second most
common value after zero — while the next magnitudes down (349, 348, 346, 341)
are far rarer. That is a clipping plateau, not a distribution.

**Where 350 comes from.** Not spacenavd, and not the spring. It is the value
the device's own HID report descriptor declares, once, covering all six axes:

```
0x16, 0xA2, 0xFE,   //  Logical Minimum (-350)
0x26, 0x5E, 0x01,   //  Logical Maximum (350)
```

spacenavd then applies sensitivity as a bare multiply with no clamp
(`src/event.c`):

```c
inp->val = (int)((float)inp->val * cfg.sensitivity * axis_sens);
```

so:

```
full scale = 350 x cfg.sensitivity x per-half or per-axis sensitivity
```

`sensitivity` defaults to 1.0 and the measured machine has no `/etc/spnavrc`,
which is why it saturates at exactly 350. The measurement and the descriptor
close on each other.

Sources: the HID descriptor is parsed in the [udev-hid-bpf SpaceNavigator case
study](https://udev-hid-bpf-bentiss-648236040b7c508ff54e7bc3510428536d7fd37b91.pages.freedesktop.org/case-study-spacenavigator.html);
the multiply is
[`spacenavd/src/event.c`](https://github.com/FreeSpacenav/spacenavd/blob/master/src/event.c).

**Consequences.**

- Any calibration reading below 350 on this device is under-pushing, not a
  measurement of the hardware. Single-push calibration returned 216 on one run
  and 146 on the next for exactly that reason.
- Two gestures landing on the *identical* peak is therefore evidence the clamp
  was reached. A lone peak below it is only a lower bound. `-calibrate` says
  which it got (doc 10).
- 350 is not universal. `spnavrc` exposes `sensitivity`,
  `sensitivity-translation` and `sensitivity-rotation` as independent knobs,
  plus per-axis variants, so a tuned machine can saturate at different values
  for sliding and tipping. That is why full scale is measured rather than
  assumed, and why translation and rotation carry separate scales.
- **No static number is guaranteed to stay right.** `spnavcfg` changes
  sensitivity by talking to the daemon rather than editing the file, and
  `bnactN = sensitivity-up` / `sensitivity-down` puts it on a button. Reading
  `spnavrc` would therefore be an improvement on assuming 1.0, but not a
  guarantee. Showing live axis magnitudes, so a user can see saturation
  directly, is the durable answer — a job for the status UI.
- Shipping a measured calibration in the package would be ceremony: it would
  record 350, which is what the built-in default already is.
