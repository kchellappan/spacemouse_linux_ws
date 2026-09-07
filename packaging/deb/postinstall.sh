#!/bin/sh
# Enable the user service for every user, present and future.
#
# Deliberately does nothing about certificates. postinst runs once, as root,
# while NSS trust stores are per-user — and at install time the user may not be
# logged in, may have no browser profile yet, and will certainly create more
# profiles later. The service handles all of that at start, in the user's own
# session. See docs/08-distribution-plan.md.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
	systemctl --global enable spacemouse-bridge.service >/dev/null 2>&1 || true
fi

exit 0
