#!/usr/bin/env bash
# Remove the local CA from every browser NSS store found for this user.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=nss-common.sh
source "$HERE/nss-common.sh"

require_certutil

while IFS= read -r -d '' dir; do
  if certutil -D -d "sql:$dir" -n "$CERT_NICKNAME" 2>/dev/null; then
    echo "removed → $dir"
  else
    echo "absent  → $dir"
  fi
done < <(nss_dirs)

echo
echo "Done. Restart your browser for it to take effect."
