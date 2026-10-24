"""GIO integration tests. Run inside the isolated Linux desktop test image."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest

from desktop_test_support import require_container

WORKER = Path("/usr/lib/qubes-air/desktop-worker.py")


class DesktopWorkerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        require_container()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="air-gio-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        os.chmod(self.root, 0o755)
        self.home = self.root / "home"
        self.system = self.root / "system"
        self.local = self.home / ".local/share/applications"
        self.local.mkdir(parents=True)
        (self.system / "applications").mkdir(parents=True)
        os.chown(self.home, 1000, 1000)
        self.env = dict(PATH="/usr/bin:/bin", LANG="C.UTF-8", HOME=str(self.home),
                        XDG_DATA_HOME=str(self.home / ".local/share"),
                        XDG_DATA_DIRS=str(self.system), XDG_CURRENT_DESKTOP="Qubes",
                        DISPLAY=":100", XAUTHORITY="/run/qubes-air-desktop/Xauthority")

    def entry(self, name, fields, local=False):
        base = self.local if local else self.system / "applications"
        path = base / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("[Desktop Entry]\nType=Application\nName=Editor\nExec=/usr/bin/true\n" + fields)
        return path

    def run_worker(self, *args, uid=1000):
        result = subprocess.run(["/usr/bin/python3", "-I", str(WORKER), *args],
                                env=self.env, stdin=subprocess.DEVNULL,
                                capture_output=True, timeout=12, user=uid, group=uid, extra_groups=[])
        self.assertEqual(result.stderr, b"")
        reply = json.loads(result.stdout)
        self.assertEqual(result.returncode, reply.get("code", 0))
        return reply

    def test_nested_id_precedence_and_visibility(self):
        self.entry("nested/editor.desktop", "Name=Nested editor\n")
        self.entry("override.desktop", "Name=System\n")
        self.entry("override.desktop", "Name=Personal\n", local=True)
        self.entry("hidden.desktop", "")
        self.entry("hidden.desktop", "Hidden=true\n", local=True)
        self.entry("nodisplay.desktop", "NoDisplay=true\n")
        self.entry("other.desktop", "OnlyShowIn=Other;\n")
        self.entry("saver.desktop", "Categories=Screensaver;\n")
        self.entry("missing.desktop", "TryExec=/nonexistent/desktop-test\n")
        reply = self.run_worker("menus")
        self.assertTrue(reply["ok"])
        self.assertIn("nested-editor.desktop:Name=Nested editor\n", reply["menu"])
        self.assertIn("override.desktop:Name=Personal\n", reply["menu"])
        for hidden in ("hidden", "nodisplay", "other", "saver", "missing"):
            self.assertNotIn(hidden + ".desktop:", reply["menu"])
        self.assertNotIn("Name=System", reply["menu"])

    def test_metadata_is_bounded_and_allowlisted(self):
        self.entry("editor.desktop", "Icon=/tmp/private-icon\nComment=Safe\nX-Private=not-exported\n")
        self.entry("large.desktop", "Name=" + "x" * 2049 + "\n")
        self.entry("My App.desktop", "")
        reply = self.run_worker("menus")
        self.assertTrue(reply["ok"])
        self.assertIn("editor.desktop:Exec=qubes-desktop-run editor.desktop\n", reply["menu"])
        for text in ("/tmp/private-icon", "not-exported", "large.desktop:", "My App.desktop:"):
            self.assertNotIn(text, reply["menu"])

    def test_menu_limit_is_failure_not_partial_success(self):
        for index in range(257):
            self.entry(f"app{index}.desktop", "")
        self.assertEqual(self.run_worker("menus")["code"], 70)

    def test_invalid_request_and_root_refused(self):
        for args in ((), ("launch",), ("launch", "../app"), ("menus", "extra"), ("launch", "a" * 114)):
            self.assertEqual(self.run_worker(*args)["code"], 64)
        self.assertEqual(self.run_worker("menus", uid=0)["code"], 77)

    def test_unavailable_display_and_hidden_launch(self):
        self.entry("editor.desktop", "")
        self.env["DISPLAY"] = "remote:100"
        self.assertEqual(self.run_worker("launch", "editor")["code"], 69)
        self.env["DISPLAY"] = ":100"
        self.entry("hidden.desktop", "Hidden=true\n")
        self.assertEqual(self.run_worker("launch", "hidden")["code"], 69)

    def test_gio_argv_uid_and_survival_after_worker_exit(self):
        recorder = self.root / "record.py"
        record = self.home / "result.json"
        recorder.write_text("import json,os,sys,time\nfrom pathlib import Path\n"
                            "Path(sys.argv[1]).write_text(json.dumps(dict(uid=os.getuid(),args=sys.argv[2:],pid=os.getpid())))\n"
                            "time.sleep(10)\n")
        entry = self.entry("nested/record.desktop", f'Name=Two Words\nExec=/usr/bin/python3 {recorder} {record} %c %k "quoted words" %%\n')
        reply = self.run_worker("launch", "nested-record.desktop")
        self.assertTrue(reply["ok"], reply)
        until = time.monotonic() + 3
        while not record.exists() and time.monotonic() < until:
            time.sleep(0.02)
        data = json.loads(record.read_text())
        self.assertEqual(data["uid"], 1000)
        self.assertEqual(data["args"], ["Two Words", str(entry), "quoted words", "%"])
        os.kill(data["pid"], 0)
        self.addCleanup(os.kill, data["pid"], 15)

    def test_real_window_survives_rpc_worker(self):
        self.entry("clock.desktop", "Exec=/usr/bin/xclock -name air-desktop-fixture\n")
        reply = self.run_worker("launch", "clock")
        self.assertTrue(reply["ok"], reply)
        until = time.monotonic() + 3
        while time.monotonic() < until:
            windows = subprocess.run(["/usr/bin/xwininfo", "-root", "-tree"], env=self.env,
                                     capture_output=True, check=True, user=1000, group=1000, extra_groups=[])
            if b"air-desktop-fixture" in windows.stdout:
                return
            time.sleep(0.05)
        self.fail("launched X11 window did not appear")

    def test_missing_program_reports_failure(self):
        self.entry("missing.desktop", "Exec=/nonexistent/qubes-air-fixture\n")
        self.assertFalse(self.run_worker("launch", "missing")["ok"])


if __name__ == "__main__":
    unittest.main()
