#!/usr/bin/python3
"""Fixed unprivileged Xpra session; systemd owns its runtime directories/cgroup."""
import os
from pathlib import Path
import pwd
import signal
import socket
import stat
import subprocess
import sys
import threading
import time

UID = 1000
RUNTIME = Path("/run/qubes-air-desktop")
BROKER = Path("/run/qubes-air-desktop-broker")


def check_versions():
    import xpra
    import gi
    if xpra.__version__ != "6.5.5" or tuple(gi.version_info) < (3, 44, 2):
        raise RuntimeError("unsupported Xpra or PyGObject version")


def session_environment(account, bus):
    home = account.pw_dir
    if account.pw_uid != UID or not home.startswith("/") or str(Path(home)) != home or home == "/" \
            or ".." in Path(home).parts:
        raise RuntimeError("invalid desktop home")
    if not bus.startswith("unix:path=/") or any(char in bus for char in "\r\n\x00"):
        raise RuntimeError("private desktop D-Bus session unavailable")
    return {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "HOME": home, "NO_AT_BRIDGE": "1",
            "USER": account.pw_name, "LOGNAME": account.pw_name,
            "XDG_RUNTIME_DIR": str(RUNTIME), "XAUTHORITY": str(RUNTIME / "Xauthority"),
            "DBUS_SESSION_BUS_ADDRESS": bus,
            "XPRA_DEFAULT_CONF_DIRS": "", "XPRA_SYSTEM_CONF_DIRS": "", "XPRA_USER_CONF_DIRS": "",
            # 6.5.5 imports helpers belonging to disabled subsystems. Preserve
            # imports while leaving each subsystem's actual option disabled.
            # Real native-client tests check mmap/control remain unavailable.
            "XPRA_ENFORCE_FEATURES": "0", "XPRA_POWER_EVENTS": "0", "XPRA_CLIENT_CAN_SHUTDOWN": "0"}


def prepare_runtime():
    for directory in (RUNTIME, BROKER):
        info = directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != UID or stat.S_IMODE(info.st_mode) != 0o700:
            raise RuntimeError("desktop runtime must be private and owned by uid 1000")
    # Never replace a stale file or follow a link. Xpra populates this file;
    # only systemd removes it after every process in the unit has stopped.
    fd = os.open(RUNTIME / "Xauthority", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    os.close(fd)


def xpra_command():
    return ["/usr/bin/python3", "-I", "/usr/bin/xpra", "start", ":100", "--daemon=no", "--systemd-run=no",
            "--bind=" + str(RUNTIME / "xpra.sock") + ",auth=peercred(uid=0:1000,allow-owner=no),control=no,info=no",
            "--socket-dir=" + str(RUNTIME), "--socket-dirs=" + str(RUNTIME), "--socket-permissions=600",
            "--xvfb=Xvfb -screen 0 1024x768x24 -nolisten tcp -noreset -auth $XAUTHORITY",
            "--resize-display=no", "--video-encoders=none", "--encodings=png,rgb", "--opengl=no",
            "--mdns=no", "--html=off", "--http=no", "--dbus=no", "--dbus-launch=", "--source=",
            "--ssh=no", "--ssh-upgrade=no",
            "--audio=no", "--pulseaudio=no", "--speaker=disabled", "--microphone=disabled", "--webcam=no",
            "--printing=no", "--file-transfer=no", "--open-files=no", "--open-url=no",
            "--clipboard=no", "--notifications=no", "--start-new-commands=no", "--commands=no",
            # The auth dispatcher still calls the control handler when the
            # global module is disabled in 6.5.5. Keep that handler available
            # to return a refusal; the sole socket explicitly denies control.
            "--shell=no", "--mmap=no", "--control=yes", "--remote-logging=no",
            "--exit-with-client=no", "--exit-with-windows=no"]


def session_ready(env):
    try:
        endpoint = (RUNTIME / "xpra.sock").lstat()
        auth = (RUNTIME / "Xauthority").lstat()
    except FileNotFoundError:
        return False
    for info, kind in ((endpoint, stat.S_ISSOCK), (auth, stat.S_ISREG)):
        if not kind(info.st_mode) or info.st_uid != UID or stat.S_IMODE(info.st_mode) != 0o600:
            raise RuntimeError("unsafe Xpra socket or authentication file")
    if auth.st_size == 0:
        return False
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.settimeout(0.2)
            conn.connect(str(RUNTIME / "xpra.sock"))
    except (ConnectionRefusedError, FileNotFoundError, TimeoutError):
        return False
    try:
        result = subprocess.run(["/usr/bin/xdpyinfo", "-display", ":100"], env=env, timeout=2,
                                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                check=False)
    except subprocess.TimeoutExpired:
        return False
    return result.returncode == 0


def wait_for_session(server, env, stopping):
    deadline = time.monotonic() + 20
    while not stopping.is_set():
        if server.poll() is not None:
            raise RuntimeError("Xpra exited before readiness")
        if session_ready(env):
            return
        if time.monotonic() >= deadline:
            raise TimeoutError("Xpra readiness deadline exceeded")
        stopping.wait(0.05)


def stop_process(child):
    try:
        os.killpg(child.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        child.wait(timeout=5)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        child.wait(timeout=5)


def supervise(env, stopping):
    server = subprocess.Popen(xpra_command(), env=env, stdin=subprocess.DEVNULL, start_new_session=True)
    broker = None
    try:
        wait_for_session(server, env, stopping)
        if stopping.is_set():
            return
        broker = subprocess.Popen(["/usr/bin/qubes-air-desktop", "daemon"], env=env,
                                  stdin=subprocess.DEVNULL, start_new_session=True)
        while not stopping.is_set():
            if server.poll() is not None or broker.poll() is not None:
                raise RuntimeError("desktop session process exited")
            stopping.wait(0.1)
    finally:
        try:
            if broker is not None:
                stop_process(broker)
        finally:
            stop_process(server)


def main(args):
    if args:
        return 64
    if os.getuid() != UID or os.geteuid() != UID:
        return 77
    stopping = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopping.set())
    signal.signal(signal.SIGINT, lambda *_: stopping.set())
    try:
        check_versions()
        env = session_environment(pwd.getpwuid(UID), os.environ.get("DBUS_SESSION_BUS_ADDRESS", ""))
        os.umask(0o077)
        prepare_runtime()
        supervise(env, stopping)
    except (OSError, RuntimeError, TimeoutError, ImportError, KeyError):
        print("qubes-air-desktop: session unavailable; check dependencies and runtime state", file=sys.stderr)
        return 69
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
