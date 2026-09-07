# spacemouse-linux-ws

> **Built with AI.** Most of this repository — the Go implementation, the
> packaging, and the notes under [`docs/`](docs/) — was written by Claude
> Opus 5 in Claude Code, directed and reviewed by a human. The protocol and
> hardware findings were verified empirically, against a real SpaceMouse and
> real browsers, rather than taken on the model's word; the docs mark what was
> measured separately from what was inferred, and record the occasions where
> the two disagreed.

A Linux bridge that lets a 3Dconnexion SpaceMouse drive browser-based CAD —
primarily [Onshape](https://onshape.com) — on Ubuntu, where 3Dconnexion ships
no driver.

Web CAD apps talk to the 3Dconnexion driver through `3dconnexion.js`, which
opens a WebSocket to a fixed loopback address. This project is the missing
other end of that socket: it reads the puck from `spacenavd`, computes a new
camera pose, and writes it back to the page.

**Status:** working end to end. Six-degree-of-freedom navigation is verified
live in Chrome (deb), Firefox (snap), and Zen (flatpak), and ships as a `.deb`
that installs a systemd user service.

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
cmd/spacemouse-bridge   the binary
internal/spacenav       spacenavd client, axis calibration
internal/nav            the navigation model (device motion -> camera)
internal/wamp           WAMP v1 session
internal/navlib         navlib property model, matrix conventions
internal/server         WSS server, nlproxy endpoint, the live drive loop
internal/certs          CA/leaf generation and NSS trust injection
web/testpage            dependency-free harness driving a wireframe cube
packaging/              systemd unit, deb maintainer scripts, nfpm config
scripts/                build and run helpers
docs/                   what we learned, one file per topic
```

Start with [docs/README.md](docs/README.md) — it indexes nine topic documents
covering the protocol, certificates, spacenavd, Onshape integration, and the
implementation.

## Installing

Download the `.deb` from the
[Releases](https://github.com/kchellappan/spacemouse_linux_ws/releases) page:

```sh
sudo apt install ./spacemouse-bridge_<version>_amd64.deb
```

Then log out and back in, or start it now with:

```sh
systemctl --user start spacemouse-bridge
```

That is the whole procedure. The package pulls in `spacenavd` and
`libnss3-tools` and enables a systemd **user** service; everything per-user —
generating the certificate, installing it into each browser profile — happens
when that service starts, in your own session. Nothing sensitive ships inside
the package, and nothing is added to the system CA bundle.

Restart any browser that was already running: NSS reads its certificate store
once, at startup.

### Tuning

Run this once, with the device plugged in:

```sh
spacemouse-bridge -calibrate
systemctl --user restart spacemouse-bridge
```

It measures your device's full deflection and resting noise and writes them to
`~/.config/spacemouse-bridge/config.json`, along with every other tunable —
speeds, response curve, button mapping. Without it the bridge assumes a typical
device, which on ours cost about a third of the usable range. `-show-config`
prints the effective settings; see
[docs/10-configuration.md](docs/10-configuration.md) for what each one does.

### When something is wrong

```sh
spacemouse-bridge -selftest
```

It checks each link in the chain — spacenavd, the device node, the port, the
certificates and their permissions, and every browser profile it can find —
and says which one is broken and what to do about it.

Other useful flags: `-trust` and `-untrust` install and remove the local CA by
hand, `-read-mouse` dumps the raw device stream, `-calibrate` walks the six
axes and prints the measured axis map, and `-mode orbit` spins the scene with
no hardware attached.

## Building

Requires Go 1.24+ and a running `spacenavd`.

```sh
sudo apt install spacenavd libnss3-tools
go build -o bin/spacemouse-bridge ./cmd/spacemouse-bridge
go test ./...
```

Build the package with `scripts/build-deb.sh`; it lands in `dist/`. CI builds
and installs it on every pull request, so a broken package shows up as a red
check rather than a bad release.

## Running (development)

```sh
scripts/run-dev.sh
```

That builds and runs from the working tree, managing credentials and browser
trust exactly as the packaged service does. Add `-no-auto-trust` to leave the
browser trust stores alone.

To exercise the bridge without Onshape, run `scripts/serve-testpage.sh` and
open `http://localhost:8080/web/testpage/`. That harness loads 3Dconnexion's
`3DconnexionJS` from the vendor SDK, which is **not** included in this repo —
download `3DxWare_SDK_v4-0-6_r22071` from 3dconnexion.com and unpack it at the
repo root.

## License

MIT — see [LICENSE](LICENSE).

### Relationship to the 3Dconnexion SDK

This repository contains no 3Dconnexion code. The 3DxWare SDK is not vendored
here; the test harness loads `3DconnexionJS` from a copy you download yourself,
and the bridge does not depend on the SDK at build or run time.

What *is* derived from it is interface information needed to interoperate:
navlib property names, the loopback address and port the client library
hardcodes, the V3DK button codes, and the version string the real NL-Proxy
reports. These are the identifiers that have to match for a page to talk to a
driver at all, and they are documented in [docs/](docs/) with their sources
cited.

The SDK carries its own licence, which governs your copy of it rather than
this software. Two clauses are worth reading before you build on this: the
grant is limited to integrating with 3Dconnexion hardware, which is what this
does; and it forbids using SDK elements to create or enhance a product that
competes with a 3Dconnexion product. 3Dconnexion ships no Linux driver, so
this fills a gap rather than displacing something they sell — but that is an
observation, not legal advice, and the judgement is yours.
