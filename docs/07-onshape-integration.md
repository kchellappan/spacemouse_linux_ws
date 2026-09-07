# Onshape Integration

## Headline: no browser extension is required

**Onshape no longer gates the SpaceMouse code path on `navigator.platform`.**
Verified 2026-09-06 on Ubuntu, unpatched, in both browser engines.

This removes an entire workstream that earlier planning had assumed:

- no browser extension
- no Chrome Web Store listing or developer account
- no AMO submission or signing
- no Tampermonkey dependency for users
- no enterprise policy channel for extension force-install

Distribution surface reduces to a single `.deb` (doc 08).

## Historical context

`spacenav-ws` ships `additional/onshape-3d-mouse-linux.user.js` (April 2025):

```js
Object.defineProperty(Navigator.prototype, 'platform', { get: () => 'Win32' });
```

Its description states the fake is what causes Onshape to query
`https://127.51.68.120:8181/3dconnexion/nlproxy`. On Linux, Onshape reported
`Linux x86_64`, concluded no driver could exist, and never attempted the
connection — a decision made in Onshape's JavaScript **before any network
traffic**, so no daemon-side behaviour could influence it.

That userscript and its Greasyfork listing are now obsolete. A short PR to
`spacenav-ws` noting this would be a cheap contribution.

## Verification procedure

Reusable whenever Onshape changes. Tool: `tools/nlproxy-probe.py`.

### 1. Generate a certificate

See doc 04. The probe accepts a chain file and a key:

```sh
python3 tools/nlproxy-probe.py fullchain.pem leaf-key.pem
```

### 2. Establish trust

Either inject the CA with `certutil` (doc 04 — preferred, since it also tests
the mechanism the installer will use), or visit `https://127.51.68.120:8181/`
once and click through.

**A cross-origin XHR to an untrusted certificate fails silently with no
prompt** — indistinguishable from the sniff blocking you. Always establish trust
first or you will chase a false positive.

### 3. Open a real Onshape document

A Part Studio or Assembly with a 3D viewport, not the dashboard.

### 4. Read the output

```
[21:10:45] GET /3dconnexion/nlproxy  origin=https://cad.onshape.com
[21:10:45] GET /                     origin=https://cad.onshape.com  << WEBSOCKET UPGRADE >>
```

The `origin=` field identifies the initiator:

- `origin=-` — no `Origin` header: a top-level navigation. You typing the URL,
  or a favicon fetch. **Not** a signal.
- `origin=https://cad.onshape.com` — JavaScript on Onshape's page. This is the
  hit that matters.

`<< WEBSOCKET UPGRADE >>` is the strongest signal: Onshape parsed the discovery
JSON, honoured the port, and proceeded to open the WAMP socket. The probe does
not speak WebSocket so the connection dies there — expected, and it still
confirms the whole chain up to where a real bridge takes over.

### Using a fresh certificate as a clean control

Gecko's `cert_override.txt` pins host:port **plus fingerprint**, and Chrome's
click-through decision is likewise cert-specific. Serving a *newly generated*
certificate therefore invalidates any prior manual exception without touching
browser settings — the cleanest way to prove that trust came from `certutil`
rather than from an earlier click-through.

## Results, 2026-09-06

All unpatched, no manual certificate acceptance, trust injected via `certutil`:

| Browser | Packaging | Result |
|---|---|---|
| Zen (Gecko 155) | flatpak | nlproxy + WebSocket upgrade |
| Chrome | deb | nlproxy + WebSocket upgrade |
| Firefox 147 | snap | nlproxy + WebSocket upgrade |

Onshape gates on neither platform nor engine.

## Observed protocol facts

- Onshape identifies itself as `name: "Onshape"` in the `create 3dcontroller`
  info object (known from `spacenav-ws`'s whitelist).
- It honours the `port` field returned by the discovery endpoint.
- Discovery and socket upgrade occur within the same second.
- It uses the modern matrix layout — translation at flat indices 12–14 (doc 03).

## If Onshape regresses

Should the platform gate return, the fix is the one-line `navigator.platform`
override delivered as a content script. Publish to the Chrome Web Store and AMO
rather than asking users to install Tampermonkey; Zen carries Firefox's app ID
(`{ec8030f7-c20a-464f-9b0e-13a3a9e97384}`) so an AMO-signed extension installs
there too.

Worth raising with Onshape support regardless: `navigator.platform` is a
**deprecated** API, and gating a feature on it is defensible as a bug report.
The correct behaviour is to attempt the connection on all platforms and fail
gracefully when nothing answers — which is what 3Dconnexion's own sample does
(`3dconnexion.js:246` merely logs on XHR error).
