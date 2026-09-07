#!/usr/bin/env bash
# Install the local CA into every browser NSS store found for this user.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=nss-common.sh
source "$HERE/nss-common.sh"

CA="${1:-$HERE/../certs/ca.pem}"
[[ -f "$CA" ]] || { echo "no CA at $CA — run scripts/dev-certs.sh first" >&2; exit 1; }

require_certutil
warn_if_browsers_running

found=0
while IFS= read -r -d '' dir; do
  found=$((found+1))
  certutil -D -d "sql:$dir" -n "$CERT_NICKNAME" 2>/dev/null || true
  if certutil -A -d "sql:$dir" -n "$CERT_NICKNAME" -t "C,," -i "$CA" 2>/dev/null; then
    echo "installed → $dir"
  else
    echo "FAILED    → $dir" >&2
  fi
done < <(nss_dirs)

echo
if [[ $found -eq 0 ]]; then
  echo "No NSS stores found. Launch your browser once, then re-run." >&2
  exit 1
fi
echo "$found store(s) updated. Restart your browser for it to take effect."
