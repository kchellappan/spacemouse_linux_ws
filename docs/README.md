# SpaceMouse on Linux — Investigation Notes

Research notes for building a Linux driver / WebSocket bridge that lets a
3Dconnexion SpaceMouse drive browser-based CAD (primarily Onshape) on Ubuntu.

All findings dated **2026-09-06** unless stated otherwise. Sources are cited
inline. Where something is inferred rather than observed, it says so.

## Contents

| Doc | Topic |
|---|---|
| [01-architecture-and-data-model.md](01-architecture-and-data-model.md) | Who owns the camera transform; the navlib property model |
| [02-wire-protocol.md](02-wire-protocol.md) | WAMP v1 handshake, endpoints, message flow, captures |
| [03-client-library-versions.md](03-client-library-versions.md) | 3dconnexion.js version differences and the matrix-layout trap |
| [04-certificates-and-browser-trust.md](04-certificates-and-browser-trust.md) | TLS requirements, what failed, the working cert model |
| [05-spacenavd.md](05-spacenavd.md) | What spacenavd provides; Linux desktop app landscape |
| [06-spacenav-ws.md](06-spacenav-ws.md) | Prior art: existing Python bridge, its design and gaps |
| [07-onshape-integration.md](07-onshape-integration.md) | Platform sniff (resolved), verification procedure |
| [08-distribution-plan.md](08-distribution-plan.md) | Language, packaging, install flow |
| [09-implementation.md](09-implementation.md) | Code layout, design decisions, how to run and test |

`tools/nlproxy-probe.py` — the throwaway server used to verify browser behaviour.
See doc 07.

## The short version

- The **driver owns the camera transform**, not the web app. The page is a
  property server; the driver reads pose + context, computes a new camera, and
  writes it back. The hard part of this project is the navigation algorithm,
  not the transport. — doc 01
- Transport is **WAMP v1 over WSS** on a hardcoded loopback address, with
  server→client RPCs smuggled inside WAMP EVENT payloads. — doc 02
- **Onshape no longer sniffs `navigator.platform`.** No browser extension or
  userscript is required. Verified across Chromium and Gecko. — doc 07
- **TLS trust is the only real install friction**, and it is solved: a
  per-user CA plus a distinct leaf, injected into NSS with `certutil`.
  Validated on Chrome (deb), Firefox (snap), and Zen (flatpak). — doc 04
- Build on **spacenavd** rather than raw evdev. — doc 05
- Prior art exists (`spacenav-ws`) and upstream is asking for a maintainer. — doc 06
