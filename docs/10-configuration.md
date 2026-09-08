# Configuration

Settings live in `~/.config/spacemouse-bridge/config.json`
(`$XDG_CONFIG_HOME/spacemouse-bridge/config.json` when that is set). The bridge
reads it at start; `-config <path>` points somewhere else.

## Why this exists

Before it, every knob was a command-line flag and the packaged unit passed
none of them:

```ini
ExecStart=/usr/bin/spacemouse-bridge -mode drive
```

So `nav.DefaultConfig()` always won — `FullScale: 350`, a typical SpaceMouse
Compact reading and not a measurement of anybody's actual device. `-calibrate`
measured full scale, printed it, and threw it away. A device reading **216** at
full deflection therefore ran against 350 forever, losing roughly a third of
its usable range, with no way to fix it short of hand-editing a systemd unit.

Settings that cannot outlive a process are not settings. This is the
prerequisite for the planned web UI, but it is worth having on its own.

## Precedence

```
explicit flag  >  config file  >  built-in default
```

"Explicit" means the user actually typed the flag. `flag.Visit` reports only
flags that were set, which is the whole reason it is used rather than comparing
values against defaults — otherwise deliberately passing `-full-scale 350`
would be indistinguishable from not passing it, and a calibrated 216 in the
file would be silently overwritten by a flag nobody used.

`-show-config` prints the effective settings after this merge, which is where a
support conversation should start.

## The file

JSON, because the web UI will read and write it as JSON anyway and because
staying on the standard library keeps the single static binary. The cost is
that JSON has no comments, so `Save` always writes **every** field: the file
documents itself by example.

```json
{
  "version": 3,
  "navMode": "object",
  "fullScale": 216,
  "rotationFullScale": 146,
  "noiseFloor": 6,
  "calibratedAt": "2026-09-07T11:25:38-07:00",
  "deadzone": 0.06,
  "curve": 1.6,
  "panSpeed": 0.9,
  "rotateSpeed": 1.6,
  "zoomSpeed": 1.2,
  "dominantAxis": false,
  "enableTranslation": true,
  "enableRotation": true,
  "frameRate": 60,
  "buttons": { "0": "fit", "1": "menu" }
}
```

| Key | Meaning |
|---|---|
| `version` | Schema version, so a later change can migrate rather than guess. |
| `navMode` | `object` (the model follows the cap) or `camera`. |
| `fullScale` | Device magnitude when the cap is **slid** to its limit. Measured by `-calibrate`. |
| `rotationFullScale` | The same when the cap is **tipped or twisted**. Separate because the two do not share a range — see below. |
| `noiseFloor` | Resting offset, recorded for diagnosis. Nothing reads it yet. |
| `calibratedAt` | When `-calibrate` last wrote this file. Absent means never. |
| `deadzone` | Fraction of full scale to ignore. Must exceed the noise floor or the view drifts when idle (doc 05). |
| `curve` | Response exponent. 1 is linear; higher gives finer control near centre. |
| `panSpeed` | Model diagonals per second at full deflection. |
| `rotateSpeed` | Radians per second at full deflection. |
| `zoomSpeed` | Orthographic zoom, e-foldings per second (doc 09). |
| `dominantAxis` | Use only the strongest axis. Off by default (doc 05). |
| `enableTranslation`, `enableRotation` | Gate each half. |
| `frameRate` | Camera updates per second when the bridge drives the clock. |
| `buttons` | Device button id to action: `none`, `fit`, `menu`, `dominant-axis`, `rotation-lock`. |

A **partial** file is fine. Loading unmarshals over the defaults rather than
into a zero value, so keys the file omits keep their default — including
booleans. Writing `{"fullScale": 216}` does not silently disable rotation.

A **malformed** file is an error and the bridge exits. Resetting to defaults
would lose a user's calibration without telling them.

Writes go to a temporary file and are renamed into place, so an interrupted
save cannot leave a half-written file that the next start refuses to parse.

## Schema versions

| Version | Change |
|---|---|
| 1 | Initial. One `fullScale` for the whole device. |
| 2 | Split `rotationFullScale` out of `fullScale`. |
| 3 | Added `calibratedAt`. |

A version 1 file is migrated on load by copying `fullScale` into
`rotationFullScale`, which is what it effectively meant. A version 2 file gets
its `calibratedAt` from the file's own modification time: calibration is the
only thing that writes this file, so the file existing is itself evidence it
ran. Both migrations happen in memory; the file is only rewritten when
something saves it.

## Whether a device has been calibrated is recorded, not inferred

The first version of the "have you calibrated?" check compared `fullScale`
against the built-in default. That is wrong for the most common case there is:
a correctly calibrated stock device measures **exactly** the built-in 350, so
every successful calibration was reported as a missing one, on every service
start and in every `-selftest`. A warning that fires on success teaches people
to ignore warnings.

`calibratedAt` records the fact instead of guessing at it from the values.

## Why full scale is two numbers

