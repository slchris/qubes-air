"""Real systemd/Xpra HOME checks ONLY inside the disposable encrypted QEMU guest."""
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import subprocess
import time
import urllib.request

from data_mount_support import command, require_guest, unlock

DESKTOP_HOME = Path("/data/home")
UNIT = "qubes-air-desktop.service"
BROKER = Path("/run/qubes-air-desktop-broker/control.sock")
CLIENT = "/usr/bin/qubes-air-desktop"


def install_xpra():
    root = Path("/tmp/qubes-air-desktop-debs")
    root.mkdir(mode=0o700)
    packages = []
    for line in Path("/root/desktop-packages.sha256").read_text().splitlines():
        digest, name = line.split()
        if not re.fullmatch(r"[a-f0-9]{64}", digest) \
                or not re.fullmatch(r"xpra-[a-z0-9-]+_6\.5\.5-r0-1_amd64\.deb", name):
            raise RuntimeError("invalid pinned desktop package")
        path = root / name
        with urllib.request.urlopen("https://xpra.org/dists/trixie/main/binary-amd64/" + name, timeout=60) as source:
            payload = source.read(64 * 1024 * 1024 + 1)
        if len(payload) > 64 * 1024 * 1024 or hashlib.sha256(payload).hexdigest() != digest:
            raise RuntimeError("desktop package digest mismatch")
        path.write_bytes(payload)
        packages.append(str(path))
    assert len(packages) == 6, "complete pinned desktop dependency set required"
    subprocess.run(["apt-get", "install", "-y", "--no-install-recommends", *packages,
                    "python3-gi-cairo", "gir1.2-gtk-3.0", "python3-dbus", "dbus-x11", "xvfb",
                    "xauth", "x11-utils", "x11-apps"], check=True, timeout=400,
                   env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C", "DEBIAN_FRONTEND": "noninteractive"})


def active():
    return command("systemctl", "show", UNIT, "--property=ActiveState", "--value") == "active"


def wait_for_desktop():
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        if BROKER.exists() and active():
            return
        time.sleep(0.1)
    raise RuntimeError("desktop did not become ready")


def refuse_start():
    reset_desktop()
    subprocess.run(["systemctl", "start", UNIT], check=False, timeout=25,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    assert not active(), "invalid HOME started a desktop"
    command("systemctl", "stop", UNIT)
    assert not BROKER.exists(), "failed desktop retained its endpoint"


def reset_desktop():
    for unit in (UNIT, "qubes-air-desktop-home.service"):
        if command("systemctl", "show", unit, "--property=ActiveState", "--value") == "failed":
            command("systemctl", "reset-failed", unit)


def prepare_guest():
    require_guest()
    account = pwd.getpwuid(1000)
    assert account.pw_gid == 1000 and re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", account.pw_name)
    command("usermod", "--home", str(DESKTOP_HOME), account.pw_name)
    command("systemctl", "daemon-reload")
    command("systemd-analyze", "verify", "/etc/systemd/system/" + UNIT)
    # Unlocking needs the running agent/network. An opt-in desktop must not
    # order its not-yet-present mapper before local-fs.target or wait forever.
    assert "local-fs.target" not in command("systemctl", "show", "data.mount", "--property=Before", "--value").split()
    # Pulling in data.mount must not mount an unverified mapper or create HOME.
    refuse_start()
    assert not DESKTOP_HOME.exists()
    command("mkdir", "/data")
    command("mount", "-t", "tmpfs", "-o", "mode=0755", "tmpfs", "/data")
    marker = Path("/data/foreign-marker")
    marker.write_text("foreign mount retained")
    refuse_start()
    assert not DESKTOP_HOME.exists(), "foreign mount was mutated"
    assert marker.read_text() == "foreign mount retained", "foreign mount was replaced or unmounted"
    command("umount", "/data")


def verify_invalid_home(key):
    unlock(key, True)
    # The pinned upstream package enables its own TCP proxy socket at install.
    # Refuse our desktop until deployment explicitly masks that separate path.
    assert command("systemctl", "show", "xpra-server.socket", "--property=ActiveState", "--value") == "active"
    refuse_start()
    assert not DESKTOP_HOME.exists(), "unmasked upstream proxy was accepted"
    command("systemctl", "mask", "--now", "xpra-server.socket", "xpra-server.service")
    assert "14500" not in command("ss", "-ltn"), "upstream TCP proxy is still listening"
    target = Path("/tmp/unrelated-home")
    target.mkdir(mode=0o700)
    DESKTOP_HOME.symlink_to(target, target_is_directory=True)
    refuse_start()
    assert DESKTOP_HOME.is_symlink() and list(target.iterdir()) == []
    DESKTOP_HOME.unlink()
    DESKTOP_HOME.mkdir(mode=0o700)
    refuse_start()
    assert DESKTOP_HOME.stat().st_uid == 0, "foreign owner was changed"
    DESKTOP_HOME.rmdir()
    command("systemctl", "enable", UNIT)
    reset_desktop()
    unlock(key, True)
    wait_for_desktop()
    info = DESKTOP_HOME.stat()
    assert (info.st_uid, info.st_gid, info.st_mode & 0o777) == (1000, 1000, 0o700)


def write_application():
    entries = DESKTOP_HOME / ".local/share/applications"
    entries.mkdir(parents=True)
    for path in (DESKTOP_HOME / ".local", DESKTOP_HOME / ".local/share", entries):
        os.chown(path, 1000, 1000)
    application = DESKTOP_HOME / "home-fixture.py"
    application.write_text('''import json, os, signal, subprocess, time
from pathlib import Path
home = Path.home()
(home / "environment.json").write_text(json.dumps({"home": str(home), "cwd": os.getcwd()}))
marker = home / "persisted.txt"
if not marker.exists():
    marker.write_text("retained encrypted HOME\\n")
def stop(*_):
    with (home / "stop-order.txt").open("w") as output:
        output.write("application stopped while HOME remained mounted\\n")
        output.flush()
        os.fsync(output.fileno())
    raise SystemExit(0)
signal.signal(signal.SIGTERM, stop)
subprocess.Popen(["/usr/bin/xclock", "-name", "air-encrypted-home"])
while True:
    time.sleep(0.1)
''')
    os.chown(application, 1000, 1000)
    entry = entries / "air-home-fixture.desktop"
    entry.write_text("[Desktop Entry]\nType=Application\nName=Encrypted HOME fixture\n"
                     "Exec=/usr/bin/python3 /data/home/home-fixture.py\n")
    os.chown(entry, 1000, 1000)


def verify_application():
    assert "air-home-fixture.desktop" in command(CLIENT, "menus")
    command(CLIENT, "launch", "air-home-fixture.desktop")
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if (DESKTOP_HOME / "environment.json").exists():
            windows = command("env", "DISPLAY=:100", "XAUTHORITY=/run/qubes-air-desktop/Xauthority",
                              "xwininfo", "-root", "-tree")
            if "air-encrypted-home" in windows:
                break
        time.sleep(0.1)
    else:
        raise RuntimeError("fixture application window did not appear")
    environment = json.loads((DESKTOP_HOME / "environment.json").read_text())
    assert environment == {"home": "/data/home", "cwd": "/data/home"}, environment
    assert (DESKTOP_HOME / "persisted.txt").read_text() == "retained encrypted HOME\n"


def stop_mount():
    group = command("systemctl", "show", UNIT, "--property=ControlGroup", "--value")
    assert group == "/system.slice/" + UNIT
    pids = (Path("/sys/fs/cgroup") / group.lstrip("/") / "cgroup.procs").read_text().split()
    assert pids and all(pid.isdigit() for pid in pids)
    command("systemctl", "stop", "data.mount")
    assert not active() and not BROKER.exists()
    assert all(not (Path("/proc") / pid).exists() for pid in pids), "desktop processes survived unmount"
    assert not DESKTOP_HOME.exists(), "HOME fell back to a directory on the system disk"
    command("cryptsetup", "close", "qubesair-data")


def verify_repeat_unlock(key):
    pid = command("systemctl", "show", UNIT, "--property=MainPID", "--value")
    assert pid.isdigit() and pid != "0"
    unlock(key, True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        job = command("systemctl", "show", "qubes-air-desktop-home.service", "--property=Job", "--value")
        if job in ("", "0"):
            assert active() and command("systemctl", "show", UNIT, "--property=MainPID", "--value") == pid
            assert command("systemctl", "show", "qubes-air-desktop-home.service", "--property=Result", "--value") == "success"
            return
        time.sleep(0.1)
    raise RuntimeError("repeat unlock did not complete HOME verification")


def main():
    require_guest()
    install_xpra()
    prepare_guest()
    key = secrets.token_urlsafe(32)
    verify_invalid_home(key)
    write_application()
    verify_application()
    verify_repeat_unlock(key)
    stop_mount()
    unlock(key, True)
    wait_for_desktop()
    assert (DESKTOP_HOME / "stop-order.txt").read_text() == "application stopped while HOME remained mounted\n"
    verify_application()
    stop_mount()
    print("encrypted HOME, real systemd/Xpra, application exit before unmount and persistence passed", flush=True)
    print("QUBES_AIR_DATA_MOUNT_TEST_PASS", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        subprocess.run(["journalctl", "-u", UNIT, "-u", "qubes-air-desktop-home.service",
                        "-u", "data.mount", "--no-pager", "-n", "120"],
                       check=False, timeout=10)
        raise
