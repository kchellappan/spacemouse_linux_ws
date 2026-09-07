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
  "version": 1,
  "navMode": "object",
  "fullScale": 216,
  "noiseFloor": 6,
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
| `fullScale` | Device magnitude at full deflection. Measured by `-calibrate`. |
| `noiseFloor` | Resting offset, recorded for diagnosis. Nothing reads it yet. |
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

## Calibration writes to it

`-calibrate` now saves what it measured — full scale, noise floor — and widens
the dead zone if the resting offset demands it, then says so:

```
    Full deflection 216, resting noise floor 6.

    Raising the dead zone from 0.060 to 0.042: ...
    saved to /home/you/.config/spacemouse-bridge/config.json
    Restart the service to pick this up:
      systemctl --user restart spacemouse-bridge
```

`-selftest` warns when the file is missing, or when it exists but full scale is
still the built-in value, because an unmeasured full scale costs sensitivity
silently and nothing else would ever mention it.

## When the web UI arrives: the API rules

The UI will be served from the origin that already exists — the bridge's own
`https://127.51.68.120:8181` — and CAD sites already talk to that origin. Any
endpoint that *changes* settings therefore needs all three of these, decided
here so the UI does not have to rediscover it:

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

Today the exposure is limited: the only thing a page can do over WAMP is drive
its own camera. A mutating API changes that, and retrofitting these is harder
than designing them in.
