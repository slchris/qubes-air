"""Session failures must not start the broker or leave supervised children alive."""
import importlib.util
import os
from pathlib import Path
import signal
import socket
import stat
import subprocess
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

SOURCE = Path(__file__).resolve().parents[1] / "lib/xpra-session.py"
if not SOURCE.is_file():
    SOURCE = Path("/usr/lib/qubes-air/xpra-session.py")
SPEC = importlib.util.spec_from_file_location("xpra_session", SOURCE)
SESSION = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SESSION)


class SessionPolicyTests(unittest.TestCase):
    def test_root_and_arguments_refused_before_any_work(self):
        with patch.object(SESSION, "supervise") as supervise:
            with patch.object(SESSION.os, "getuid", return_value=0):
                self.assertEqual(SESSION.main([]), 77)
            self.assertEqual(SESSION.main(["--bind-tcp=1234"]), 64)
            supervise.assert_not_called()

    def test_versions_are_checked_without_starting(self):
        for xpra, gi, valid in [("6.5.5", (3, 50, 0), True), ("6.5.4", (3, 50, 0), False),
                                ("6.6", (3, 50, 0), False), ("6.5.5", (3, 42, 2), False)]:
            with self.subTest(xpra=xpra, gi=gi):
                modules = {"xpra": SimpleNamespace(__version__=xpra), "gi": SimpleNamespace(version_info=gi)}
                with patch.dict("sys.modules", modules):
                    if valid:
                        SESSION.check_versions()
                    else:
                        with self.assertRaises(RuntimeError):
                            SESSION.check_versions()

    def test_environment_discards_caller_configuration(self):
        account = SimpleNamespace(pw_uid=1000, pw_name="desktop", pw_dir="/data/home")
        with patch.dict(os.environ, {"HOME": "/root", "PYTHONPATH": "/tmp/inject", "XPRA_MMAP_DIR": "/tmp"}):
            env = SESSION.session_environment(account, "unix:path=/tmp/private-bus")
        self.assertEqual(env["HOME"], "/data/home")
        self.assertEqual(env["XPRA_ENFORCE_FEATURES"], "0")
        self.assertEqual(env["XPRA_USER_CONF_DIRS"], "")
        self.assertNotIn("PYTHONPATH", env)
        self.assertNotIn("XPRA_MMAP_DIR", env)
        for home in ["/", "relative", "/home/../root", "/home/desktop/", "/home/desktop"]:
            account.pw_dir = home
            with self.subTest(home=home), self.assertRaises(RuntimeError):
                SESSION.session_environment(account, "unix:path=/tmp/private-bus")
        account.pw_dir = "/data/home"
        for bus in ["", "tcp:host=example", "unix:path=/tmp/bus\n", "unix:path=/tmp/bus\x00"]:
            with self.subTest(bus=bus), self.assertRaises(RuntimeError):
                SESSION.session_environment(account, bus)

    def test_unavailable_home_never_starts_a_session(self):
        with patch.object(SESSION.os, "getuid", return_value=1000), \
                patch.object(SESSION.os, "geteuid", return_value=1000), \
                patch.object(SESSION.signal, "signal"), patch.object(SESSION, "check_versions"), \
                patch.object(SESSION.pwd, "getpwuid", return_value=SimpleNamespace(pw_uid=1000)), \
                patch.object(SESSION.os, "umask"), \
                patch.object(SESSION, "session_environment", return_value={"HOME": "/data/home"}), \
                patch.object(SESSION.os, "chdir", side_effect=FileNotFoundError), \
                patch.object(SESSION, "prepare_runtime") as prepare, \
                patch.object(SESSION, "supervise") as supervise:
            self.assertEqual(SESSION.main([]), 69)
            prepare.assert_not_called()
            supervise.assert_not_called()

    def test_runtime_files_are_exclusive_and_private(self):
        with tempfile.TemporaryDirectory() as temp:
            runtime = Path(temp) / "runtime"
            broker = Path(temp) / "broker"
            runtime.mkdir(mode=0o700)
            broker.mkdir(mode=0o700)
            with patch.multiple(SESSION, RUNTIME=runtime, BROKER=broker, UID=os.getuid()):
                SESSION.prepare_runtime()
                auth = runtime / "Xauthority"
                self.assertEqual(stat.S_IMODE(auth.stat().st_mode), 0o600)
                auth.write_text("existing authentication")
                with self.assertRaises(FileExistsError):
                    SESSION.prepare_runtime()
                self.assertEqual(auth.read_text(), "existing authentication")
                auth.unlink()
                auth.symlink_to(runtime / "unrelated")
                with self.assertRaises(FileExistsError):
                    SESSION.prepare_runtime()
                self.assertFalse((runtime / "unrelated").exists())

    def test_unsafe_runtime_never_creates_authentication(self):
        with tempfile.TemporaryDirectory() as temp:
            runtime = Path(temp) / "runtime"
            broker = Path(temp) / "broker"
            runtime.mkdir(mode=0o755)
            broker.mkdir(mode=0o700)
            with patch.multiple(SESSION, RUNTIME=runtime, BROKER=broker, UID=os.getuid()):
                with self.assertRaises(RuntimeError):
                    SESSION.prepare_runtime()
                self.assertFalse((runtime / "Xauthority").exists())
                runtime.chmod(0o700)
                with patch.object(SESSION, "UID", os.getuid() + 1), self.assertRaises(RuntimeError):
                    SESSION.prepare_runtime()
                link = Path(temp) / "link"
                link.symlink_to(runtime)
                with patch.object(SESSION, "RUNTIME", link), self.assertRaises(RuntimeError):
                    SESSION.prepare_runtime()

    def test_startup_exit_and_timeout_do_not_start_broker(self):
        for failure in [RuntimeError("exited"), TimeoutError("not ready")]:
            server = Mock()
            with patch.object(SESSION.subprocess, "Popen", return_value=server) as start, \
                    patch.object(SESSION, "wait_for_session", side_effect=failure), \
                    patch.object(SESSION, "stop_process") as stop:
                with self.assertRaises(type(failure)):
                    SESSION.supervise({}, threading.Event())
                self.assertEqual(start.call_count, 1)
                stop.assert_called_once_with(server)

    def test_readiness_needs_listening_socket_and_private_auth(self):
        with tempfile.TemporaryDirectory() as temp:
            runtime = Path(temp)
            auth = runtime / "Xauthority"
            auth.write_text("fixture")
            auth.chmod(0o600)
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener, \
                    patch.multiple(SESSION, RUNTIME=runtime, UID=os.getuid()), \
                    patch.object(SESSION.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as probe:
                listener.bind(str(runtime / "xpra.sock"))
                (runtime / "xpra.sock").chmod(0o600)
                self.assertFalse(SESSION.session_ready({}))
                probe.assert_not_called()
                listener.listen(8)
                self.assertTrue(SESSION.session_ready({}))
                auth.chmod(0o644)
                with self.assertRaises(RuntimeError):
                    SESSION.session_ready({})
                auth.chmod(0o600)
                with patch.object(SESSION, "UID", os.getuid() + 1), self.assertRaises(RuntimeError):
                    SESSION.session_ready({})
                probe.side_effect = subprocess.TimeoutExpired("xdpyinfo", 2)
                self.assertFalse(SESSION.session_ready({}))

    def test_readiness_has_deadline_and_detects_exit(self):
        server = Mock(poll=Mock(return_value=None))
        with patch.object(SESSION, "session_ready", return_value=False), \
                patch.object(SESSION.time, "monotonic", side_effect=[0, 21]):
            with self.assertRaises(TimeoutError):
                SESSION.wait_for_session(server, {}, threading.Event())
        server.poll.return_value = 1
        with patch.object(SESSION, "session_ready") as ready, self.assertRaises(RuntimeError):
            SESSION.wait_for_session(server, {}, threading.Event())
        ready.assert_not_called()

    def test_clean_stop_has_no_failure_and_stops_both_children(self):
        server = Mock(poll=Mock(return_value=None))
        broker = Mock(poll=Mock(return_value=None))
        stopping = threading.Event()
        stopped = []
        with patch.object(SESSION.subprocess, "Popen", side_effect=[server, broker]), \
                patch.object(SESSION, "wait_for_session"), \
                patch.object(stopping, "wait", side_effect=lambda _: stopping.set()), \
                patch.object(SESSION, "stop_process", side_effect=stopped.append):
            SESSION.supervise({}, stopping)
        self.assertEqual(stopped, [broker, server])

    def test_child_failure_stops_broker_before_xpra(self):
        for server_status, broker_status in [(1, None), (None, 1)]:
            server = Mock(poll=Mock(return_value=server_status))
            broker = Mock(poll=Mock(return_value=broker_status))
            stopped = []
            with patch.object(SESSION.subprocess, "Popen", side_effect=[server, broker]), \
                    patch.object(SESSION, "wait_for_session"), \
                    patch.object(SESSION, "stop_process", side_effect=stopped.append):
                with self.assertRaises(RuntimeError):
                    SESSION.supervise({}, threading.Event())
                self.assertEqual(stopped, [broker, server])

    def test_cancel_during_startup_never_starts_broker(self):
        stopping = threading.Event()
        stopping.set()
        server = Mock()
        with patch.object(SESSION.subprocess, "Popen", return_value=server) as start, \
                patch.object(SESSION, "wait_for_session"), patch.object(SESSION, "stop_process") as stop:
            SESSION.supervise({}, stopping)
        self.assertEqual(start.call_count, 1)
        stop.assert_called_once_with(server)

    def test_stubborn_child_is_killed_and_reaped(self):
        child = Mock(pid=12345, wait=Mock(side_effect=[subprocess.TimeoutExpired("fixture", 5), 0]))
        with patch.object(SESSION.os, "killpg") as kill:
            SESSION.stop_process(child)
        self.assertEqual(kill.call_args_list, [unittest.mock.call(12345, signal.SIGTERM),
                                              unittest.mock.call(12345, signal.SIGKILL)])
        self.assertEqual(child.wait.call_count, 2)


if __name__ == "__main__":
    unittest.main()
