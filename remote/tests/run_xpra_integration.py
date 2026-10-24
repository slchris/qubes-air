"""Pinned real Xpra interoperability, only inside a disposable test container."""
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import stat
import subprocess
import time

from desktop_test_support import require_container

RUNTIME = Path("/run/qubes-air-desktop")
SOCKET = RUNTIME / "xpra.sock"
BROKER = Path("/run/qubes-air-desktop-broker")
WINDOWS = ("air-xpra-fixture", "air-xpra-second")
SESSION_ARGS = ["dbus-run-session", "--", "/usr/bin/python3", "-I", "/usr/lib/qubes-air/xpra-session.py"]


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
    # Caller configuration must not add a listener to the fixed session.
    config = Path("/data/home/.xpra")
    config.mkdir()
    (config / "xpra.conf").write_text("bind-tcp=127.0.0.1:10055\n")
    env = {"PATH": "/usr/bin:/bin", "HOME": "/data/home", "LANG": "C.UTF-8",
           "XDG_RUNTIME_DIR": str(RUNTIME), "XAUTHORITY": str(RUNTIME / "Xauthority"),
           "XPRA_DEFAULT_CONF_DIRS": "", "XPRA_SYSTEM_CONF_DIRS": "", "XPRA_USER_CONF_DIRS": ""}
    with open("/tmp/xpra-fixture.log", "wb") as log:
        server = start_session(env, log)
        try:
            wait_for((BROKER / "control.sock").exists, server)
            info = SOCKET.stat()
            assert info.st_uid == 1000 and stat.S_IMODE(info.st_mode) == 0o600
            verify_listener()
            verify_control_denied(env)
            verify_duplicate_refused(env)
            launch_and_test(env, server)
        finally:
            if server.poll() is None:
                os.kill(session_pid(), signal.SIGTERM)
                assert server.wait(timeout=25) == 0, "clean session stop failed"
        verify_stopped()
        verify_session_restarts(env, log)
    assert "leaks memory" not in Path("/tmp/xpra-fixture.log").read_text()


def start_session(env, log):
    caller_env = dict(env, XPRA_USER_CONF_DIRS="/data/home/.xpra")
    return subprocess.Popen(SESSION_ARGS, env=caller_env, user=1000, group=1000, extra_groups=[],
                            stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)


def process_ids(args):
    result = []
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            command = (entry / "cmdline").read_bytes().rstrip(b"\0").split(b"\0")
            if command == [arg.encode() for arg in args] and entry.stat().st_uid == 1000:
                result.append(int(entry.name))
        except OSError:
            pass
    return result


def session_pid():
    pids = process_ids(SESSION_ARGS[2:])
    assert len(pids) == 1, pids
    return pids[0]


def verify_duplicate_refused(env):
    before = (RUNTIME / "Xauthority").read_bytes()
    result = subprocess.run(SESSION_ARGS, env=env, user=1000, group=1000, extra_groups=[],
                            capture_output=True, timeout=10, check=False)
    assert result.returncode == 69 and (RUNTIME / "Xauthority").read_bytes() == before
    for args, expected in [(SESSION_ARGS[2:], 77), (SESSION_ARGS[2:] + ["--mmap=yes"], 64)]:
        result = subprocess.run(args, env=env, capture_output=True, timeout=5, check=False)
        assert result.returncode == expected, result.stderr
    print("root, arguments and duplicate session starts are refused", flush=True)


def verify_stopped():
    assert not SOCKET.exists(), "supervisor left Xpra running"
    assert not process_ids(["/usr/bin/qubes-air-desktop", "daemon"]), "supervisor left broker running"
    assert not Path("/tmp/.X11-unix/X100").exists(), "supervisor left X11 running"
    run_tests("^TestXpraLiveSessionUnavailable$")


