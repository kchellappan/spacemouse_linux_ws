#!/usr/bin/env bash
# Build the .deb.
#
# Run from anywhere; output lands in dist/. The version comes from the current
# git tag when there is one, so the same script produces a release artifact on
# a tag and a clearly-marked development build everywhere else.
#
#   VERSION=1.2  scripts/build-deb.sh     # override the derived version
#   ARCH=arm64   scripts/build-deb.sh     # cross-build
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Pinned rather than @latest: the packaging tool is part of the build, and
# `go run pkg@version` verifies the download against the checksum database.
NFPM_VERSION="v2.47.0"

ARCH="${ARCH:-amd64}"

if [[ -z "${VERSION:-}" ]]; then
	if tag="$(git describe --tags --exact-match 2>/dev/null)"; then
		# Tags are v{major}.{minor}; Debian versions carry no leading v.
		VERSION="${tag#v}"
	else
		# '~' sorts *before* any release in dpkg's ordering, so a dev build
		# never looks newer than the release it came after.
		VERSION="0.0.0~dev.$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
	fi
fi

export PATH="$HOME/.local/go/bin:$PATH"

echo "building spacemouse-bridge $VERSION ($ARCH)"

rm -rf dist
mkdir -p dist

# CGO off keeps the binary static, so it does not acquire a libc floor that
# the .deb does not declare.
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
	go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
	-o dist/spacemouse-bridge ./cmd/spacemouse-bridge

VERSION="$VERSION" ARCH="$ARCH" \
	go run "github.com/goreleaser/nfpm/v2/cmd/nfpm@$NFPM_VERSION" package \
	--config packaging/nfpm.yaml \
	--packager deb \
	--target dist/

deb="$(ls dist/*.deb)"
echo
echo "built $deb"
dpkg-deb --info "$deb" 2>/dev/null | sed 's/^/    /' || true
echo "  contents:"
dpkg-deb --contents "$deb" 2>/dev/null | sed 's/^/    /' || true
