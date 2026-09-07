#!/bin/sh
# Stop enabling the unit for future logins.
#
# Running instances belong to user sessions that root cannot address reliably,
# so they keep going until logout. Users who want it gone now can run:
#   systemctl --user disable --now spacemouse-bridge
set -e

if [ -d /run/systemd/system ]; then
	systemctl --global disable spacemouse-bridge.service >/dev/null 2>&1 || true
fi

exit 0