def verify_session_restarts(env, log):
    previous_cookie = (RUNTIME / "Xauthority").read_bytes()
    for failure in ("xpra", "broker"):
        # Only in this guarded disposable container: emulate systemd removing
        # its runtime directories after the unit's processes have stopped.
        require_container()
        for directory in (RUNTIME, BROKER):
            shutil.rmtree(directory)
            directory.mkdir(mode=0o700)
            os.chown(directory, 1000, 1000)
        session = start_session(env, log)
        try:
            wait_for((BROKER / "control.sock").exists, session)
            cookie = (RUNTIME / "Xauthority").read_bytes()
            assert cookie and cookie != previous_cookie, "cold restart reused authentication"
            previous_cookie = cookie
            if failure == "xpra":
                pid = int((RUNTIME / "xpra/100/server.pid").read_text())
            else:
                pids = process_ids(["/usr/bin/qubes-air-desktop", "daemon"])
                assert len(pids) == 1, pids
                pid = pids[0]
            os.kill(pid, signal.SIGTERM)
            assert session.wait(timeout=25) == 69, "child failure was not reported"
            verify_stopped()
            print(f"{failure} exit stops the whole session; cold start uses a new cookie", flush=True)
        finally:
            if session.poll() is None:
                stop(session)


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
    fixed = {str(SOCKET), str(BROKER / "control.sock"), "/tmp/.X11-unix/X100", "@/tmp/.X11-unix/X100"}
    extra = set(listeners) - fixed
    if not fixed.issubset(listeners) or len(extra) != 1:
        for entry in Path("/proc").iterdir():
            if entry.name.isdigit():
                try:
                    print(entry.name, (entry / "cmdline").read_bytes().replace(b"\0", b" ").decode())
                except (OSError, UnicodeError):
                    pass
    assert fixed.issubset(listeners) and len(extra) == 1, listeners
    bus = extra.pop()
    assert re.fullmatch(r"/tmp/dbus-[A-Za-z0-9]+", bus) and Path(bus).stat().st_uid == 1000, bus
    print("session has only the fixed Xpra/broker sockets, X11 and its private D-Bus", flush=True)


def verify_control_denied(env):
    result = subprocess.run(["xpra", "control", "socket://" + str(SOCKET), "hello"], env=env,
                            capture_output=True, text=True, timeout=10, check=False)
    output = result.stdout + result.stderr
    assert result.returncode != 0 and "not enabled" in output, output[-3000:]
    print("Xpra control requests are refused on the authenticated socket", flush=True)


def launch_and_test(env, server):
    entries = Path("/data/home/.local/share/applications")
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
    client_env = dict(env, DISPLAY=":101", XAUTHORITY=str(auth),
                      XPRA_MMAP_MIN_SIZE=str(64 * 1024 * 1024), XPRA_MMAP_MAX_SIZE=str(64 * 1024 * 1024))
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
                    verify_mmap_disabled(attempt + 1)
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
                             "--reconnect=no", "--mmap=yes", "--debug=mmap", "--clipboard=no", "--notifications=no",
                             "--webcam=no", "--tray=no", "--splash=no", "--printing=no",
                             "--file-transfer=no", "--open-files=no", "--open-url=no", "--encodings=png,rgb",
                             "--speaker=disabled", "--microphone=disabled"],
                            env=env, user=1000, group=1000, extra_groups=[],
                            stdout=log, stderr=log, start_new_session=True)


def verify_mmap_disabled(connections):
    output = Path("/tmp/xpra-client.log").read_text()
    # The real client creates and offers usable areas. The server must answer
    # with no mmap capabilities even when a client explicitly requests mmap.
    assert output.count("mmap supported=True") == connections, "client did not offer mmap"
    assert output.count("mmap.parse_server_capabilities(..) mmap_caps=typedict({})") == connections, \
        "server negotiated shared memory"
    print("server refuses native client mmap negotiation", flush=True)


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
    run_tests("^TestXpraLiveSessionPolicy$")
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
