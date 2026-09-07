# Client Library Versions and the Matrix-Layout Trap

Different deployments of `3dconnexion.js` behave differently in ways that will
silently corrupt navigation if ignored. The bridge must branch on what the
client announces, not on assumption.

## Known versions

| Deployment | Library version | `name` reported | `info` object |
|---|---|---|---|
| Local SDK (`3DxWare_SDK_v4-0-6_r22071`) | **0.8.1** (dated 2025-01-09) | `WebThreeJS Sample` | `{version: 0.8, name, rowMajorOrder: bool}` |
| 3dconnexion.com online sample | **0.3.11** (2020 capture) | `web_threejs.html` | `{version: 0, name}` — no `rowMajorOrder` |
| Onshape (measured 2026-09-07) | **0.6.0** | `Onshape` | `{version: 0.6, name: "Onshape"}` — column-major, frame timing |

Onshape's version could not be read by fetching its bundle — 3dconnexion.com
and Onshape both return HTTP 403 to non-browser clients (Cloudflare). It was
read instead off the wire, from the `create 3dcontroller` handshake of a live
session against `https://cad.onshape.com`:

```
msg="3dmouse created" connexion=mouse-d5imn6af95 clientLibVersion=0.6.0
msg="3dcontroller created" instance=ctl-gilplq042k client=Onshape \
    clientVersion=0.6 matrixLayout=column-major(12,13,14) frameTiming=true
```

0.6 sits after the v0.5 break, so the quirks table derives column-major from
the version alone, with no `rowMajorOrder` field present — which is the outcome
this document exists to protect. Onshape also drives its own frame clock, so
the bridge suppresses its internal ticker while the client is animating (doc
09).

## The matrix layout problem

`3dconnexion.js`'s changelog header records a **breaking change at v0.5**:

> `01/30/17 MSB v0.5 breaking changes:` mutators renamed `put*` → `set*`; row
> vectors (column major order matrices) became the default; row-major can be
> requested via the `rowMajorOrder` option. Requires NLServer v1.3.0.

Concretely, where the translation lives in the flat 16-element array differs:

| Source | Translation at flat indices |
|---|---|
| 2020 capture (v0.3.11), driver's `view.affine` writes | **3, 7, 11** |
| 0.8.1 three.js sample (`camera.matrixWorld.toArray()`) | **12, 13, 14** |
| `spacenav-ws` (works against Onshape) | 12, 13, 14 |
| Onshape 0.6.0, measured live 2026-09-07 | 12, 13, 14 |

**Do not hardcode.** Read the `info` object captured at `create 3dcontroller`
and drive a per-client quirks table off `name` / `version` / `rowMajorOrder`.

One caveat worth knowing: a single `view.affine` *read reply* in that same 2020
capture came back in the opposite layout from the others. It could not be
explained from one capture. Verify empirically against whichever client you
target rather than trusting a lone sample.

## Feature differences

Present in 0.8.x, absent in 0.3.x:

- **`frame.timingSource` / `frame.time`** — client-driven animation. The 0.8.1
  sample sets `frame.timingSource = 1` in `on3dmouseCreated`, then per frame
  calls `update3dcontroller({frame: {time: now}})`.

  **This has a 60 ms timeout** (`web_threejs.html:135-146`): if the driver does
  not respond in time the sample sets `animating = false` and stops the loop. A
  bridge written for the old push model will make the modern sample stall.

- `commands.tree`, `commands.activeSet`, `images` — button/menu export
- `settings.changed`
- `view.focusDistance`, `pivot.user`

## What has not changed

The transport. WAMP v1 over Autobahn 0.8.2, the same handshake, the same
reverse-RPC-in-EVENT envelope, the same hardcoded `127.51.68.120:8181`. A
correct protocol implementation works across all versions; only the payload
semantics vary.

## Recommended handling

```
on create_3dcontroller(connexion_id, info):
    quirks = lookup(info.name, info.version, info.get("rowMajorOrder"))
    # quirks.translation_offset  -> 3,7,11 | 12,13,14
    # quirks.frame_timing        -> bool
    # quirks.supports_commands   -> bool
```

Unknown clients should default to the modern layout (12/13/14) and log loudly,
since that is what current Onshape and the current SDK both use.
