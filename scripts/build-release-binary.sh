#!/bin/bash
# Build one release binary from console/backend, stamp it with the build
# identity an operator reads back, and refuse to hand out a binary that lost a
# stamp.
#
# Why a script and not a workflow step: the release workflow used to spell the
# three -X flags — and the guard that reads them back — inline for the console.
# Adding a second inline copy for qubes-air-backup is exactly the drift the
# workflow header warns about, and here the drift is silent: -X that matches no
# variable is ignored rather than reported, so two spellings diverge into a
# published binary that says `revision=unknown` with nothing red. Both artifacts
# now go through this one build-and-verify path (G-G5).
#
# Usage:
#   VERSION=v1.2.3 scripts/build-release-binary.sh ./cmd/server dist/qubes-air-console
#   VERSION=v1.2.3 scripts/build-release-binary.sh ./cmd/qubes-air-backup dist/qubes-air-backup
#
# VERSION is required and is NOT re-derived from git here. The release resolves
# it once (the pushed tag, or the workflow_dispatch input) and the same string
# has to reach the binary, the .deb and the release page; a script that
# re-derived it from `git describe` could disagree with the tag it is published
# under.
#
# Cross-compilation is the caller's environment, not this script's: the release
# workflow sets GOOS/GOARCH and CGO_ENABLED=1. CGO is not optional for either
# binary — both link mattn/go-sqlite3, and a CGO_ENABLED=0 build compiles cleanly
# then fails at runtime with `unknown driver "sqlite3"`. Overriding the caller's
# toolchain here would produce something the developer's `make build-backend`
# does not, which is the difference this script exists to remove.
#
# The output path is relative to the repository root unless it is absolute.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="${REPO_ROOT}/console/backend"

# The package whose unexported variables the linker stamps through -X. The same
# literal appears in the Makefile's CONSOLE_VERSION_PKG and in
# cmd/*/version_test.go, and it has to: a rename that reaches only one of them
# leaves the field unstamped with nothing red anywhere.
STAMP_PKG="github.com/slchris/qubes-air/console/internal/buildinfo"

die() {
    echo "error: $*" >&2
    exit 1
}

# The guard's own exit path: the message is what a release engineer greps for in
# a failed run, so it is a fixed prefix and never a warning.
fatal() {
    echo "FATAL: $*" >&2
    exit 1
}

PACKAGE="${1:-}"
OUTPUT="${2:-}"
[ -n "$PACKAGE" ] || die "usage: VERSION=<version> scripts/build-release-binary.sh <package> <output>"
[ -n "$OUTPUT" ] || die "usage: VERSION=<version> scripts/build-release-binary.sh <package> <output>"
[ -n "${VERSION:-}" ] || die "VERSION is required: it is the release version this artifact is published under"

case "$OUTPUT" in
/*) OUT="$OUTPUT" ;;
*) OUT="${REPO_ROOT}/${OUTPUT}" ;;
esac

# The values travel as shell variables and never through the command line: a
# version interpolated into a command string is re-parsed by the shell, and a
# git ref is repository-controlled input.
revision="$(git -C "$REPO_ROOT" rev-parse HEAD)" || die "cannot resolve HEAD in ${REPO_ROOT}"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

ldflags="-s -w"
ldflags="$ldflags -X ${STAMP_PKG}.version=${VERSION}"
ldflags="$ldflags -X ${STAMP_PKG}.revision=${revision}"
ldflags="$ldflags -X ${STAMP_PKG}.buildTime=${build_time}"

mkdir -p "$(dirname "$OUT")"

echo ">>> package   : ${PACKAGE}"
echo ">>> output    : ${OUT}"
echo ">>> version   : ${VERSION}"
echo ">>> revision  : ${revision}"

(cd "$BACKEND_DIR" && go build -trimpath -ldflags="$ldflags" -o "$OUT" "$PACKAGE")

# Informational, not a check: it is what tells a human reading the CI log which
# architecture was produced without unpacking the artifact. `file` is not on
# every minimal image, and its absence must not fail a build.
if command -v file >/dev/null 2>&1; then
    file "$OUT"
fi

# The guard. -X that matches no variable is silently ignored, so read the stamp
# back out of the artifact and fail the release rather than publish a binary
# that cannot say which build it is.
#
# Every field is checked, not just the version, because every field is its own
# -X flag and can therefore be the one that silently missed: a typo in a sibling
# flag (`revisions=`) leaves the version correct and that field unstamped. The
# artifact would then be published with `revision=unknown`, which is exactly the
# question (which commit is this?) the stamp exists to answer.
NAME="$(basename "$OUT")"
version_out="$("$OUT" --version)"
printf '%s\n' "$version_out"

case "$version_out" in
*"version=${VERSION} "*) ;;
*) fatal "${NAME} does not carry the release version ${VERSION}: ${version_out}" ;;
esac
for field in revision build_time; do
    case "$version_out" in
    *"${field}=unknown"*)
        fatal "${NAME} carries no ${field} stamp: ${version_out}"
        ;;
    esac
done
# `tree` has no `unknown`-free guarantee of its own: an unstamped version parses
# to `unknown`, so it is only acceptable as clean or dirty.
case "$version_out" in
*"tree=clean"* | *"tree=dirty"*) ;;
*) fatal "${NAME} carries no tree stamp: ${version_out}" ;;
esac

echo ">>> verified  : ${NAME} carries all four stamp fields"
