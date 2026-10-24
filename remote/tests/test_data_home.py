"""Preparing HOME must never mutate an unverified mount or an existing directory."""
import importlib.util
import os
from pathlib import Path
import stat
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "lib/data-home.py"
if not SOURCE.is_file():
    SOURCE = Path("/usr/lib/qubes-air/data-home.py")
SPEC = importlib.util.spec_from_file_location("data_home", SOURCE)
PREPARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PREPARE)


class EncryptedHomeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.data = Path(self.temp.name).resolve()
        self.home = self.data / "home"
        self.account = SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid(), pw_dir=str(self.home))
        for item in (patch.multiple(PREPARE, DATA_ROOT=self.data, DESKTOP_HOME=self.home,
                                    DESKTOP_UID=os.getuid(), DESKTOP_GID=os.getgid()),
                     patch.object(PREPARE.subprocess, "run")):
            self.checker = item.start()
            self.addCleanup(item.stop)
        self.checker.return_value.stdout = "ActiveState=inactive\nLoadState=masked\n"

    def test_verified_mount_creates_private_home(self):
        PREPARE.prepare_home(self.account)
        info = self.home.stat()
        self.assertEqual((info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode)),
                         (os.getuid(), os.getgid(), 0o700))
        call = self.checker.call_args_list[0]
        self.assertEqual(call.args[0], ["/usr/bin/python3", "-I", "/usr/lib/qubes-air/data-mount.py", "mounted"])
        self.assertEqual(call.kwargs["stdin"], subprocess.DEVNULL)
        self.assertEqual(call.kwargs["timeout"], 10)
        self.assertTrue(call.kwargs["check"])
        self.assertNotIn("shell", call.kwargs)

    def test_unmasked_or_running_upstream_xpra_refuses_home_creation(self):
        for state in ("LoadState=loaded\nActiveState=inactive\n",
                      "LoadState=masked\nActiveState=active\n",
                      "LoadState=not-found\nActiveState=inactive\n", ""):
            for unit in ("xpra-server.socket", "xpra-server.service"):
                def check(argv, **_):
                    output = state if unit in argv else "LoadState=masked\nActiveState=inactive\n"
                    return subprocess.CompletedProcess(argv, 0, stdout=output)
                self.checker.side_effect = check
                with self.subTest(state=state, unit=unit), self.assertRaises(RuntimeError):
                    PREPARE.prepare_home(self.account)
                self.assertFalse(self.home.exists())

    def test_upstream_unit_inspection_failure_never_creates_home(self):
        for failure in (subprocess.CalledProcessError(1, "systemctl"),
                        subprocess.TimeoutExpired("systemctl", 10), FileNotFoundError()):
            self.checker.side_effect = [subprocess.CompletedProcess([], 0), failure]
            with self.subTest(failure=type(failure)), self.assertRaises(type(failure)):
                PREPARE.prepare_home(self.account)
            self.assertFalse(self.home.exists())
            call = self.checker.call_args
            self.assertEqual(call.args[0], ["/usr/bin/systemctl", "show", "xpra-server.socket",
                                           "--property=LoadState", "--property=ActiveState"])
            self.assertEqual(call.kwargs["timeout"], 10)
            self.assertTrue(call.kwargs["check"])

    def test_existing_valid_home_is_unchanged(self):
        self.home.mkdir(mode=0o700)
        marker = self.home / "existing"
        marker.write_text("retained user content")
        before = self.home.stat()
        with patch.object(PREPARE.os, "fchown") as chown:
            PREPARE.prepare_home(self.account)
            PREPARE.prepare_home(self.account)
            chown.assert_not_called()
        self.assertEqual(before.st_ino, self.home.stat().st_ino)
        self.assertEqual(marker.read_text(), "retained user content")

    def test_existing_home_does_not_require_reading_its_contents(self):
        self.home.mkdir(mode=0o700)
        # The root unit keeps CAP_CHOWN, but no DAC override. It must inspect
        # directory metadata without opening the user's private directory.
        with patch.object(PREPARE.os, "open", side_effect=PermissionError):
            PREPARE.prepare_home(self.account)

    def test_invalid_or_unavailable_mount_never_creates_home(self):
        for failure in (subprocess.CalledProcessError(69, "checker"),
                        subprocess.TimeoutExpired("checker", 10), FileNotFoundError()):
            with self.subTest(failure=type(failure)):
                self.checker.side_effect = failure
                with self.assertRaises(type(failure)):
                    PREPARE.prepare_home(self.account)
                self.assertFalse(self.home.exists())

    def test_account_must_use_fixed_uid_gid_and_encrypted_path(self):
        for change in ({"pw_uid": os.getuid() + 1}, {"pw_gid": os.getgid() + 1}, {"pw_dir": "/home/desktop"}):
            account = SimpleNamespace(**(vars(self.account) | change))
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                PREPARE.prepare_home(account)
        self.checker.assert_not_called()
        self.assertFalse(self.home.exists())

    def test_unsafe_existing_permissions_are_rejected_without_repair(self):
        for mode in (0o755, 0o777, 0o750):
            self.home.mkdir(mode=mode)
            self.home.chmod(mode)
            with self.subTest(mode=oct(mode)), self.assertRaises(RuntimeError):
                PREPARE.prepare_home(self.account)
            self.assertEqual(stat.S_IMODE(self.home.stat().st_mode), mode)
            self.home.rmdir()

    def test_foreign_owner_is_not_chowned(self):
        self.home.mkdir(mode=0o700)
        for field, value in (("DESKTOP_UID", os.getuid() + 1), ("DESKTOP_GID", os.getgid() + 1)):
            account = SimpleNamespace(**vars(self.account))
            setattr(account, "pw_uid" if field == "DESKTOP_UID" else "pw_gid", value)
            with patch.object(PREPARE, field, value), patch.object(PREPARE.os, "fchown") as chown:
                with self.assertRaises(RuntimeError):
                    PREPARE.prepare_home(account)
                chown.assert_not_called()

    def test_symlink_and_regular_file_are_never_replaced(self):
        target = self.data / "unrelated"
        target.mkdir(mode=0o700)
        self.home.symlink_to(target, target_is_directory=True)
        with self.assertRaises((RuntimeError, OSError)):
            PREPARE.prepare_home(self.account)
        self.assertTrue(self.home.is_symlink())
        self.home.unlink()
        self.home.write_text("existing file")
        with self.assertRaises((RuntimeError, OSError)):
            PREPARE.prepare_home(self.account)
        self.assertEqual(self.home.read_text(), "existing file")

    def test_creation_failure_never_reports_success(self):
        with patch.object(PREPARE.os, "fchown", side_effect=PermissionError):
            with self.assertRaises(PermissionError):
                PREPARE.prepare_home(self.account)

    def test_entry_is_root_only_and_has_no_arguments(self):
        with patch.object(PREPARE, "prepare_home") as prepare:
            self.assertEqual(PREPARE.main(["/tmp/elsewhere"]), 64)
            with patch.object(PREPARE.os, "geteuid", return_value=1000):
                self.assertEqual(PREPARE.main([]), 77)
            prepare.assert_not_called()


if __name__ == "__main__":
    unittest.main()
