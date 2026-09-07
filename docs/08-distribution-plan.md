# Distribution Plan

Target: colleagues running Onshape on Ubuntu. Delivery is a `.deb` attached to
GitHub Releases — no configuration management, no internal APT repo, so the
install must be idempotent and self-diagnosing.

## Language: Go

Chosen for distribution properties, which is the binding constraint here:

- one static binary, no libc or Python-runtime coupling
- stdlib TLS and HTTP server
- trivial cross-compilation
- `nfpm` produces a `.deb` from ~20 lines of YAML, no debhelper knowledge
- [mkcert](https://github.com/FiloSottile/mkcert) is Go and BSD-3 — its
  `truststore` package is most of the install layer (doc 04)

**Rust** is an equally good answer if the team already writes it (`axum` +
`rustls` + `cargo-deb`); pick it for existing expertise, not on merit here.

**C** only if upstreaming into FreeSpacenav is a goal — matching the daemon's
language would help, at the cost of hand-rolling WebSocket framing, TLS and
JSON.

The 4×4 matrix work is ~100 lines by hand in any of them; linear algebra
libraries are not a differentiator.

## Architecture

```
spacenavd (system service, from apt)
   └── /var/run/spnav.sock
        └── spacemouse-bridge (user systemd service, static binary)
             ├── https://127.51.68.120:8181/3dconnexion/nlproxy
             └── wss://127.51.68.120:8181/   (WAMP v1, subprotocol "wamp")
                  └── browser — no extension needed
```

No root and no network configuration at runtime: 127.0.0.0/8 already routes to
`lo` (verified — binding `127.51.68.120:8181` succeeds with no IP alias), and
8181 is unprivileged.

## Package contents

```
spacemouse-bridge_x.y.z_amd64.deb        # nfpm
  Depends: spacenavd, libnss3-tools
  /usr/bin/spacemouse-bridge
  /usr/lib/systemd/user/spacemouse-bridge.service
```

`postinst` does the minimum: install files, `systemctl --global enable`. That is
all.

## Certificate work belongs in the user service, not postinst

`postinst` runs **once, as root**, but NSS stores are **per-user** — and at
install time the user may not be logged in, may have no browser profile yet, and
will certainly create new profiles later.

Put it in the user service instead, which already runs in the user's session
with `$HOME`:

```
user service start (every boot, idempotent):
  1. ~/.local/share/spacemouse-bridge/{ca,leaf}.pem present and unexpired?
     else generate (doc 04)
  2. discover NSS profiles by glob; inject the CA where absent
  3. bind 127.51.68.120:8181; connect /var/run/spnav.sock; serve
```

This is simultaneously simpler and safer than a root postinst: the private key
is generated on the machine, owned by that user at mode 600, and never exists
in the `.deb` — nothing sensitive to leak from a public GitHub release. Nothing
enters the system CA bundle. New browser profiles self-heal on next start.

Caveat: injection requires the browser to be **stopped** (doc 04). Running at
login, before browsers start, is the right moment.

## Install flow

```sh
sudo apt install ./spacemouse-bridge_x.y.z_amd64.deb
# log out and back in, or: systemctl --user start spacemouse-bridge
```

That is the whole user-facing procedure.

## selftest

`spacemouse-bridge -selftest` (a flag, to match the rest of the CLI) should
check and report:

- spacenavd running, `/var/run/spnav.sock` connectable
- a device present
- `127.51.68.120:8181` bindable (or held by us, not something else)
- CA and leaf present, unexpired, key mode 600
- per NSS profile found: CA present with `C,,`
- whether any browser is currently running (blocks injection)

With manual installs and no configuration management, this is the difference
between a bug report and a self-service fix.

## Build order

1. **~~Cert trust spike~~** — done, validated across four browser variants (doc 04)
2. **~~Verify Onshape's platform sniff~~** — done, gate is gone (doc 07)
3. **WAMP layer** against the local 0.8.1 three.js sample — you control the
   page, `_3DCONNEXION_DEBUG = true` gives protocol tracing, and a stall is your
   bug rather than an Onshape change
4. **Navigation model** — the hard part; port from `spacenav-ws` (doc 06)
5. **Onshape**
6. **~~Packaging~~** — done; see "What was built" below

## Design decisions to build in from the start

- **Quirks table keyed on the `info` object** from `create 3dcontroller`, so
  matrix layout and feature support are data rather than assumptions (doc 03).
- **Replay tests from captured WAMP sessions** — the HAR captures in
  `spacenav-ws` are the model. An Onshape change should surface as a failing
  test, not a support ticket.
- **Per-connection state only.** Nothing about the view outlives the socket
  (doc 01).
- **Own frame timer**, latching the last deflection, rather than one frame per
  device event (doc 05).

## What was built

Implemented 2026-09-07. The plan above survived contact largely intact; this
section records what actually exists, so the two do not have to be reconciled
by reading code.

```
packaging/
  systemd/spacemouse-bridge.service   user unit, WantedBy=default.target
  deb/postinstall.sh                  systemctl --global enable, nothing else
  deb/preremove.sh                    systemctl --global disable
  nfpm.yaml                           package metadata and file map
scripts/build-deb.sh                  version derivation, build, nfpm invocation
internal/certs/                       CA and leaf generation, NSS trust injection
```

`scripts/build-deb.sh` derives the version from the current git tag, falling
back to `0.0.0~dev.<sha>` off a tag. The `~` matters: in dpkg's ordering it
sorts *before* any release, so a development build never looks newer than the
release it came after. nfpm runs through `go run <pinned>`, which verifies the
download against the Go checksum database and keeps the packaging tool out of
the machine's global state.

### Certificate handling, as implemented

The service does at start, in the user's own session:

1. `certs.Ensure` — generate the CA and leaf if absent, reissue the leaf within
   30 days of expiry. Keys are written 0600 into a 0700 directory.
2. `ensureTrust` — find every NSS store by glob, install the CA where it is
   absent, warn about browsers that are already running.
3. Bind, connect to spacenavd, serve.

All three are idempotent, so this is a no-op on every start after the first.

One correction to the original design: whether a store is trusted is decided by
comparing the *certificate*, not the nickname. Deleting the credential
directory produces a new CA under the same nickname, and a nickname check would
report the stale entry as trusted, skip the install, and leave the browser
rejecting the bridge with nothing to explain why.
`TestTrustedComparesTheCertificateNotTheNickname` pins this.

### Continuous integration

`.github/workflows/ci.yml` runs on every pull request: gofmt, vet, build, the
test suite under `-race`, then a `package` job that builds the `.deb`,
**installs it with apt**, verifies the unit file and `--global` enablement,
runs `-selftest`, and removes it again. Building a package only proves it can
be written; installing it proves the dependencies resolve and the maintainer
scripts run, which is what actually breaks.

`libnss3-tools` is installed in CI so the NSS trust tests run for real rather
than skipping — the trust layer is the part that fails on users' machines.

### Releases

`.github/workflows/release.yml` fires on tags matching `v*` (the scheme is
`v{major}.{minor}`). It calls the CI workflow as a reusable workflow, so a tag
cannot ship something that would have failed a pull request, then publishes a
GitHub Release carrying the same `.deb` artifact CI built and installed. It
refuses to publish if the package version does not match the tag, which is what
a shallow clone or a missing tag would otherwise produce silently.

### Known gaps

- The service exits if the SpaceMouse is absent for more than 30 seconds at
  start, and systemd restarts it every 10 seconds thereafter
  (`StartLimitIntervalSec=0` keeps it retrying instead of giving up). A device
  unplugged *while running* ends the process the same way. In-process
  reconnection would be better than a restart loop.
- amd64 only. `ARCH=arm64 scripts/build-deb.sh` works; nothing publishes it.
- `preremove` cannot stop instances already running in user sessions.

## Notes for a public release

- The bridge is useful beyond Onshape — any site using `3dconnexion.js` works,
  including 3Dconnexion's own samples. Don't whitelist client names the way
  `spacenav-ws` does.
- Upstream FreeSpacenav is actively looking for a maintainer for exactly this
  layer (doc 05). Worth opening a conversation early.
