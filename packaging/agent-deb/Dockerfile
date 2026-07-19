# syntax=docker/dockerfile:1
#
# Builds qubes-air-agent_<version>_amd64.deb.
#
# Build context is the REPOSITORY ROOT, not this directory: the package needs
# the Go sources under console/backend and the qrexec services under
# remote/qubes-rpc. Drive it through scripts/build-agent-deb.sh rather than
# calling docker build by hand.
#
# There is deliberately no --platform on any stage. The host here is Apple
# Silicon; forcing linux/amd64 would run every stage under qemu emulation for no
# gain, because the two things that must be amd64 are produced by cross-tools
# that do it natively:
#   - the binary, via GOOS/GOARCH, which Go cross-compiles without a toolchain
#   - the Architecture: amd64 field, which is metadata dpkg-deb copies verbatim
# The dpkg-deb stage never executes the packaged binary, so it does not care
# what architecture that binary is.

ARG GO_VERSION=1.25
ARG DEBIAN_SUITE=bookworm

# --------------------------------------------------------------------------
# Stage 1: cross-compile the agent
# --------------------------------------------------------------------------
FROM golang:${GO_VERSION}-${DEBIAN_SUITE} AS build

ARG VERSION=dev

WORKDIR /src

# Module files first so a source-only change does not re-download the graph.
COPY console/backend/go.mod console/backend/go.sum ./
RUN go mod download

COPY console/backend/ ./

# CGO_ENABLED=0 is not an optimisation. With cgo on, the toolchain links against
# the builder's libc, which on this image is the wrong architecture entirely;
# static also means the target image's libc version stops being a variable.
#
# main.buildVersion is the exact variable name in cmd/qubes-air-agent/main.go —
# if it is renamed, -X silently does nothing and the agent reports "dev" forever.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags "-s -w -X main.buildVersion=${VERSION}" \
    -o /out/qubes-air-agent ./cmd/qubes-air-agent

# Fail the build here rather than ship a binary that dies with "exec format
# error" on the target. cloud-init's runcmd swallows that failure, so without
# this check the first sign of trouble is a qube that boots clean with a dead
# agent — precisely the silent failure this packaging exists to prevent.
#
# `go version -m` rather than file(1): it reads the architecture the linker
# actually recorded, and it is part of the toolchain, so the check cannot be
# skipped by a base image that happens not to ship file.
RUN go version -m /out/qubes-air-agent | grep -q 'GOARCH=amd64' \
    || { echo "FATAL: agent binary is not amd64:"; go version -m /out/qubes-air-agent; exit 1; }

# --------------------------------------------------------------------------
# Stage 2: assemble the tree and build the .deb
# --------------------------------------------------------------------------
FROM debian:${DEBIAN_SUITE}-slim AS package

ARG VERSION=dev

WORKDIR /work

COPY --from=build /out/qubes-air-agent pkg/usr/bin/qubes-air-agent
COPY packaging/agent-deb/qubes-air-agent.service pkg/lib/systemd/system/qubes-air-agent.service
COPY remote/qubes-rpc/ pkg/etc/qubes-rpc/
COPY packaging/agent-deb/README.md pkg/usr/share/doc/qubes-air-agent/README.md
COPY packaging/agent-deb/control.in packaging/agent-deb/postinst packaging/agent-deb/prerm packaging/agent-deb/postrm ./meta/

RUN set -eu; \
    install -d pkg/DEBIAN; \
    \
    chmod 0755 pkg/usr/bin/qubes-air-agent; \
    chmod 0644 pkg/usr/share/doc/qubes-air-agent/README.md; \
    chmod 0644 pkg/lib/systemd/system/qubes-air-agent.service; \
    chmod 0755 pkg/etc/qubes-rpc/*; \
    \
    install -m 0755 meta/postinst meta/prerm meta/postrm pkg/DEBIAN/; \
    \
    # Installed-Size is in KiB and describes the unpacked tree, so it has to be
    # measured after the tree is staged and before DEBIAN/ is counted.
    size="$(du -k -s --exclude=DEBIAN pkg | cut -f1)"; \
    sed -e '/^#/d' -e "s/@VERSION@/${VERSION}/" -e "s/@INSTALLED_SIZE@/${size}/" \
        meta/control.in > pkg/DEBIAN/control; \
    chmod 0644 pkg/DEBIAN/control; \
    \
    # Everything under /etc is a conffile by policy, so operator edits to a
    # qrexec service survive an upgrade instead of being silently reverted.
    # Generated from the staged tree so adding a service to remote/qubes-rpc/
    # cannot leave this list stale.
    ( cd pkg && find etc -type f -printf '/%p\n' | sort ) > pkg/DEBIAN/conffiles; \
    chmod 0644 pkg/DEBIAN/conffiles; \
    \
    # md5sums lets `dpkg -V qubes-air-agent` detect a tampered binary. On a host
    # whose only integrity control upstream is a SHA256 pin in cloud-init, a
    # second local check after install is worth the two lines. Conffiles are
    # excluded per policy — an operator editing one is expected, and listing
    # them here would make `dpkg -V` report that edit as damage.
    ( cd pkg && find . -type f ! -path './DEBIAN/*' ! -path './etc/*' -printf '%P\n' | sort \
        | xargs -r md5sum ) > pkg/DEBIAN/md5sums; \
    chmod 0644 pkg/DEBIAN/md5sums

# --root-owner-group instead of relying on the container's uid: it pins every
# entry to root:root in the archive, so the package installs the same way no
# matter who or what built it.
RUN set -eu; \
    install -d /out; \
    dpkg-deb --root-owner-group --build pkg "/out/qubes-air-agent_${VERSION}_amd64.deb"; \
    dpkg-deb -I "/out/qubes-air-agent_${VERSION}_amd64.deb"; \
    dpkg-deb -c "/out/qubes-air-agent_${VERSION}_amd64.deb"

# --------------------------------------------------------------------------
# Stage 3: the artifact, and nothing else
# --------------------------------------------------------------------------
# scratch so that `docker build --output type=local` writes exactly one file to
# the destination directory instead of an entire root filesystem.
FROM scratch AS artifact
COPY --from=package /out/ /
