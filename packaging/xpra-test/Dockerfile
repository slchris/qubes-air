# Run with --platform linux/amd64: the signed upstream packages are amd64.
ARG DEBIAN_IMAGE=debian:trixie-slim
FROM ${DEBIAN_IMAGE}
COPY *.deb packages.sha256 /tmp/xpra-debs/
WORKDIR /tmp/xpra-debs
RUN sha256sum -c packages.sha256
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    /tmp/xpra-debs/*.deb python3-gi-cairo gir1.2-gtk-3.0 python3-dbus dbus-x11 xvfb xauth x11-utils x11-apps \
    && rm -rf /var/lib/apt/lists/* /tmp/xpra-debs
RUN useradd -m -u 1000 -d /data/home desktop && chmod 0700 /data/home
WORKDIR /
RUN xpra --version
