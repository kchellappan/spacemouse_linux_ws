#!/usr/bin/env bash
# Build and run the bridge from a working tree.
#
# No certificate setup here: the binary generates its own credentials under
# ~/.local/share/spacemouse-bridge and installs the CA into browser profiles
# at startup, exactly as the packaged service does. Pass -no-auto-trust to
# leave the trust stores alone.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export PATH="$HOME/.local/go/bin:$PATH"
cd "$ROOT"
go build -o "$ROOT/bin/spacemouse-bridge" ./cmd/spacemouse-bridge

exec "$ROOT/bin/spacemouse-bridge" "$@"
