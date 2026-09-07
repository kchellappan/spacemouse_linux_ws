# Shared NSS trust-store discovery. Sourced by trust-certs.sh / untrust-certs.sh.
#
# Every supported browser keeps a cert9.db; confinement (snap, flatpak) only
# restricts what the *app* sees, not what we can write into its data dir.
# See docs/04-certificates-and-browser-trust.md.

CERT_NICKNAME="SpaceMouse Bridge Local CA"

require_certutil() {
  if ! command -v certutil >/dev/null 2>&1; then
    echo "certutil not found. Install it with:" >&2
    echo "  sudo apt install libnss3-tools" >&2
    exit 1
  fi
}

# Warn if a browser is running: NSS reads its store at startup, so changes made
# now will not be seen, and snap/flatpak keep processes alive after the window
# closes.
warn_if_browsers_running() {
  local running
  running=$(pgrep -a -f 'firefox|chrome|chromium|/app/zen/zen' 2>/dev/null | head -3 || true)
  if [[ -n "$running" ]]; then
    echo "WARNING: a browser appears to be running. NSS is read at startup," >&2
    echo "         so quit all browsers, then re-run this script." >&2
    echo "$running" | sed 's/^/         /' >&2
    echo >&2
  fi
}

# Emit one NUL-terminated NSS directory per entry.
nss_dirs() {
  # Chromium keeps a single shared db and may not have created it yet.
  if [[ ! -f "$HOME/.pki/nssdb/cert9.db" ]]; then
    mkdir -p "$HOME/.pki/nssdb"
    certutil -N --empty-password -d "sql:$HOME/.pki/nssdb" 2>/dev/null || true
  fi

  find "$HOME/.pki" "$HOME/.mozilla" "$HOME/snap" "$HOME/.var/app" \
       -name cert9.db -print0 2>/dev/null |
    while IFS= read -r -d '' db; do
      printf '%s\0' "$(dirname "$db")"
    done
}