The first version measured one full scale from a single opening push — "push
as far as it goes, **any direction**". On one SpaceMouse Compact that returned
**216** on one run and **146** on the next.

`FullScale` is a divisor:

```go
n := float64(v) / fullScale   // then clamped to ±1
```

so a 48% swing in the measurement is a 48% swing in how sensitive the device
feels. At 146 the puck reaches full speed at 42% of the deflection 350
assumed. "Calibrated" cannot mean "whatever I got that time".

The cause was **under-pushing**, not the hardware. A later `-read-mouse` dump
showed every axis of that device clamping at exactly ±350 in both directions
(doc 05), so 216 and 146 were both simply short of the stop, and the built-in
default of 350 had been right all along.

Sliding and tipping can still differ on a tuned machine — `spnavrc` sets
`sensitivity-translation` and `sensitivity-rotation` independently — so the two
are measured and stored separately. On an untuned device they come out equal.

Three fixes, all using data that was already being collected and discarded:

- Full scale now comes from the **six deliberate gestures**, pooled into
  sliding and tipping, rather than from the improvised opening push. That push
  survives only to give gesture detection a provisional threshold.
- Each half is normalised against its own scale. `RotationFullScale` of zero
  falls back to `FullScale`, so an old file behaves exactly as it did.
- `-calibrate` reports whether a number is the **device limit** or a **lower
  bound**. Two gestures reaching the identical peak means the clamp was hit; a
  lone peak below it is just how hard the user pushed. Presenting both as
  equally authoritative is what let 216 and 146 pass without suspicion.

`-calibrate` also now shows the peak per gesture, and reports the ratio when
the two halves differ by 1.4x or more. The absence of those numbers on screen
is why this went unnoticed through two calibration runs.

## Calibration writes to it

`-calibrate` now saves what it measured — full scale, noise floor — and widens
the dead zone if the resting offset demands it, then says so:

```
    GESTURE            AXIS   SIGN   PEAK    CONFIDENCE
    translate right    x      +      216     clean
    ...
    roll right         rz     +      146     clean

    Full deflection: 216 sliding, 146 tipping. Resting noise floor 6.
    They differ by 1.5x, which is why one number for both was
    unreliable. Each half is now normalised against its own.

    saved to /home/you/.config/spacemouse-bridge/config.json
    Restart the service to pick this up:
      systemctl --user restart spacemouse-bridge
```

If a whole half goes undetected, `-calibrate` says so and falls back to the
provisional push rather than saving a zero, which would leave that half numb.

`-selftest` warns when the file is missing, or when it exists but full scale is
still the built-in value, because an unmeasured full scale costs sensitivity
silently and nothing else would ever mention it.

## The API rules

The UI is served from the origin that already exists — the bridge's own
`https://127.51.68.120:8181` — and CAD sites already talk to that origin.

Rules 1 and 3 below are **already in force**, because they turned out to be
needed sooner than expected: the log records which sites connected, so even a
read-only endpoint discloses browsing activity. `/api` is mounted behind a
same-origin check and carries no CORS headers, with tests pinning both.

Rule 2 is in force too, as of the tuning UI:

1. **No CORS headers on `/api`.** `setCORS` currently echoes whatever `Origin`
   it is given, which is required for the discovery endpoint and wrong for
   everything else.
2. **Require `Content-Type: application/json` on writes.** This is the part
   that actually stops the request. CORS governs whether a page may *read a
   response*; a cross-origin `POST` with a simple content type is still
   delivered and executed. A non-simple header forces a preflight, and the
   preflight fails because nothing answers it.
3. **Reject a foreign `Origin` on mutating requests.** Browsers always attach
   it to `POST` and page JavaScript cannot forge it.

All three are tested rather than asserted: a foreign `Origin` gets 403 on both
reads and writes, a write with any simple content type gets 415 before it
reaches the code that would apply it, and `/api` carries no CORS headers.

## Live settings

`PUT /api/settings` applies a change immediately; `?persist=1` also writes the
file. They are separate so a user can feel a slider with the puck in their
hand and still walk away without having changed anything on disk.

The drive loop reads the settings every frame rather than capturing them at
startup, which is what makes a slider change the feel mid-gesture. Buttons
that toggle settings write back through the same holder, so the device and the
page cannot end up describing different states.

Two things the request path has to get right, both found by testing rather
than by reasoning:

- **Merge over what is in force, do not parse into a zero value.** The page
  sends only what changed; parsing fresh would clear everything else. Same
  reason `Load` does it.
- **Copy the button map before decoding.** Assigning the struct shares it, and
  `json.Unmarshal` merges into an existing map rather than replacing it — so
  decoding a *rejected* update still mutated the settings in force,
  permanently, and every later change then failed for a reason the user could
  not see.

Calibration is preserved across every update. It is measured, not chosen, and
the tuning UI round-trips the whole document, so it would otherwise be able to
erase a measurement by sending back what it rendered.
