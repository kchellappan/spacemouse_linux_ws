#!/usr/bin/env bash
# Build and run the bridge against the dev certificates.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERTS="$ROOT/certs"

[[ -f "$CERTS/fullchain.pem" ]] || { echo "run scripts/dev-certs.sh first" >&2; exit 1; }

export PATH="$HOME/.local/go/bin:$PATH"
cd "$ROOT"
go build -o "$ROOT/bin/spacemouse-bridge" ./cmd/spacemouse-bridge

exec "$ROOT/bin/spacemouse-bridge" \
  -cert "$CERTS/fullchain.pem" \
  -key  "$CERTS/leaf-key.pem" \
  "$@"
