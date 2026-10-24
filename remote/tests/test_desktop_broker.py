"""End-to-end checks through installed binaries, services and kernel peers."""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import time
import unittest
from unittest import mock

from desktop_test_support import require_container

CLIENT = "/usr/bin/qubes-air-desktop"
SOCKET = Path("/run/qubes-air-desktop-broker/control.sock")


class DesktopBrokerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        require_container()

    def setUp(self):
        self.process = None
        self.log = open("/tmp/air-desktop-broker-test.log", "ab")
        self.addCleanup(self.log.close)
        self.addCleanup(self.stop)
        # Simulate systemd's cleanup of this isolated test unit's runtime dir.
        # The daemon itself must never unlink a live, stale or replaced socket.
        if SOCKET.exists():
            SOCKET.unlink()
        self.entries = Path("/home/desktop/.local/share/applications")
        self.entries.mkdir(parents=True, exist_ok=True)
        self.entry = self.entries / "air-fixture.desktop"
        self.entry.write_text("[Desktop Entry]\nType=Application\nName=Air fixture\nExec=/usr/bin/xclock -name air-broker-fixture\n")

    def start(self):
        self.process = subprocess.Popen(["dbus-run-session", "--", CLIENT, "daemon"],
                                        user=1000, group=1000, extra_groups=[],
                                        stdin=subprocess.DEVNULL, stdout=self.log, stderr=self.log,
                                        start_new_session=True, env={"PATH": "/usr/bin:/bin", "HOME": "/home/desktop"})
        for _ in range(100):
            if SOCKET.exists():
                return
            if self.process.poll() is not None:
                self.fail("broker failed to start; inspect /tmp/air-desktop-broker-test.log")
            time.sleep(0.02)
        self.fail("broker did not become ready")

    def stop(self):
        if self.process is not None:
            try:
                os.killpg(self.process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            self.process.wait(timeout=5)
            self.process = None

    def client(self, *args, uid=0):
        return subprocess.run([CLIENT, *args], stdin=subprocess.DEVNULL, capture_output=True,
                              user=uid, group=uid, extra_groups=[], timeout=12)

    def test_actual_root_and_desktop_clients(self):
        self.start()
        for uid in (0, 1000):
            reply = self.client("menus", uid=uid)
            self.assertEqual(reply.returncode, 0, reply.stderr)
            self.assertIn(b"air-fixture.desktop:Name=Air fixture", reply.stdout)
        reply = subprocess.run(["/etc/qubes-rpc/qubes.StartApp", "air-fixture.desktop"],
                               input=b"ignored payload", capture_output=True, timeout=12)
        self.assertEqual(reply.returncode, 0, reply.stderr)
        self.assertIn(b"accepted 'air-fixture.desktop' on :100", reply.stdout)
        self.assertNotEqual(self.client("menus", uid=65534).returncode, 0)

    def test_root_broker_and_unavailable_service_fail_closed(self):
        self.assertEqual(self.client("daemon").returncode, 69)
        self.assertEqual(self.client("menus").returncode, 69)
        self.assertEqual(self.client("launch", "../editor").returncode, 64)
        self.assertEqual(self.client("menus", "extra").returncode, 64)

    def test_client_rejects_root_socket_before_sending(self):
        with socket.socket(socket.AF_UNIX) as listener:
            listener.bind(str(SOCKET))
            listener.listen(1)
            result = self.client("launch", "air-fixture")
            self.assertEqual(result.returncode, 77)
            with listener.accept()[0] as peer:
                self.assertEqual(peer.recv(512), b"")

    def test_repeated_start_refuses_and_clean_restart_recovers(self):
        self.start()
        second = subprocess.run(["dbus-run-session", "--", CLIENT, "daemon"],
                                user=1000, group=1000, extra_groups=[], capture_output=True, timeout=3)
        self.assertNotEqual(second.returncode, 0)
        self.assertEqual(self.client("menus").returncode, 0)
        self.stop()
        self.assertTrue(SOCKET.exists(), "daemon must leave cleanup to systemd")
        self.assertEqual(self.client("menus").returncode, 69)
        SOCKET.unlink()
        self.start()
        self.assertEqual(self.client("menus").returncode, 0)

    def test_excess_input_is_rejected_without_dispatch(self):
        self.start()
        with socket.socket(socket.AF_UNIX) as connection:
            connection.settimeout(2)
            connection.connect(str(SOCKET))
            connection.sendall(b"x" * 513)
            connection.shutdown(socket.SHUT_WR)
            reply = json.loads(connection.recv(512))
            self.assertEqual(reply["code"], 64)
        self.assertEqual(self.client("menus").returncode, 0)

    def test_fixture_guard_refuses_missing_marker(self):
        with mock.patch.dict(os.environ, {"QUBESAIR_DESKTOP_TEST_CONTAINER": ""}):
            with self.assertRaises(RuntimeError):
                require_container()
