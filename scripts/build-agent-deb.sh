#!/usr/bin/env bash
# Build qubes-air-agent_<version>_amd64.deb.
#
# The build runs in Docker so the .deb does not depend on the developer's
# machine having dpkg-deb — which, on the Apple Silicon hosts this is developed
# on, it does not. See packaging/agent-deb/Dockerfile for why nothing is
# emulated despite the target being amd64.
#
# Usage:
#   scripts/build-agent-deb.sh                 # version from git describe
#   VERSION=1.2.3 scripts/build-agent-deb.sh   # explicit version
#   OUT_DIR=/tmp/x scripts/build-agent-deb.sh  # somewhere other than dist/
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${OUT_DIR:-${REPO_ROOT}/dist}"
DOCKERFILE="${REPO_ROOT}/packaging/agent-deb/Dockerfile"

die() {
    echo "error: $*" >&2
    exit 1
}

command -v docker >/dev/null 2>&1 || die "docker is required but not on PATH"
docker info >/dev/null 2>&1 || die "docker is installed but the daemon is not reachable"

# Turn whatever git says into something dpkg will accept.
#
# This is not cosmetic. A Debian version must begin with a digit and may only
# contain [A-Za-z0-9.+~:-]. `git describe --always` on a repository with no tags
# returns a bare commit hash, which begins with a letter roughly six times out
# of sixteen — so a build that worked yesterday fails today with a version
# parsing error for no reason the developer changed. Normalising up front makes
# that difference invisible instead of intermittent.
sanitize_version() {
    local raw="$1" v
    v="${raw#v}"                                     # v1.2.3 -> 1.2.3
    v="$(printf '%s' "$v" | tr -c 'A-Za-z0-9.+~' '+')" # '-' and friends -> '+'
    case "$v" in
    [0-9]*) ;;
    *) v="0.0.0+${v}" ;;                             # bare hash starting with a letter
    esac
    printf '%s' "$v"
}

if [[ -n "${VERSION:-}" ]]; then
    RAW_VERSION="$VERSION"
else
    git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1 \
        || die "not a git repository and no VERSION set"
    RAW_VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty)"
fi
DEB_VERSION="$(sanitize_version "$RAW_VERSION")"

# The same string goes into the control file and into main.buildVersion, so that
# `dpkg -l qubes-air-agent` and `qubes-air-agent --version` cannot disagree.
# An operator comparing "what is installed" against "what is running" is usually
# doing so because something is already wrong; two spellings of the same version
# would send them chasing a mismatch that is not real.
DEB_NAME="qubes-air-agent_${DEB_VERSION}_amd64.deb"

echo ">>> version    : ${DEB_VERSION}$([[ "$DEB_VERSION" != "$RAW_VERSION" ]] && echo "  (from ${RAW_VERSION})")"
echo ">>> output     : ${OUT_DIR}/${DEB_NAME}"

mkdir -p "$OUT_DIR"

# --output type=local extracts the scratch stage's contents. Without it the .deb
# would only exist inside an image layer and would have to be coaxed out with a
# throwaway container.
DOCKER_BUILDKIT=1 docker build \
    --file "$DOCKERFILE" \
    --target artifact \
    --build-arg "VERSION=${DEB_VERSION}" \
    --output "type=local,dest=${OUT_DIR}" \
    "$REPO_ROOT"

DEB_PATH="${OUT_DIR}/${DEB_NAME}"
[[ -f "$DEB_PATH" ]] || die "build reported success but ${DEB_PATH} is missing"

echo
echo ">>> sha256     : $(shasum -a 256 "$DEB_PATH" | cut -d' ' -f1)"
echo
echo "The SHA256 above is the only integrity control between the artifact store"
echo "and the guest: uploads are unauthenticated and delivery is plain HTTP, so"
echo "pin this value in the cloud-init identity document, which travels a"
echo "trusted path. Do not let a guest fetch this package without it."
