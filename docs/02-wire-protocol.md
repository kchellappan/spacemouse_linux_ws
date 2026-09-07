# Wire Protocol

Transport between the browser page and the driver. Everything here is either
read from `3DconnexionJS/src/3dconnexion.js` (v0.8.1) or observed on the wire.

## Endpoints

Both are hardcoded in the client library — `3dconnexion.js:103` sets
`this.host = "127.51.68.120"` and `:98` sets `this.defport = 8181`. **The host
cannot be changed**, which is what forces the certificate design in doc 04.

### 1. Discovery (HTTPS XHR)

```
GET https://127.51.68.120:8181/3dconnexion/nlproxy
Accept: application/json; charset=utf-8
Origin: https://cad.onshape.com
→ 200 {"port": 8181, "version": "1.4.8.21486"}
```

Cross-origin, so `Access-Control-Allow-Origin` is required. Echo the request's
`Origin`. The client only attempts the WebSocket if this succeeds
(`3dconnexion.js:225`).

The returned `port` is honoured — observed: we answered `8181` and the browser
came back to 8181.

### 2. WAMP socket

```
wss://127.51.68.120:<port>/          subprotocol: "wamp"
```

Path is `/`. Confirmed by observation and matched by `spacenav-ws`'s
`@app.websocket("/")`.

`wss://` is mandatory — the host page is HTTPS, so `ws://` is blocked as mixed
content.

## WAMP v1

Autobahn|JS 0.8.2, bundled in the SDK. Messages are JSON arrays whose first
element is the type:

| ID | Type |
|---|---|
| 0 | WELCOME |
| 1 | PREFIX |
| 2 | CALL |
| 3 | CALLRESULT |
| 4 | CALLERROR |
| 5 | SUBSCRIBE |
| 6 | UNSUBSCRIBE |
| 7 | PUBLISH |
| 8 | EVENT |

Note the client never sends a HELLO; the server speaks first.

## Handshake

Observed capture (`spacenav-ws/additional/har_captures/har_part_handshake.json`,
April 2025, against the 3dconnexion.com online sample):

```
rece [0,"fRG6E9mlnokuL4G2",1,"Nl-Proxy v1.4.0.17559 Copyright 2013-2020 3Dconnexion…"]
send [1,"3dx_rpc","wss://127.51.68.120/3dconnexion#"]
send [1,"3dconnexion","wss://127.51.68.120/3dconnexion"]
send [1,"self","https://3dconnexion.com/technical_support/web_threejs.html"]
send [2,"0.3uy1h4l77rf","3dx_rpc:create","3dconnexion:3dmouse","0.3.11"]
rece [3,"0.3uy1h4l77rf",{"connexion":"tWSuWzvaxQNE0Bxo"}]
send [2,"0.paydocbsmr","3dx_rpc:create","3dconnexion:3dcontroller","tWSuWzvaxQNE0Bxo",
      {"version":0,"name":"web_threejs.html"}]
rece [3,"0.paydocbsmr",{"instance":3773718827494}]
send [5,"3dconnexion:3dcontroller/3773718827494"]
send [2,"0.yg35qrvwlc","3dx_rpc:update","3dconnexion:3dcontroller/3773718827494",{"focus":true}]
rece [3,"0.yg35qrvwlc",{}]
```

Sequence a server must implement:

1. Accept the socket with subprotocol `wamp`; send `[0, sessionId, 1, serverIdent]`.
2. Consume three PREFIX messages.
3. `3dx_rpc:create` / `3dconnexion:3dmouse` / `<libVersion>` → reply `{"connexion": <id>}`.
4. `3dx_rpc:create` / `3dconnexion:3dcontroller` / `<connexionId>` / `<info>` →
   reply `{"instance": <id>}`. **Keep `info`** — it carries `name`, `version`,
   and (0.5+) `rowMajorOrder`. See doc 03.
5. Handle SUBSCRIBE on `3dconnexion:3dcontroller/<instance>`. Only start
   emitting after this.
6. Handle `3dx_rpc:update` on the controller with `{focus: bool}`.

### URI prefix resolution quirk

Prefix `3dconnexion` = `wss://127.51.68.120/3dconnexion` and the client sends
`"3dconnexion:3dcontroller/" + id`. Naive concatenation yields
`wss://127.51.68.120/3dconnexion3dcontroller/<id>` — **no slash**. That is what
`spacenav-ws` matches on, so it is the real resolved form. Don't "fix" it.

## Server→client RPC: the reverse-RPC trick

WAMP v1 has no server-initiated CALL. The driver works around this by
**publishing an EVENT to the controller topic whose payload is a CALL message**:

```
[8, "3dconnexion:3dcontroller/<id>", [2, "<callId>", "self:read", "", "<property>"]]
[8, "3dconnexion:3dcontroller/<id>", [2, "<callId>", "self:update", "", "<property>", <value>]]
```

The page unwraps this in `onEvent` (`3dconnexion.js:525`), checks
`event[0] === ab._MESSAGE_TYPEID_CALL`, and replies with a bare CALLRESULT via
`session._send()`:

```
[3, "<callId>", <result>]                                     // success
[4, "<callId>", "self:read#generic", "<prop> unknown property"] // unsupported
```

Every property read and write goes through this envelope.

## Focus gating

`create3dmouse` binds `focus`/`blur` on the canvas and pushes
`{focus: true|false}` via `3dx_rpc:update`. Suppress output while unfocused or
you will fight the page.

## Teardown

`delete3dmouse()` issues `3dx_rpc:delete` on `3dconnexion:3dmouse/<connexion>`,
but the three.js sample only calls it from its `ID_CLOSE` command handler
(`web_threejs.html:164`) — **nothing hooks `beforeunload`**. A page refresh is
therefore an abrupt socket close with no graceful delete. Treat socket close as
your teardown signal and keep all per-session state on the connection object.

## Live verification (2026-09-06)

Against `tools/nlproxy-probe.py`, Onshape unpatched on Linux:

```
[21:10:45] GET /3dconnexion/nlproxy  origin=https://cad.onshape.com
[21:10:45] GET /                     origin=https://cad.onshape.com  << WEBSOCKET UPGRADE >>
```

Both requests inside the same second — discovery immediately followed by the
socket upgrade, exactly as `3dconnexion.js:225-260` describes.
