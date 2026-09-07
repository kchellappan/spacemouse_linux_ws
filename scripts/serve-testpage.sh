#!/usr/bin/env bash
# Serve the repository over plain HTTP so the test harness can load the SDK's
# 3dconnexion.module.js. The SDK readme notes samples must not be loaded from
# file:// — application command export breaks.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${1:-8080}"

echo "serving $ROOT on http://localhost:$PORT"
echo "test harness: http://localhost:$PORT/web/testpage/"
cd "$ROOT"
exec python3 -m http.server "$PORT" --bind 127.0.0.1
