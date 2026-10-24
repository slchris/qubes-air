#!/bin/bash
# Real Xpra 6.5.5 + kernel peers + broker/GIO window + Go/mTLS PNG exchange.
# No host services, published ports or privileged container are needed.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/qubesair-xpra-test.XXXXXX")"
trap 'rm -rf "$OUT_DIR"' EXIT

# Pins were checked against the upstream signed Bookworm InRelease/Packages.
# The image rechecks all SHA256s before apt can execute any package hooks.
cp "$REPO_ROOT/packaging/xpra-test/packages.sha256" "$OUT_DIR/"
while read -r digest package; do
    [[ "$digest" =~ ^[a-f0-9]{64}$ && "$package" =~ ^xpra-[a-z0-9-]+_6\.5\.5-r0-1_amd64\.deb$ ]]
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --tlsv1.2 --max-time 120 --retry 2 \
        "https://xpra.org/dists/bookworm/main/binary-amd64/$package" -o "$OUT_DIR/$package"
done < "$OUT_DIR/packages.sha256"

docker build --platform linux/amd64 -t qubes-air-xpra-test:6.5.5 \
    --build-arg "DEBIAN_IMAGE=${DEBIAN_IMAGE:-debian:bookworm-slim}" \
    -f "$REPO_ROOT/packaging/xpra-test/Dockerfile" "$OUT_DIR"

(
    cd "$REPO_ROOT/console/backend"
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -tags=xpra_integration \
        -o "$OUT_DIR/xpra-live.test" ./internal/transport/grpc
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$OUT_DIR/qubes-air-desktop" ./cmd/qubes-air-desktop
)
docker run --rm --network none --platform linux/amd64 -e QUBESAIR_DESKTOP_TEST_CONTAINER=1 \
    -v "$REPO_ROOT/remote/tests:/tmp/desktop-tests:ro" \
    -v "$OUT_DIR/xpra-live.test:/tmp/xpra-live.test:ro" \
    -v "$OUT_DIR/qubes-air-desktop:/usr/bin/qubes-air-desktop:ro" \
    -v "$REPO_ROOT/remote/lib/desktop-worker.py:/usr/lib/qubes-air/desktop-worker.py:ro" \
    qubes-air-xpra-test:6.5.5 python3 /tmp/desktop-tests/run_xpra_integration.py
