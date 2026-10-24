#!/bin/bash
# Install/upgrade/failure-path smoke test for the qubes-air-agent .deb.
#
# RUNS INSIDE a debian:bookworm-slim container, driven by
# scripts/test-agent-deb.sh, which mounts the old and new packages read-only.
# Everything here is exercised through the installed package, not the build
# tree: file layout, dependency installation, version reporting, startup
# refusals and conffile preservation across an upgrade.
#
# Usage (inside the container):
#   bash test-install.sh
set -euo pipefail

OLD_DEB=/tmp/old.deb
NEW_DEB=/tmp/new.deb
export DEBIAN_FRONTEND=noninteractive
FAIL=0

note() { printf '\n=== %s ===\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; FAIL=1; }
ok() { printf 'ok: %s\n' "$*"; }

# expect_failure <description> <message-substring> <command...>
expect_failure() {
    local desc="$1" want="$2"
    shift 2
    local out rc
    set +e
    out="$("$@" 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -eq 0 ]; then
        fail "$desc: exit 0, expected failure"
        return
    fi
    if ! printf '%s' "$out" | grep -qF -- "$want"; then
        fail "$desc: output does not mention '$want': $out"
        return
    fi
    ok "$desc (exit $rc)"
}

# dpkg normalizes versions ("-" becomes "+" in the build script's sanitizer) and
# the Docker image excludes /usr/share/doc, so expectations come from the
# archives themselves rather than from hand-written strings.
OLD_VERSION="$(dpkg-deb -f "$OLD_DEB" Version)"
NEW_VERSION="$(dpkg-deb -f "$NEW_DEB" Version)"
[ -n "$OLD_VERSION" ] && [ -n "$NEW_VERSION" ] || { echo "cannot read package versions" >&2; exit 1; }

note "install dependency resolution and package contents"
apt-get update -qq
apt-get install -y -qq openssl xvfb xauth x11-apps >/dev/null
useradd -m -u 1000 desktop
apt-get install -y -qq "$OLD_DEB" >/dev/null

installed="$(dpkg-query -W -f='${Version}' qubes-air-agent)"
[ "$installed" = "$OLD_VERSION" ] || fail "installed version is $installed, want $OLD_VERSION"

for f in /usr/bin/qubes-air-agent \
    /usr/bin/qubes-air-desktop \
    /usr/bin/qubes-desktop-run \
    /usr/lib/qubes-air/desktop-worker.py \
    /usr/lib/qubes-air/xpra-session.py \
    /usr/lib/qubes-air/data-mount.py \
    /lib/systemd/system/qubes-air-desktop.service \
    /lib/systemd/system/qubes-air-agent.service \
    /etc/qubes-rpc/qubesair.Ping \
    /etc/qubes-rpc/qubesair.Exec \
    /etc/qubes-rpc/qubesair.FileCopy \
    /etc/qubes-rpc/qubesair.UnlockData \
    /etc/qubes-rpc/qubesair.RekeyData; do
    [ -e "$f" ] || fail "missing installed file $f"
done
# The image excludes /usr/share/doc from unpacking, so the archive is what can
# be checked for the shipped README.
dpkg-deb -c "$OLD_DEB" | grep -q 'usr/share/doc/qubes-air-agent/README.md' \
    || fail "package does not ship the agent README"
[ -x /usr/bin/qubes-air-agent ] || fail "agent binary is not executable"
command -v python3 >/dev/null || fail "python3 dependency was not installed"
/usr/bin/python3 -c 'from gi.repository import Gio; assert Gio.DesktopAppInfo' || fail "GIO dependency was not installed"
if systemctl is-enabled --quiet qubes-air-desktop.service; then
    fail "desktop broker was enabled without opt-in"
fi
systemd-analyze verify /lib/systemd/system/qubes-air-desktop.service
expect_failure "root desktop broker" "desktop session unavailable" /usr/bin/qubes-air-desktop daemon
expect_failure "unavailable desktop broker" "desktop session unavailable" /usr/bin/qubes-air-desktop menus

version_out="$(/usr/bin/qubes-air-agent --version)"
printf '%s' "$version_out" | grep -qF "$installed" || fail "--version does not report $installed: $version_out"

note "package integrity"
verify_out="$(dpkg -V qubes-air-agent || true)"
unexpected="$(printf '%s\n' "$verify_out" | grep -v '^$' | grep -v '/usr/share/doc/' || true)"
[ -z "$unexpected" ] || fail "dpkg -V reported unexpected changes: $unexpected"

note "startup refusals"
# A real CA file: identity loading fails on an unreadable CA path before any of
# the checks below, which would make every case report the wrong failure.
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=smoke-ca \
    -keyout /tmp/ca.key -out /tmp/ca.pem >/dev/null 2>&1

expect_failure "missing mTLS flags" "mTLS is mandatory" /usr/bin/qubes-air-agent

expect_failure "no identity and no token" "no bootstrap token" \
    /usr/bin/qubes-air-agent --ca /tmp/ca.pem --cert /tmp/agent.pem --key /tmp/agent.key \
    --bootstrap-token /tmp/does-not-exist

printf 'smoke-token' > /tmp/bootstrap-token
expect_failure "missing revocation URL" "revocation configuration" \
    /usr/bin/qubes-air-agent --ca /tmp/ca.pem --cert /tmp/agent.pem --key /tmp/agent.key \
    --bootstrap-token /tmp/bootstrap-token

expect_failure "empty allowlist" "allow is empty" \
    /usr/bin/qubes-air-agent --ca /tmp/ca.pem --cert /tmp/agent.pem --key /tmp/agent.key \
    --bootstrap-token /tmp/bootstrap-token --allow "" --revocation-url https://127.0.0.1:1/pki/revocations

note "conffile preservation across upgrade"
echo '# local operator edit' >> /etc/qubes-rpc/qubesair.Ping
apt-get install -y -qq "$NEW_DEB" >/dev/null
upgraded="$(dpkg-query -W -f='${Version}' qubes-air-agent)"
[ "$upgraded" = "$NEW_VERSION" ] || fail "upgraded version is $upgraded, want $NEW_VERSION"
[ "$upgraded" != "$installed" ] || fail "upgrade did not change the version"
grep -qF '# local operator edit' /etc/qubes-rpc/qubesair.Ping \
    || fail "an operator edit to a conffile was reverted by the upgrade"
new_version_out="$(/usr/bin/qubes-air-agent --version)"
printf '%s' "$new_version_out" | grep -qF "$upgraded" || fail "--version after upgrade does not report $upgraded"

note "installed desktop broker and GIO integration"
/usr/bin/python3 /tmp/desktop-tests/run_desktop_integration.py
note "installed native signature probe"
/usr/bin/python3 -I - <<'PY'
import importlib.util
from pathlib import Path
import tempfile

spec = importlib.util.spec_from_file_location("data_mount", "/usr/lib/qubes-air/data-mount.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with tempfile.TemporaryDirectory() as root:
    disk = Path(root) / "blank.raw"
    with disk.open("wb") as output:
        output.truncate(2 * 1024 * 1024)
    assert module.probe_signature(disk) == "unrecognized"
    try:
        module.probe_signature(Path(root) / "missing.raw")
    except RuntimeError:
        pass
    else:
        raise AssertionError("missing device did not fail closed")
PY
if systemctl is-enabled --quiet qubes-air-desktop.service; then
    fail "upgrade enabled the desktop broker"
fi

note "removal"
dpkg -r qubes-air-agent >/dev/null
[ ! -e /usr/bin/qubes-air-agent ] || fail "binary survived removal"
[ ! -e /usr/bin/qubes-air-desktop ] || fail "desktop binary survived removal"
[ ! -e /usr/lib/qubes-air/desktop-worker.py ] || fail "desktop worker survived removal"

if [ "$FAIL" -ne 0 ]; then
    printf '\nagent package smoke test FAILED\n' >&2
    exit 1
fi
printf '\nagent package smoke test passed\n'
