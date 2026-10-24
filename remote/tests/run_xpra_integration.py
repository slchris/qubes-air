"""Pinned real Xpra interoperability, only inside a disposable test container."""
import os
from pathlib import Path
import secrets
import signal
import stat
import subprocess
import time

from desktop_test_support import require_container

RUNTIME = Path("/run/qubes-air-desktop")
SOCKET = RUNTIME / "xpra.sock"
BROKER = Path("/run/qubes-air-desktop-broker")
WINDOWS = ("air-xpra-fixture", "air-xpra-second")


def wait_for(check, process):
    for _ in range(200):
        if check():
            return
        if process.poll() is not None:
            raise RuntimeError("desktop fixture exited before becoming ready")
        time.sleep(0.05)
    raise RuntimeError("desktop fixture did not become ready")


def stop(process):
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    process.wait(timeout=10)


def run_tests(name, uid=0):
    subprocess.run(["/tmp/xpra-live.test", "-test.v", "-test.timeout=60s", "-test.run=" + name],
                   user=uid, group=uid, extra_groups=[], timeout=65, check=True)


def main():
    require_container()
    for directory in (RUNTIME, BROKER):
        directory.mkdir(mode=0o700)
        os.chown(directory, 1000, 1000)
    Path("/tmp/.X11-unix").mkdir(mode=0o1777)
    os.chmod("/tmp/.X11-unix", 0o1777)
    # Xpra only reuses an existing writable XAUTHORITY. Precreate the fixed
    # private file so its default path/mode cannot drift from the broker.
    auth = RUNTIME / "Xauthority"
    auth.touch(mode=0o600)
    os.chown(auth, 1000, 1000)
    env = {"PATH": "/usr/bin:/bin", "HOME": "/home/desktop", "LANG": "C.UTF-8",
           "XDG_RUNTIME_DIR": str(RUNTIME), "XAUTHORITY": str(RUNTIME / "Xauthority"),
           "XPRA_DEFAULT_CONF_DIRS": "", "XPRA_SYSTEM_CONF_DIRS": "", "XPRA_USER_CONF_DIRS": ""}
    args = ["/usr/bin/xpra", "start", ":100", "--daemon=no", "--systemd-run=no",
            "--bind=" + str(SOCKET) + ",auth=peercred(uid=0:1000,allow-owner=no),control=no,info=no",
            "--socket-dir=" + str(RUNTIME), "--socket-dirs=" + str(RUNTIME), "--socket-permissions=600",
            "--xvfb=Xvfb -screen 0 1024x768x24 -nolisten tcp -noreset -auth $XAUTHORITY",
            "--resize-display=no", "--video-encoders=none", "--encodings=png,rgb",
            "--mdns=no", "--html=off", "--http=no", "--dbus=no", "--source=", "--ssh-upgrade=no",
            "--pulseaudio=no", "--speaker=disabled", "--microphone=disabled", "--webcam=no",
            "--printing=no", "--file-transfer=no", "--open-files=no", "--open-url=no",
            "--clipboard=no", "--notifications=no", "--start-new-commands=no",
            # 6.5.5 imports mmap helpers from its window encoder even when the
            # client disables mmap. Keep the module loaded; this fixture's GTK
            # client disables negotiation, as does the Go screenshot hello.
            "--shell=no", "--mmap=yes", "--exit-with-client=no", "--exit-with-windows=no"]
    with open("/tmp/xpra-fixture.log", "wb") as log:
        server = subprocess.Popen(args, env=env, user=1000, group=1000, extra_groups=[],
                                  stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
        try:
            wait_for(SOCKET.exists, server)
            info = SOCKET.stat()
            assert info.st_uid == 1000 and stat.S_IMODE(info.st_mode) == 0o600
            verify_control_denied(env)
            verify_listener()
            broker = subprocess.Popen(["dbus-run-session", "--", "/usr/bin/qubes-air-desktop", "daemon"],
                                      env=env, user=1000, group=1000, extra_groups=[],
                                      stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
            try:
                wait_for((BROKER / "control.sock").exists, broker)
                launch_and_test(env, server)
            finally:
                stop(broker)
        finally:
            stop(server)
    run_tests("^TestXpraLiveSessionUnavailable$")


def verify_listener():
    # This container has no network and no other services. Inspect the whole
    # namespace, without CAP_SYS_PTRACE or access to another UID's /proc/fd.
    for table in ("/proc/net/tcp", "/proc/net/tcp6"):
        for line in Path(table).read_text().splitlines()[1:]:
            fields = line.split()
            assert fields[3] != "0A", "Xpra unexpectedly listens on TCP"
    listeners = []
    for line in Path("/proc/net/unix").read_text().splitlines()[1:]:
        fields = line.split()
        if fields[3] == "00010000":
            listeners.append(fields[7])
    assert set(listeners) == {str(SOCKET), "/tmp/.X11-unix/X100", "@/tmp/.X11-unix/X100"}, listeners
    print("Xpra listens only on the fixed Unix socket", flush=True)


def verify_control_denied(env):
    result = subprocess.run(["xpra", "control", "socket://" + str(SOCKET), "hello"], env=env,
                            capture_output=True, text=True, timeout=10, check=False)
    output = result.stdout + result.stderr
    assert result.returncode != 0 and "not enabled" in output, output[-3000:]
    print("Xpra control requests are refused on the authenticated socket", flush=True)


def launch_and_test(env, server):
    entries = Path("/home/desktop/.local/share/applications")
    entries.mkdir(parents=True)
    for index, name in enumerate(WINDOWS):
        (entries / (name + ".desktop")).write_text(
            "[Desktop Entry]\nType=Application\nName=Air Xpra fixture\n"
            f"Exec=/usr/bin/xclock -name {name} -title {name} -geometry 180x180+{20 + index * 220}+20\n")
        subprocess.run(["/usr/bin/qubes-air-desktop", "launch", name], check=True, timeout=12)

    def has_window():
        reply = subprocess.run(["xwininfo", "-root", "-tree"], env=dict(env, DISPLAY=":100"),
                               user=1000, group=1000, extra_groups=[], capture_output=True, timeout=2, check=True)
        return all(name.encode() in reply.stdout for name in WINDOWS)

    wait_for(has_window, server)
    original_ids = server_window_ids(env)
    # A seamless window gets its pixels when a GUI client maps it. Use an
    # actual native Xpra client on a separate X display, not a fake wire peer.
    auth = RUNTIME / "client.Xauthority"
    subprocess.run(["xauth", "-f", str(auth), "add", ":101", ".", secrets.token_hex(16)],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    os.chown(auth, 1000, 1000)
    os.chmod(auth, 0o600)
    client_env = dict(env, DISPLAY=":101", XAUTHORITY=str(auth))
    with open("/tmp/xpra-client.log", "wb") as log:
        display = subprocess.Popen(["Xvfb", ":101", "-screen", "0", "1024x768x24", "-nolisten", "tcp", "-auth", str(auth)],
                                   env=client_env, user=1000, group=1000, extra_groups=[],
                                   stdout=log, stderr=log, start_new_session=True)
        try:
            wait_for(lambda: Path("/tmp/.X11-unix/X101").exists(), display)
            for attempt in range(2):
                client = start_client(client_env, log)
                try:
                    wait_for(lambda: client_has_windows(client_env), client)
                    assert server_window_ids(env) == original_ids, "client reconnect must preserve application windows"
                    verify_streams()
                    print(f"native client connection {attempt + 1}: both original windows present", flush=True)
                finally:
                    stop(client)
                assert server.poll() is None, "detaching the client must leave the session running"
        finally:
            stop(display)


def start_client(env, log):
    return subprocess.Popen(["xpra", "attach", "socket://" + str(SOCKET), "--opengl=no", "--mdns=no",
                             "--bind=none",
                             "--reconnect=no", "--mmap=no", "--clipboard=no", "--notifications=no",
                             "--webcam=no", "--tray=no", "--splash=no", "--printing=no",
                             "--file-transfer=no", "--open-files=no", "--open-url=no", "--encodings=png,rgb",
                             "--speaker=disabled", "--microphone=disabled"],
                            env=env, user=1000, group=1000, extra_groups=[],
                            stdout=log, stderr=log, start_new_session=True)


def client_has_windows(env):
    reply = subprocess.run(["xwininfo", "-root", "-tree"], env=env, user=1000, group=1000, extra_groups=[],
                           capture_output=True, timeout=2, check=True)
    return all(sum(name.encode() in line for line in reply.stdout.splitlines()) == 1 for name in WINDOWS)


def server_window_ids(env):
    reply = subprocess.run(["xwininfo", "-root", "-tree"], env=dict(env, DISPLAY=":100"),
                           user=1000, group=1000, extra_groups=[], capture_output=True, timeout=2, check=True)
    return {name: next(line.split()[0] for line in reply.stdout.splitlines() if name.encode() in line)
            for name in WINDOWS}


def verify_streams():
    run_tests("^TestXpraLiveKernelPeer$", 0)
    run_tests("^TestXpraLiveKernelPeer$", 1000)
    # Permit filesystem traversal solely in this disposable negative fixture,
    # so UID 1001 reaches the real Xpra peercred check instead of just EACCES.
    os.chmod(RUNTIME, 0o711)
    os.chmod(SOCKET, 0o666)
    try:
        run_tests("^TestXpraLiveKernelPeer$", 1001)
    finally:
        os.chmod(SOCKET, 0o600)
        os.chmod(RUNTIME, 0o700)
    run_tests("^TestXpraLiveScreenshotOverMTLS$")


if __name__ == "__main__":
    try:
        main()
    except Exception:
        for name in ("/tmp/xpra-fixture.log", "/tmp/xpra-client.log"):
            log_path = Path(name)
            if log_path.exists():
                print(log_path.read_text(errors="replace")[-12000:])
        raise
