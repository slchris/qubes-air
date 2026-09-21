#!/bin/bash
# Build and smoke-test the qubes-air-agent .deb in Docker.
#
# Installs an older package, checks the installed layout, version reporting and
# startup refusals, then upgrades to the current build and verifies conffile
# preservation, integrity and removal. See packaging/agent-deb/test-install.sh
# for the container-side steps.
#
# Usage:
#   scripts/test-agent-deb.sh                 # build old + current, then test
#   VERSION=1.2.3 scripts/test-agent-deb.sh   # name the "new" version explicitly
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/qubesair-agent-deb.XXXXXX")"
trap 'rm -rf "$OUT_DIR"' EXIT

die() {
    echo "error: $*" >&2
    exit 1
}

command -v docker >/dev/null 2>&1 || die "docker is required but not on PATH"
docker info >/dev/null 2>&1 || die "docker is installed but the daemon is not reachable"

# A version that sorts below any real build, so installing the current package
# over it is an upgrade no matter what git describe returns.
OLD_VERSION="0.0.1+smoke-old"
NEW_VERSION="${VERSION:-$(git -C "$REPO_ROOT" describe --tags --always 2>/dev/null || echo 0.0.0+smoke-new)}"

shopt -s nullglob
packages_in() { printf '%s\n' "$OUT_DIR"/qubes-air-agent_*_amd64.deb; }

echo "building old package ($OLD_VERSION) and current package ($NEW_VERSION)..."
VERSION="$OLD_VERSION" OUT_DIR="$OUT_DIR" "$REPO_ROOT/scripts/build-agent-deb.sh" >/dev/null
old_packages=()
while IFS= read -r candidate; do
    old_packages+=("$candidate")
done < <(packages_in)
[ "${#old_packages[@]}" -eq 1 ] || die "expected one package after the first build, got ${#old_packages[@]}"
OLD_DEB="${old_packages[0]}"

VERSION="$NEW_VERSION" OUT_DIR="$OUT_DIR" "$REPO_ROOT/scripts/build-agent-deb.sh" >/dev/null
NEW_DEB=""
while IFS= read -r candidate; do
    if [ "$candidate" != "$OLD_DEB" ]; then NEW_DEB="$candidate"; fi
done < <(packages_in)
[ -n "$NEW_DEB" ] || die "could not identify the freshly built package in $OUT_DIR"

echo "testing install/upgrade/failure paths in debian:bookworm-slim..."
docker run --rm \
    -v "$OLD_DEB:/tmp/old.deb:ro" \
    -v "$NEW_DEB:/tmp/new.deb:ro" \
    -v "$REPO_ROOT/packaging/agent-deb/test-install.sh:/tmp/test-install.sh:ro" \
    debian:bookworm-slim \
    bash /tmp/test-install.sh
