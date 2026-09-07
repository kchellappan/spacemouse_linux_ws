# spacemouse-linux-ws

A Linux bridge that lets a 3Dconnexion SpaceMouse drive browser-based CAD —
primarily [Onshape](https://onshape.com) — on Ubuntu, where 3Dconnexion ships
no driver.

Web CAD apps talk to the 3Dconnexion driver through `3dconnexion.js`, which
opens a WebSocket to a fixed loopback address. This project is the missing
other end of that socket: it reads the puck from `spacenavd`, computes a new
camera pose, and writes it back to the page.

**Status:** working end to end. Six-degree-of-freedom navigation is verified
live in Chrome (deb), Firefox (snap), and Zen (flatpak). Packaging into a
`.deb` is in progress.

## How it works

The page is a *property server*; the bridge is the *property client*. The
bridge reads the camera, pivot, and model extents from the page, integrates
device motion into a new camera matrix, and writes it back — so the bridge
owns the navigation model, not the web app.

Transport is WAMP v1 over WSS on `127.51.68.120:8181`, with server→client RPCs
smuggled inside WAMP `EVENT` payloads. See
[docs/02-wire-protocol.md](docs/02-wire-protocol.md).

TLS is the only real install friction, and it is solved: a per-user CA plus a
distinct leaf certificate, injected into each browser's NSS store with
`certutil`. The CA private key is generated on the user's machine at first run,
is mode 0600, and never leaves it. See
[docs/04-certificates-and-browser-trust.md](docs/04-certificates-and-browser-trust.md).

## Layout

```
src/       Go source (module rooted here)
  cmd/spacemouse-bridge   the binary
  internal/spacenav       spacenavd client, axis calibration
  internal/nav            the navigation model (device motion -> camera)
  internal/wamp           WAMP v1 session
  internal/navlib         navlib property model, matrix conventions
  internal/server         WSS server, nlproxy endpoint, the live drive loop
  internal/certs          CA/leaf generation and NSS trust injection
web/testpage             dependency-free harness driving a wireframe cube
scripts/                 dev certificate and run helpers
docs/                    what we learned, one file per topic
```

Start with [docs/README.md](docs/README.md) — it indexes nine topic documents
covering the protocol, certificates, spacenavd, Onshape integration, and the
implementation.

## Building

Requires Go 1.24+ and a running `spacenavd`.

```sh
sudo apt install spacenavd libnss3-tools
cd src && go build -o ../bin/spacemouse-bridge ./cmd/spacemouse-bridge
go test ./...
```

## Running (development)

```sh
scripts/dev-certs.sh      # generate a CA + leaf under certs/
scripts/trust-certs.sh    # inject the CA into every NSS store found
scripts/run-dev.sh        # build and serve
```

Then open a CAD page. `scripts/untrust-certs.sh` removes the CA again.

To exercise the bridge without Onshape, run `scripts/serve-testpage.sh` and
open `http://localhost:8080/web/testpage/`. That harness loads 3Dconnexion's
`3DconnexionJS` from the vendor SDK, which is **not** included in this repo —
download `3DxWare_SDK_v4-0-6_r22071` from 3dconnexion.com and unpack it at the
repo root.

Useful flags: `-read-mouse` dumps the raw device stream, `-calibrate` walks
through the six axes and prints the measured axis map, `-mode orbit` spins the
scene without hardware.
